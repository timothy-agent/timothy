"""HTTP wrapper around tesseract: reads the text off one image per
request. Internal-only sidecar — brain is the sole caller, same trust
boundary as searxng/markitdown. No auth: never expose this port
outside the compose network.

This is the free, local fallback KB ingest takes when no vision route
is bound (issue #558): recognized text, not a described image.
"""

import io

import pytesseract
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse
from PIL import Image

app = FastAPI()

# Guards against a decompression-bomb image exhausting memory; well
# above anything brain sends (embedded PDF images and 110-DPI page
# renders).
Image.MAX_IMAGE_PIXELS = 64_000_000

# Caps one uploaded image. Well above anything brain sends (embedded PDF
# images and 110-DPI page renders) while keeping a single request from
# buffering unbounded bytes; MAX_IMAGE_PIXELS above bounds the decode,
# this bounds the read that precedes it.
MAX_BODY_BYTES = 32 * 1024 * 1024


async def read_capped_body(request: Request) -> bytes:
    """Reads the request body, refusing anything over MAX_BODY_BYTES.

    Streams rather than awaiting request.body(): a declared
    Content-Length is rejected before a single chunk is read, and an
    undeclared (chunked) body stops at the first chunk that crosses the
    cap instead of buffering the whole upload. Unbounded reads here let
    one oversized image OOM the sidecar.
    """
    declared = request.headers.get("content-length")
    if declared is not None:
        try:
            if int(declared) > MAX_BODY_BYTES:
                raise HTTPException(status_code=413, detail="request body too large")
        except ValueError:
            raise HTTPException(status_code=400, detail="invalid content-length") from None

    chunks: list[bytes] = []
    total = 0
    async for chunk in request.stream():
        total += len(chunk)
        if total > MAX_BODY_BYTES:
            raise HTTPException(status_code=413, detail="request body too large")
        chunks.append(chunk)
    return b"".join(chunks)


@app.get("/healthz")
def healthz():
    return {"status": "ok"}


@app.post("/recognize")
async def recognize(request: Request):
    """Recognizes the text in the request body (raw image bytes).

    Pillow sniffs the real format rather than trusting the request's
    Content-Type, so every format it decodes (PNG, JPEG, WEBP, GIF —
    the set brain's captioning path allows) works without a per-type
    branch here.
    """
    body = await read_capped_body(request)
    if not body:
        raise HTTPException(status_code=400, detail="empty request body")

    try:
        with Image.open(io.BytesIO(body)) as img:
            text = pytesseract.image_to_string(img)
    except Exception as exc:  # Pillow/tesseract raise assorted decode errors
        raise HTTPException(status_code=422, detail=f"ocr failed: {exc}") from exc

    return JSONResponse({"text": text.strip()})
