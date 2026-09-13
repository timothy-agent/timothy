# syntax=docker/dockerfile:1

FROM python:3.12-slim

WORKDIR /app

# pytesseract shells out to the tesseract CLI, so the apt package is the
# real dependency; the Python wheel is a thin wrapper needing no build
# toolchain. tesseract-ocr-eng carries the English traineddata the base
# package leaves out on slim.
RUN apt-get update \
    && apt-get install -y --no-install-recommends tesseract-ocr tesseract-ocr-eng \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY main.py .

USER nobody
EXPOSE 8000
ENTRYPOINT ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
