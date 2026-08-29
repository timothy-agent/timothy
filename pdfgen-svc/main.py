"""HTTP wrapper around the Typst CLI: turns markdown documents into one
typeset PDF per request. Internal-only sidecar, same trust boundary as
markitdown/searxng. No auth: never expose this port outside the
compose network.
"""

import subprocess
import tempfile
from pathlib import Path

from fastapi import FastAPI
from fastapi.responses import JSONResponse, Response
from pydantic import BaseModel, Field

app = FastAPI()

TEMPLATE_PATH = Path(__file__).parent / "template.typ"
STDERR_LIMIT = 2000


class Document(BaseModel):
    title: str
    content: str


class Options(BaseModel):
    cover_title: str = ""
    toc: bool = False


class RenderRequest(BaseModel):
    documents: list[Document] = Field(default_factory=list)
    options: Options = Field(default_factory=Options)


@app.get("/healthz")
def healthz():
    return {"status": "ok"}


def _typ_string(s: str) -> str:
    """Escapes a Python string into a Typst string literal."""
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


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
        result = subprocess.run(
            ["typst", "compile", "main.typ", str(out_pdf)],
            cwd=workdir,
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            stderr = result.stderr[-STDERR_LIMIT:]
            return JSONResponse(status_code=500, content={"error": f"typst compile failed: {stderr}"})

        return Response(content=out_pdf.read_bytes(), media_type="application/pdf")
