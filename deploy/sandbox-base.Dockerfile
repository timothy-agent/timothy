# syntax=docker/dockerfile:1

# Mission sandbox base: the container model-authored shell commands
# (mission worker/reviewer shell calls, check_cmd) execute inside,
# instead of brain's own process — see internal/brain/sandbox. This is
# a warm exec target (created once per mission, `sleep infinity` as
# PID 1 under tini via --init at runtime, reused across a mission's
# turns), not a service — no ENTRYPOINT beyond that.
#
# This base image (tag timothy-sandbox-base) carries the tools every
# mission needs regardless of language: node (for the headless claude
# CLI executor) and general POSIX tooling. Per-language toolchains
# (go, java, php, ...) live in sandbox-<lang>.Dockerfile variants
# FROM this image — see the "environment" axis (D-05x, sandboxd).
FROM node:24.18.0-slim AS node-dist

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
RUN NPM_CONFIG_PREFIX=/usr/local npm install -g @anthropic-ai/claude-code@2.1.274 \
    && claude --version

# Headless pi coding agent, same rationale as claude above. Pin matches
# internal/brain/missions/executor/testdata/pi-0.87.1 - bump both
# together. node 24 here already satisfies pi's engines >=22.19.
RUN NPM_CONFIG_PREFIX=/usr/local npm install -g --ignore-scripts @earendil-works/pi-coding-agent@0.87.1 \
    && pi --version

# Headless OpenAI Codex CLI, same rationale as claude/pi above. Fixture
# dir stays testdata/codex-0.147.0 on this bump - re-recording needs a
# live run against local Ollama (issue #951) and that run hung against
# this pin (codex 0.157.1's own background websocket sync retrying
# against api.openai.com, unrelated to the custom provider under test);
# parser tests still pass against the kept 0.147.0 fixtures, no known
# wire-format drift.
RUN NPM_CONFIG_PREFIX=/usr/local npm install -g @openai/codex@0.157.1 \
    && codex --version

# Headless opencode CLI, same rationale as claude/pi/codex above. Pin
# matches internal/brain/missions/executor/testdata/opencode-1.18.32 -
# bump both together.
RUN NPM_CONFIG_PREFIX=/usr/local npm install -g opencode-ai@1.18.32 \
    && opencode --version

# Headless Cursor CLI, same rationale as claude/pi/codex/opencode
# above. No npm package exists; the official installer
# (https://cursor.com/install) downloads this same per-arch tarball
# from a version-keyed URL, so pin that URL and checksum it here
# instead of piping the installer to bash. The 2026.08.11 build the
# fixtures in internal/brain/missions/executor/testdata/cursor-2026.08.11
# were recorded against is not fetchable (the URL needs a commit suffix
# the fixtures never captured), so this pins the current stable. To
# bump: read the version off the installer script, refresh both
# checksums, re-record fixtures if the wire format moved.
#
# D-120 (issue #947): cursor-agent self-updates on start, downloading
# a per-arch package (129-146 MB of node/cursor-agent-sea/cursor-agent-
# worker-sea) into ~/.local/share/cursor-agent/versions/ and extracting
# it; the sandbox fsize ulimit is 100 MB (D-106), so tar dies with
# SIGXFSZ on every mission and dumps core into the worktree. The
# minified bundle's own commander setup (`index.js`, hidden global
# option) shows an undocumented `--disable-auto-update` flag that
# short-circuits the exact same gate the CLI already uses to skip
# updates on a "static" channel (`o.disableAutoUpdate||"static"===
# channel||setTimeout(updateCursorAgent,...)`), before the background
# update check is even scheduled - never raise fsize to work around
# this. The cursor adapter (executor/cursor.go) always passes this
# flag; verified by running cursor-agent in a container with the flag
# and confirming no versions/ directory appears under HOME.
ARG TARGETARCH
ARG CURSOR_VERSION=2026.09.26-dd393fe
# Per-arch tarball checksums (release builds are multi-arch).
ARG CURSOR_SHA256_AMD64=8085fd120f5c71f4eae7fea26a043718e5644e3071e4fab3220a0e58c51f9593
ARG CURSOR_SHA256_ARM64=ab1178d0d8c10b254e7e427d1d673533a389338424e75034be9ab9da02845bde
RUN case "${TARGETARCH}" in \
      arm64) arch=arm64; sha="${CURSOR_SHA256_ARM64}" ;; \
      *) arch=x64; sha="${CURSOR_SHA256_AMD64}" ;; \
    esac \
    && curl -sSL -o /tmp/cursor-agent.tar.gz \
      "https://downloads.cursor.com/lab/${CURSOR_VERSION}/linux/${arch}/agent-cli-package.tar.gz" \
    && echo "${sha}  /tmp/cursor-agent.tar.gz" | sha256sum -c - \
    && mkdir -p /opt/cursor \
    && tar --strip-components=1 -xzf /tmp/cursor-agent.tar.gz -C /opt/cursor \
    && rm /tmp/cursor-agent.tar.gz \
    && ln -s /opt/cursor/cursor-agent /usr/local/bin/cursor-agent \
    && chmod -R a+rX /opt/cursor \
    && cursor-agent --version

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
