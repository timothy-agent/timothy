# syntax=docker/dockerfile:1

# PHP environment variant (tag timothy-sandbox-php): adds PHP-CLI and
# Composer to the base mission sandbox.
ARG SANDBOX_BASE=timothy-sandbox-base:latest
FROM ${SANDBOX_BASE}

USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
    php-cli php-mbstring php-xml composer \
    && rm -rf /var/lib/apt/lists/*
USER 65534:65534
