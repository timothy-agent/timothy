# syntax=docker/dockerfile:1

FROM python:3.12-slim

WORKDIR /app

# CPU torch from PyTorch's own index: the PyPI wheel drags CUDA
# libraries into an image that only ever runs on CPU.
COPY requirements.txt .
RUN pip install --no-cache-dir torch==2.14.0 --index-url https://download.pytorch.org/whl/cpu \
    && pip install --no-cache-dir -r requirements.txt

# Bake the typed-decisions checkpoint at build time (not first
# request) so the running container never reaches the network: fully
# offline, deterministic startup. The first load also applies laya's
# tokenizer fix to the snapshot, so it happens here as root and the
# runtime user only reads. HF_HOME lives outside /root so the cache
# stays readable once the service runs as nobody.
ENV HF_HOME=/models
RUN python -c "import laya; laya.load('convaiinnovations/laya', subfolder='typed-decisions')" \
    && chmod -R a+rX /models

# One checkpoint, preloaded before the port binds, so /health means
# ready. The gateway's laya driver names the checkpoint per chain
# entry; LAYA_DEFAULT_MODEL covers a request that omits it.
ENV HF_HUB_OFFLINE=1 \
    LAYA_MODELS=typed-decisions \
    LAYA_DEFAULT_MODEL=typed-decisions \
    LAYA_PRELOAD=1 \
    LAYA_DEVICE=cpu \
    USE_TF=0 \
    TOKENIZERS_PARALLELISM=false \
    HOME=/tmp

USER nobody
EXPOSE 8000
ENTRYPOINT ["laya-serve"]
