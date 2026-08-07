# syntax=docker/dockerfile:1

# Mission sandbox: the container model-authored shell commands (mission
# worker/reviewer shell calls, verify_cmd) execute inside, instead of
# brain's own process — see internal/brain/sandbox. This is a warm
# exec target (created once per mission, `sleep infinity` as PID 1
# under tini via --init at runtime, reused across a mission's turns),
# not a service — no ENTRYPOINT beyond that.
FROM node:24.18.0-slim AS node-dist
FROM golang:1.26.5 AS go-dist

FROM debian:stable-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-pip \
    git bash curl jq make ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Node from the pinned official image, not debian's nodejs 20 package:
# @anthropic-ai/claude-code requires node >=22. Copying /usr/local
# brings node, npm, and corepack onto PATH with no pipe-to-shell setup.
COPY --from=node-dist /usr/local/bin /usr/local/bin
COPY --from=node-dist /usr/local/lib/node_modules /usr/local/lib/node_modules

# Headless claude CLI for delegated coding executors, detached inside
# this container. Installed while still root, to the root-owned global
# prefix (/usr/local) rather than the runtime NPM_CONFIG_PREFIX below —
# that prefix is per-user (/home/sandbox) and not writable at this
# build stage — so it lands on PATH for uid 65534 too.
RUN NPM_CONFIG_PREFIX=/usr/local npm install -g @anthropic-ai/claude-code@2.1.223

# Go toolchain from the same pinned image the repo's own containerized
# builds use — missions writing Go need `go build`/`go test` for their
# verify_cmd. GOPATH lands in the writable home below.
COPY --from=go-dist /usr/local/go /usr/local/go
ENV GOPATH=/home/sandbox/go
ENV PATH="/usr/local/go/bin:/home/sandbox/go/bin:${PATH}"

# Same numeric uid/gid as brain's alpine "nobody" (65534) — both sides
# write the shared workspace volume as the same owner. Debian's built-in
# nobody has HOME=/nonexistent, which breaks pip/npm; give it a real,
# writable home instead.
# .claude is pre-created and owned by the sandbox uid so the
# executor-claude-state named volume inherits that ownership on first
# use — an empty named volume otherwise mounts root-owned and the CLI
# cannot write its own state (D-054).
RUN mkdir -p /home/sandbox/.claude && chown -R 65534:65534 /home/sandbox
ENV HOME=/home/sandbox
# Debian's system python3 is PEP 668 externally-managed; without this,
# `pip install` (even --user) refuses to run for a model-authored command
# that has no way to pass extra pip flags on its own.
ENV PIP_BREAK_SYSTEM_PACKAGES=1
ENV NPM_CONFIG_PREFIX=/home/sandbox/.npm-global
ENV PATH="/home/sandbox/.local/bin:/home/sandbox/.npm-global/bin:${PATH}"

USER 65534:65534
WORKDIR /workspace
