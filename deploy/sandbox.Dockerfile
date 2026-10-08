# syntax=docker/dockerfile:1

# Mission sandbox base: the container model-authored shell commands
# (mission worker/reviewer shell calls, check_cmd) execute inside,
# instead of brain's own process — see internal/brain/sandbox. This is
# a warm exec target (created once per mission, `sleep infinity` as
# PID 1 under tini via --init at runtime, reused across a mission's
# turns), not a service — no ENTRYPOINT beyond that.
#
# D-141 (issue #1015): one image (tag timothy-sandbox) for every
# mission. It carries node (the executor CLIs need it), python3,
# build tools and common -dev libraries, PHP 8.1 to 8.4 with composer,
# and mise. node, python, go, java, ruby and rust versions a repo pins
# install through mise onto the shared toolchains volume (D-125); PHP
# stays baked because mise only builds it from source (D-127).
FROM node:24.18.0-slim AS node-dist

FROM composer:2.8.12 AS composer-dist

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

# Headless OpenAI Codex CLI, same rationale as claude/pi above.
# Fixtures stay testdata/codex-0.147.0: re-recording 0.157.1 hung on
# its own background sync to api.openai.com (issue #951).
RUN NPM_CONFIG_PREFIX=/usr/local npm install -g @openai/codex@0.157.1 \
    && codex --version

# Headless opencode CLI, same rationale as claude/pi/codex above. Pin
# matches internal/brain/missions/executor/testdata/opencode-1.18.32 -
# bump both together.
RUN NPM_CONFIG_PREFIX=/usr/local npm install -g opencode-ai@1.18.32 \
    && opencode --version

# Executor CLIs must keep running on this image's node 24 when a mission
# pins another node through mise (D-126, issue #991): shims precede
# /usr/local/bin on PATH, so `#!/usr/bin/env node` would pick the pinned
# version. claude and opencode are native binaries and are left alone.
RUN for c in claude codex pi opencode; do \
      f="$(readlink -f "/usr/local/bin/$c")"; \
      if [ "$(head -n 1 "$f")" = '#!/usr/bin/env node' ]; then \
        sed -i '1s|.*|#!/usr/local/bin/node|' "$f"; \
      fi; \
    done

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
# D-120 (issue #947): cursor-agent self-updates on start and the
# download exceeds the fsize ulimit (D-106), so tar dies with SIGXFSZ
# and dumps core. The adapter passes the hidden --disable-auto-update
# flag; never raise fsize instead.
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

# mise (issue #990): per-repo toolchain versions (python, node, ...) read
# from the repo's own version files. Same pin-and-checksum approach as
# cursor-agent above, no pipe-to-shell. The tarball extracts to
# mise/bin/mise.
ARG MISE_VERSION=2026.10.1
ARG MISE_SHA256_AMD64=9b92aa39b8fde54b28c8f974a68f2501925a1523d6c05a52719145df3acdd75a
ARG MISE_SHA256_ARM64=15b7e978812d1657e615f42f366c4101f9d8733b96a85b12779a9f3f3e2d8596
RUN case "${TARGETARCH}" in \
      arm64) arch=arm64; sha="${MISE_SHA256_ARM64}" ;; \
      *) arch=x64; sha="${MISE_SHA256_AMD64}" ;; \
    esac \
    && curl -sSL -o /tmp/mise.tar.gz \
      "https://github.com/jdx/mise/releases/download/v${MISE_VERSION}/mise-v${MISE_VERSION}-linux-${arch}.tar.gz" \
    && echo "${sha}  /tmp/mise.tar.gz" | sha256sum -c - \
    && tar -xzf /tmp/mise.tar.gz -C /tmp \
    && install -m 0755 /tmp/mise/bin/mise /usr/local/bin/mise \
    && rm -rf /tmp/mise /tmp/mise.tar.gz \
    && mise --version

# Build tools and the -dev libraries native extensions and gems link
# against (pg, sqlite3, openssl, zlib, nokogiri, psych, ffi, mbstring,
# zip), so a dependency install compiles without root. python3-venv for
# `python3 -m venv` on the image python.
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential pkg-config python3-venv \
    libpq-dev libsqlite3-dev libssl-dev zlib1g-dev libxml2-dev \
    libyaml-dev libffi-dev libonig-dev libzip-dev \
    && rm -rf /var/lib/apt/lists/*

# PHP 8.1 to 8.4 from the Sury apt repo, which tracks PHP independently
# of the Debian release; 8.4 is the default and a mission selects
# another minor by linking it into ~/.local/bin (D-127; phpMinors in
# internal/brain/missions/toolchain.go mirrors this list). The key is
# downloaded to a file, then installed as a keyring.
RUN . /etc/os-release \
    && curl -fsSL -o /tmp/sury.gpg https://packages.sury.org/php/apt.gpg \
    && install -m 0644 /tmp/sury.gpg /usr/share/keyrings/sury-php.gpg \
    && rm /tmp/sury.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/sury-php.gpg] https://packages.sury.org/php/ ${VERSION_CODENAME} main" \
        > /etc/apt/sources.list.d/sury-php.list \
    && apt-get update \
    && pkgs="" && for v in 8.1 8.2 8.3 8.4; do \
        for e in cli mbstring xml sqlite3 curl zip intl bcmath mysql pgsql gd redis; do pkgs="$pkgs php$v-$e"; done; \
    done \
    && apt-get install -y --no-install-recommends $pkgs \
    && update-alternatives --set php /usr/bin/php8.4 \
    && update-alternatives --set phar /usr/bin/phar8.4 \
    && update-alternatives --set phar.phar /usr/bin/phar.phar8.4 \
    && rm -rf /var/lib/apt/lists/*

# Composer phar from the official image.
COPY --from=composer-dist /usr/bin/composer /usr/local/bin/composer

# Same numeric uid/gid as brain's alpine "nobody" (65534) — both sides
# write the shared workspace volume as the same owner. Debian's built-in
# nobody has HOME=/nonexistent, which breaks pip/npm; give it a real,
# writable home instead.
# .claude is pre-created and owned by the sandbox uid so the
# executor-claude-state named volume inherits that ownership on first
# use — an empty named volume otherwise mounts root-owned and the CLI
# cannot write its own state (D-054). The mise data and cache dirs get
# the same treatment for the sandbox-toolchains volume (D-125).
RUN mkdir -p /home/sandbox/.claude /home/sandbox/.mise /home/sandbox/.cache/mise \
    && chown -R 65534:65534 /home/sandbox
ENV HOME=/home/sandbox
# Debian's system python3 is PEP 668 externally-managed; without this,
# `pip install` (even --user) refuses to run for a model-authored command
# that has no way to pass extra pip flags on its own.
ENV PIP_BREAK_SYSTEM_PACKAGES=1
ENV NPM_CONFIG_PREFIX=/home/sandbox/.npm-global
# mise: shims, not `mise activate`, because commands run via `sh -c`.
# Only /workspace configs are trusted, so a cloned repo outside it can
# not run mise tasks or hooks unprompted.
ENV MISE_DATA_DIR=/home/sandbox/.mise
ENV MISE_CACHE_DIR=/home/sandbox/.cache/mise
# mise deps freshness hashes live in the state dir; keep them on disk
# with the caches so a sandbox recreate does not rerun every install.
ENV MISE_STATE_DIR=/home/sandbox/.cache/mise-state
ENV MISE_TRUSTED_CONFIG_PATHS=/workspace
ENV MISE_YES=1
# D-139 (issue #1014): mise reads the repo's .nvmrc, .node-version,
# .python-version, .go-version, .ruby-version, .java-version, .sdkmanrc
# and rust-toolchain.toml itself; idiomatic files are off by default.
ENV MISE_IDIOMATIC_VERSION_FILE_ENABLE_TOOLS=node,python,go,ruby,java,rust
# D-141: ruby installs from mise's precompiled builds, never compiled.
# rustup and cargo homes sit on the toolchains volume with mise's own
# installs, not on the HOME tmpfs.
ENV MISE_RUBY_COMPILE=false \
    MISE_RUSTUP_HOME=/home/sandbox/.mise/rustup \
    MISE_CARGO_HOME=/home/sandbox/.mise/cargo \
    COMPOSER_HOME=/home/sandbox/.composer
# Non-interactive, wide-output exec environment (issue #1009): commands
# run without a TTY, so runners must not wrap at 80 columns, prompt, watch
# or colour. Image ENV merges with sandboxd's create-time PATH and HOME.
ENV COLUMNS=200 \
    TERM=dumb \
    CI=1 \
    NO_COLOR=1 \
    FORCE_COLOR=0 \
    LANG=C.UTF-8 \
    GIT_TERMINAL_PROMPT=0 \
    COMPOSER_NO_INTERACTION=1 \
    PIP_NO_INPUT=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PYTHONUNBUFFERED=1 \
    NPM_CONFIG_FUND=false \
    NPM_CONFIG_UPDATE_NOTIFIER=false \
    MISE_AUTO_INSTALL=false \
    MISE_EXEC_AUTO_INSTALL=false \
    MISE_NOT_FOUND_AUTO_INSTALL=false
# Package caches (D-131): sandboxd mounts ~/.cache from the shared
# sandbox-caches volume, or from the mission's workspace when that
# volume is absent, so caches stay on disk and off the HOME tmpfs.
# -modcacherw keeps the Go module cache deletable by workspace teardown.
ENV XDG_CACHE_HOME=/home/sandbox/.cache \
    COMPOSER_CACHE_DIR=/home/sandbox/.cache/composer \
    npm_config_cache=/home/sandbox/.cache/npm \
    PIP_CACHE_DIR=/home/sandbox/.cache/pip \
    UV_CACHE_DIR=/home/sandbox/.cache/uv \
    GOMODCACHE=/home/sandbox/.cache/go-mod \
    GOCACHE=/home/sandbox/.cache/go-build \
    GOFLAGS=-modcacherw \
    MAVEN_OPTS=-Dmaven.repo.local=/home/sandbox/.cache/m2/repository \
    GRADLE_USER_HOME=/home/sandbox/.cache/gradle \
    YARN_CACHE_FOLDER=/home/sandbox/.cache/yarn \
    BUN_INSTALL_CACHE_DIR=/home/sandbox/.cache/bun
ENV PATH="/home/sandbox/.mise/shims:/home/sandbox/.local/bin:/home/sandbox/.npm-global/bin:${PATH}"

USER 65534:65534
WORKDIR /workspace
