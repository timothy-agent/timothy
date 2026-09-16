"""HTTP wrapper around faster-whisper: transcribes one audio clip per
request. Internal-only sidecar — brain is the sole caller, same trust
boundary as searxng/markitdown. No auth: never expose this port
outside the compose network.
"""

import io
import os

from faster_whisper import WhisperModel
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse

app = FastAPI()
model = WhisperModel(os.environ.get("WHISPER_MODEL", "small"), device="cpu", compute_type="int8")

# Caps one uploaded clip. Matches brain's own transcribe route limit
# (transcribeBodyLimit, internal/brain/api/transcribe.go) and the audio
# attachment cap: dictation for one turn, not bulk transcription.
MAX_BODY_BYTES = 25 * 1024 * 1024


async def read_capped_body(request: Request) -> bytes:
    """Reads the request body, refusing anything over MAX_BODY_BYTES.

    Streams rather than awaiting request.body(): a declared
    Content-Length is rejected before a single chunk is read, and an
    undeclared (chunked) body stops at the first chunk that crosses the
    cap instead of buffering the whole upload. Unbounded reads here let
    one oversized clip OOM the sidecar.
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


@app.get("/health")
def health():
    return {"status": "ok"}


@app.post("/transcribe")
async def transcribe(request: Request, language: str | None = None):
    """Transcribes the request body (raw audio bytes) to text.

    Accepts whatever container format the browser recorded (webm/opus
    from MediaRecorder, also wav/ogg/mp3) — faster-whisper decodes via
    PyAV, which bundles its own ffmpeg libs, so no separate ffmpeg
    binary is needed in the image.

    `language` is an optional ISO 639-1 code (e.g. "bn", "en"). Omitted
    or empty falls back to faster-whisper's own auto-detection, which
    can mis-guess on short clips or languages the model handles less
    reliably.
    """
    body = await read_capped_body(request)
    if not body:
        raise HTTPException(status_code=400, detail="empty request body")

    try:
        segments, _ = model.transcribe(io.BytesIO(body), language=language or None)
        text = "".join(segment.text for segment in segments).strip()
    except Exception as exc:  # faster-whisper/PyAV raise assorted decode errors
        raise HTTPException(status_code=422, detail=f"transcription failed: {exc}") from exc

    return JSONResponse({"text": text})
