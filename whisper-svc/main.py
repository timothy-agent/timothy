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


@app.get("/health")
def health():
    return {"status": "ok"}


@app.post("/transcribe")
async def transcribe(request: Request):
    """Transcribes the request body (raw audio bytes) to text.

    Accepts whatever container format the browser recorded (webm/opus
    from MediaRecorder, also wav/ogg/mp3) — faster-whisper decodes via
    PyAV, which bundles its own ffmpeg libs, so no separate ffmpeg
    binary is needed in the image.
    """
    body = await request.body()
    if not body:
        raise HTTPException(status_code=400, detail="empty request body")

    try:
        segments, _ = model.transcribe(io.BytesIO(body))
        text = "".join(segment.text for segment in segments).strip()
    except Exception as exc:  # faster-whisper/PyAV raise assorted decode errors
        raise HTTPException(status_code=422, detail=f"transcription failed: {exc}") from exc

    return JSONResponse({"text": text})
