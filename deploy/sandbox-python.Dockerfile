# syntax=docker/dockerfile:1

# Python environment variant (tag timothy-sandbox-python): base already
# has python3 + pip; this variant adds venv support.
FROM timothy-sandbox-base:latest

USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3-venv \
    && rm -rf /var/lib/apt/lists/*
USER 65534:65534
