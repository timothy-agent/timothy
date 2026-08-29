# syntax=docker/dockerfile:1

FROM debian:bookworm-slim AS typst

ARG TARGETARCH
ARG TYPST_VERSION=0.15.1
# Per-arch release tarball checksums (release builds are multi-arch).
ARG TYPST_SHA256_AMD64=a6d077d0a95eed5a2eba715b2dae06be954f624ccbf85758a03f389ded33118c
ARG TYPST_SHA256_ARM64=5aa8d74a3d906e60ea12a66ac2f37f8eef1b14cbad7182a745e393a10c23dcee
ARG CMARKER_VERSION=0.1.10
ARG CMARKER_SHA256=7e5d7a0365aa1d32273bddb4511c62e47ab9d63093c905184ba04ca62064aafc
ARG MERMAN_VERSION=0.1.0
ARG MERMAN_SHA256=909b882d00c929d8b39d6b8230250dd95eac38f4872880d39b2b4f9fe736c5c5

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl xz-utils \
    && rm -rf /var/lib/apt/lists/*

# Pinned Typst CLI binary, checksum verified per target arch.
RUN case "${TARGETARCH}" in \
      arm64) triple=aarch64-unknown-linux-musl; sha="${TYPST_SHA256_ARM64}" ;; \
      *) triple=x86_64-unknown-linux-musl; sha="${TYPST_SHA256_AMD64}" ;; \
    esac \
    && curl -sSL -o /tmp/typst.tar.xz \
      "https://github.com/typst/typst/releases/download/v${TYPST_VERSION}/typst-${triple}.tar.xz" \
    && echo "${sha}  /tmp/typst.tar.xz" | sha256sum -c - \
    && tar -xf /tmp/typst.tar.xz -C /tmp \
    && mv "/tmp/typst-${triple}/typst" /usr/local/bin/typst \
    && chmod 755 /usr/local/bin/typst

# Vendor Typst packages into the cache layout so the runtime container
# compiles fully offline. TYPST_PACKAGE_CACHE_PATH is unreliable for a
# custom root in this typst build; the runtime image sets HOME instead
# and the cache lives at $HOME/.cache/typst/packages.
RUN mkdir -p /pkgcache/preview/cmarker/${CMARKER_VERSION} /pkgcache/preview/merman/${MERMAN_VERSION} \
    && curl -sSL -o /tmp/cmarker.tar.gz "https://packages.typst.org/preview/cmarker-${CMARKER_VERSION}.tar.gz" \
    && echo "${CMARKER_SHA256}  /tmp/cmarker.tar.gz" | sha256sum -c - \
    && tar -xzf /tmp/cmarker.tar.gz -C /pkgcache/preview/cmarker/${CMARKER_VERSION} \
    && curl -sSL -o /tmp/merman.tar.gz "https://packages.typst.org/preview/merman-${MERMAN_VERSION}.tar.gz" \
    && echo "${MERMAN_SHA256}  /tmp/merman.tar.gz" | sha256sum -c - \
    && tar -xzf /tmp/merman.tar.gz -C /pkgcache/preview/merman/${MERMAN_VERSION}

FROM python:3.12-slim

# DejaVu Sans Mono ships bundled with the typst binary (used for code
# fences); no separate font package needed.
COPY --from=typst /usr/local/bin/typst /usr/local/bin/typst
COPY --from=typst /pkgcache /home/nobody/.cache/typst/packages
ENV HOME=/home/nobody

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY main.py template.typ ./

RUN chown -R nobody:nogroup /home/nobody

USER nobody
EXPOSE 8000
ENTRYPOINT ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
