"""HTTP wrapper around the Typst CLI: turns markdown documents into one
typeset PDF per request. Internal-only sidecar, same trust boundary as
markitdown/searxng. No auth: never expose this port outside the
compose network.
"""

import re
import subprocess
import tempfile
from pathlib import Path

from fastapi import FastAPI, Request
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import JSONResponse, Response
from pydantic import BaseModel, Field

app = FastAPI()

TEMPLATE_PATH = Path(__file__).parent / "template.typ"
STDERR_LIMIT = 2000

# Caps one render request. Markdown only, no binary payload, so this
# sits well above any real document set while stopping an unbounded
# body from being buffered into memory before pydantic parses it.
MAX_BODY_BYTES = 16 * 1024 * 1024

# Bounds one typst compile. Mermaid diagrams and code highlighting can
# run long, so this stays generous, but below the client's own 120s
# read timeout (internal/platform/pdfgen) so a wedged compile surfaces
# as a clean error here rather than a client-side timeout with the
# subprocess still running.
TYPST_TIMEOUT_SECONDS = 90

# Matches the absolute temp workdir typst names in diagnostics. The
# workdir is per-request and meaningless to the caller; leaking it
# hands an attacker host layout for free. The filename after it stays:
# "main.typ" or "doc3.md" is what makes the error actionable.
_TEMPDIR_PATH = re.compile(r"(?:/private)?/(?:tmp|var/folders)/[^\s:]*?/([\w.-]+\.(?:typ|md|pdf))")


def _scrub_stderr(stderr: str) -> str:
    """Strips absolute temp paths out of typst diagnostics, keeping the
    filename and the compile error itself."""
    return _TEMPDIR_PATH.sub(r"\1", stderr)


class Document(BaseModel):
    title: str
    content: str


class Options(BaseModel):
    cover_title: str = ""
    toc: bool = False


class RenderRequest(BaseModel):
    documents: list[Document] = Field(default_factory=list)
    options: Options = Field(default_factory=Options)


@app.middleware("http")
async def cap_body_size(request: Request, call_next):
    """Rejects oversized bodies before the route parses them.

    The render route takes a pydantic model, so FastAPI would buffer
    the whole body itself; the check has to sit ahead of it. A declared
    Content-Length is refused without reading any body at all.
    """
    declared = request.headers.get("content-length")
    if declared is not None:
        try:
            if int(declared) > MAX_BODY_BYTES:
                return JSONResponse(status_code=413, content={"error": "request body too large"})
        except ValueError:
            return JSONResponse(status_code=400, content={"error": "invalid content-length"})

    # A chunked body declares no length, so the stream itself is capped
    # too: once past the limit the body is truncated to an immediate
    # end-of-stream rather than buffered further. Truncating (instead of
    # raising) keeps the failure inside the normal request flow, where
    # FastAPI's own body parsing would otherwise swallow the exception
    # and report it as a 400; `state.body_too_large` is what turns the
    # result back into the 413 the caller should see.
    received = 0
    original_receive = request.receive
    request.state.body_too_large = False

    async def capped_receive():
        nonlocal received
        if request.state.body_too_large:
            return {"type": "http.request", "body": b"", "more_body": False}
        message = await original_receive()
        if message["type"] == "http.request":
            received += len(message.get("body", b""))
            if received > MAX_BODY_BYTES:
                request.state.body_too_large = True
                return {"type": "http.request", "body": b"", "more_body": False}
        return message

    request._receive = capped_receive
    response = await call_next(request)
    if request.state.body_too_large:
        return JSONResponse(status_code=413, content={"error": "request body too large"})
    return response


@app.get("/healthz")
def healthz():
    return {"status": "ok"}


def _typ_string(s: str) -> str:
    """Escapes a Python string into a Typst string literal."""
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


def _compile(workdir: Path, out_pdf: Path) -> subprocess.CompletedProcess:
    """Runs one bounded typst compile.

    Raises subprocess.TimeoutExpired past TYPST_TIMEOUT_SECONDS;
    subprocess.run kills and reaps the child before re-raising, so no
    orphaned typst process survives the request. Called off the event
    loop: the compile blocks, and blocking the loop would stall
    healthz and every concurrent render behind one slow document.
    """
    return subprocess.run(
        ["typst", "compile", "main.typ", str(out_pdf)],
        cwd=workdir,
        capture_output=True,
        text=True,
        timeout=TYPST_TIMEOUT_SECONDS,
    )


@app.post("/render")
async def render(req: RenderRequest):
    if not req.documents:
        return JSONResponse(status_code=400, content={"error": "documents must not be empty"})
    for doc in req.documents:
        if not doc.title.strip():
            return JSONResponse(status_code=400, content={"error": "document title must not be empty"})

    with tempfile.TemporaryDirectory() as workdir:
        workdir = Path(workdir)
        (workdir / "template.typ").write_bytes(TEMPLATE_PATH.read_bytes())

        doc_entries = []
        for i, doc in enumerate(req.documents):
            md_path = workdir / f"doc{i}.md"
            md_path.write_text(doc.content, encoding="utf-8")
            doc_entries.append(f'(title: {_typ_string(doc.title)}, content: read("doc{i}.md"))')

        main_typ = (
            '#import "template.typ": render-doc\n'
            "#render-doc(\n"
            f"  ({', '.join(doc_entries)},),\n"
            f"  cover-title: {_typ_string(req.options.cover_title)},\n"
            f"  toc: {'true' if req.options.toc else 'false'},\n"
            ")\n"
        )
        (workdir / "main.typ").write_text(main_typ, encoding="utf-8")

        out_pdf = workdir / "out.pdf"
        try:
            result = await run_in_threadpool(_compile, workdir, out_pdf)
        except subprocess.TimeoutExpired:
            return JSONResponse(
                status_code=504,
                content={"error": f"typst compile timed out after {TYPST_TIMEOUT_SECONDS}s"},
            )
        if result.returncode != 0:
            stderr = _scrub_stderr(result.stderr)[-STDERR_LIMIT:]
            return JSONResponse(status_code=500, content={"error": f"typst compile failed: {stderr}"})

        return Response(content=out_pdf.read_bytes(), media_type="application/pdf")
