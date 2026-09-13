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
    body = await request.body()
    if not body:
        raise HTTPException(status_code=400, detail="empty request body")

    try:
        with Image.open(io.BytesIO(body)) as img:
            text = pytesseract.image_to_string(img)
    except Exception as exc:  # Pillow/tesseract raise assorted decode errors
        raise HTTPException(status_code=422, detail=f"ocr failed: {exc}") from exc

    return JSONResponse({"text": text.strip()})
