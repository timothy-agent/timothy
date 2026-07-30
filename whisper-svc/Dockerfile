# syntax=docker/dockerfile:1

FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# WHISPER_MODEL must match the value the service runs with — this is
# what makes the download below actually warm the model the container
# loads at boot. Pre-download at build time (not first-request) so the
# running container never reaches the network: fully offline,
# deterministic startup, no surprise latency on the first transcript.
ARG WHISPER_MODEL=small
RUN python -c "from faster_whisper import WhisperModel; WhisperModel('${WHISPER_MODEL}', device='cpu', compute_type='int8')"

COPY main.py .

USER nobody
EXPOSE 8001
ENTRYPOINT ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8001"]
