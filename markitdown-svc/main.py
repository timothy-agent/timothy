"""HTTP wrapper around markitdown: converts one file to markdown per
request. Internal-only sidecar — brain is the sole caller, same trust
boundary as searxng. No auth: never expose this port outside the
compose network.
"""

import io

from fastapi import FastAPI, Header, HTTPException, Request
from fastapi.responses import JSONResponse
from markitdown import MarkItDown, StreamInfo

app = FastAPI()
converter = MarkItDown(enable_plugins=False)


@app.get("/healthz")
def healthz():
    return {"status": "ok"}


@app.post("/convert")
async def convert(
    request: Request,
    x_filename: str | None = Header(default=None),
    x_mimetype: str | None = Header(default=None),
):
    """Converts the request body (raw file bytes) to markdown.

    The filename and MIME type ride request headers, not the body —
    the body is the file's bytes verbatim, nothing else to parse out.
    """
    body = await request.body()
    if not body:
        raise HTTPException(status_code=400, detail="empty request body")

    stream_info = StreamInfo(filename=x_filename, mimetype=x_mimetype)
    try:
        result = converter.convert_stream(io.BytesIO(body), stream_info=stream_info)
    except Exception as exc:  # markitdown raises assorted converter-specific errors
        raise HTTPException(status_code=422, detail=f"conversion failed: {exc}") from exc

    return JSONResponse({"markdown": result.markdown})
