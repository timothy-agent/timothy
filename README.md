# Timothy

[![CI](https://github.com/timothy-agent/timothy/actions/workflows/ci.yml/badge.svg)](https://github.com/timothy-agent/timothy/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/timothy-agent/timothy/graph/badge.svg?token=TTV3A14CFX)](https://codecov.io/gh/timothy-agent/timothy)
[![Release](https://img.shields.io/github/v/release/timothy-agent/timothy?include_prereleases&sort=semver&label=release)](https://github.com/timothy-agent/timothy/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](https://react.dev)
[![TypeScript](https://img.shields.io/badge/TypeScript-7.0-3178C6?logo=typescript&logoColor=white)](https://www.typescriptlang.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18%20%2B%20pgvector-4169E1?logo=postgresql&logoColor=white)](https://www.postgresql.org)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)

![Timothy](assets/timothy.png)

Self-hosted personal AI assistant: chat, cost tracking, tasks, and agents, running on your own hardware, talking to whichever LLM providers you configure.

**Status: early, under active development.** 

Alpha releases with prebuilt images are available on the [Releases page](https://github.com/timothy-agent/timothy/releases); expect rough edges and breaking changes between releases.

## What works today

- **Multi-provider chat**: Anthropic, Amazon Bedrock, and any OpenAI-compatible API behind one gateway; providers, models, and per-task routing are database configuration, editable at runtime from the settings panel with hot reload.
- **Sessions that survive**: every conversation is an append-only event log; kill a container mid-stream and the session resumes and replays exactly.
- **Tools, permissions, skills**: the agent loop executes tools behind a constraint/permission chain (destructive actions require explicit approval in the UI); skill packs load lazily by task.
- **Missions**: long-running agent tasks with a pure state machine, harness-owned verification (artifacts must exist before any model claim counts), per-mission sandboxes, budgets, and LLM review; recurring missions fire from cron schedules. Coding missions auto-detect their language environment (Go, Node, Python, Java, PHP) and run in a matching per-language sandbox image, pulled on demand.
- **Delegated coding harness**: a coding mission can hand its execution to headless Claude Code running inside the mission sandbox — against an Anthropic subscription, or any provider with an Anthropic-compatible endpoint (GLM, local Ollama) — while Timothy keeps verification, review, budgets, and the event timeline; it falls back to the native loop (recorded in the timeline) whenever the harness is unavailable.
- **Memory-aware scheduling**: before starting a mission, sandboxd checks whether the host can actually afford another sandbox; if not, the mission queues idle and is retried automatically as resources free up.
- **Long-term memory**: staged fact extraction with a confirmation queue, hybrid pgvector retrieval (vector + text + entity, RRF-fused) under a strict token budget.
- **Cost accounting**: every request lands in a ledger with honest pricing (unknown price is recorded as null, never guessed); usage dashboard, spend budgets with alerts, Prometheus metrics on every service.
- **Privacy floor**: tools whose output carries sensitive data (raw email) pin the rest of their turn, and every downstream side-call (memory extraction, compaction), to a dedicated route you can chain to a local model.

## Architecture

Go microservices behind a single public API, one PostgreSQL database, React web UI. All run via Docker Compose.

| Service      | Role                                                                                         |
|--------------|----------------------------------------------------------------------------------------------|
| `brain`      | Public API: chat orchestration, agent loop, missions, event-sourced sessions, SSE streaming |
| `gateway`    | Internal LLM gateway: multi-provider routing, cost ledger                                   |
| `memoryd`    | Internal memory service: pgvector-backed recall                                             |
| `sandboxd`   | Internal service holding the Docker socket: per-mission sandbox containers                  |
| `web`        | React + Tailwind interface: chat, missions, usage, settings                                 |
| `searxng`    | Internal metasearch backend for the web_search tool                                          |
| `markitdown` | Internal Python sidecar: file→markdown conversion                                           |
| `whisper`    | Internal Python sidecar: local speech-to-text for the web mic button (opt-in, off by default) |

Plus Postgres (18 + pgvector), internal only, no host port. Migrations are embedded in each Go binary and applied automatically at startup; there's no separate migrate command. Every Go service exposes `GET /health` and `GET /metrics`.

Sessions are an append-only event log: every turn, tool run, and compaction is an immutable event, so conversations survive crashes mid-stream and replay exactly as they happened.

**Published ports** (everything else is compose-internal):

| Port   | What                         |
|--------|------------------------------|
| `3300` | Web UI                       |
| `8300` | Brain (public API)           |
| `3301` | Vite dev server (`make dev`) |

## Quick start (prebuilt images)

The fastest way to run Timothy: no Go/Node toolchain, no build step, just Docker and the released images.

1. Make an empty directory and download the installer from the [latest release](https://github.com/timothy-agent/timothy/releases). While Timothy is alpha, every release is marked prerelease, so GitHub's `/releases/latest` redirect doesn't resolve; look up the newest tag instead:

   ```sh
   mkdir timothy && cd timothy
   TAG=$(curl -fsSL https://api.github.com/repos/timothy-agent/timothy/releases \
     | grep -E '"tag_name"|"published_at"' | paste - - \
     | sed -E 's/.*"tag_name": "([^"]+)".*"published_at": "([^"]+)".*/\2 \1/' \
     | sort -r | head -1 | awk '{print $2}')
   curl -fsSLo install.sh "https://github.com/timothy-agent/timothy/releases/download/$TAG/install.sh"
   ```

2. Read `install.sh` before running it. Then run it:

   ```sh
   sh install.sh
   ```

   It downloads `docker-compose.yml` and `env.example`, generates a `.env` with fresh secrets (`POSTGRES_PASSWORD`, `TIMOTHY_MASTER_KEY`, `TIMOTHY_API_TOKEN`), pulls the images, starts the stack, and prints a magic sign-in link once the web UI is up.

3. Open the printed link: the web UI signs in automatically from the token in the URL.

To upgrade later, in this order:

1. Download the new release's `docker-compose.yml` (it changes between releases; the old one may reference services or settings the new images don't expect):

   ```sh
   curl -fsSLo docker-compose.yml "https://github.com/timothy-agent/timothy/releases/download/<new-tag>/docker-compose.yml"
   ```

2. Set `TIMOTHY_VERSION` in `.env` to the new tag (without the `v` prefix).
3. `docker compose pull && docker compose up -d`

Or bump `TIMOTHY_VERSION` in `.env` and re-run the new release's `install.sh`, which handles the rest (it leaves an existing `.env` untouched, refreshes `docker-compose.yml` and the searxng config, and pre-pulls the mission sandbox image). Sandbox images (base and the per-language variants) are otherwise pulled on demand by sandboxd the first time a mission needs them.

The rest of this README covers building and running from source instead.

## Build from source

Prerequisites:

- Docker (Desktop, or engine + compose plugin).
- A [hugeicons.com](https://hugeicons.com) account with an active token. The web UI's icons are HugeIcons Pro (a paid icon set) and the token is required to build the `web` image; without it, `make up` fails on the web build step. Not needed for the prebuilt-image quick start above.

1. Copy the env file and fill in the required values:

   ```sh
   cp deploy/env.example deploy/.env
   ```

   Open `deploy/.env` and set:

   - `POSTGRES_PASSWORD`: compose refuses to start without it.
   - `TIMOTHY_MASTER_KEY`: generate with `openssl rand -base64 32`. This is the root of trust for the encrypted secret store (provider API keys, OAuth tokens all live behind it). Compose hard-fails if it's blank. **Back this up**: losing it makes every stored secret unrecoverable.
   - `TIMOTHY_API_TOKEN`: generate with `openssl rand -hex 32`. Bearer token for the API; if it's blank, every request 401s.
   - `HUGEICONS_TOKEN`: your HugeIcons Pro token, needed to build the `web` image.

2. (Optional) Missions sandbox. `deploy/env.example` prefills `MISSION_SANDBOX_IMAGE=timothy-sandbox:latest`, but that image doesn't exist until you build it:

   ```sh
   make sandbox-image
   ```

   This builds the base image plus the per-language variants (`timothy-sandbox-{go,node,python,java,php}:latest`) that coding missions run in; sandboxd derives a variant's image name from `MISSION_SANDBOX_IMAGE`, so the local names must stay in this convention. Skip this and leave `MISSION_SANDBOX_IMAGE` empty in `.env` if you don't need missions isolated in their own container; mission shell commands then run in-process instead.

3. (Linux only) Set `DOCKER_SOCK_GID` so `sandboxd` can use the Docker socket:

   ```sh
   stat -c '%g' /var/run/docker.sock
   ```

   Put that number in `.env`. On Docker Desktop the default of `0` works as-is. Note: mounting `docker.sock` gives `sandboxd` root-equivalent access to the host. It's isolated on its own compose network, read-only, and runs with all capabilities dropped, but the socket itself is the trust boundary, so only run this on a host you control.

4. Start the stack:

   ```sh
   make up
   ```

   Web UI: `http://localhost:3300`. API: `http://localhost:8300`.

5. First login. There's no login page: the web UI auto-opens a settings dialog asking for an API token the first time it can't find one. Paste the `TIMOTHY_API_TOKEN` value from `deploy/.env`. It's stored in your browser's `localStorage`.

6. Add a provider. A fresh install has zero LLM providers and no routing configured, so Timothy can't answer anything until you do this. Go to **Settings → Providers**, pick a preset tile (OpenAI, Anthropic, Bedrock, GLM, Grok, Ollama, or a custom OpenAI-compatible endpoint), fill in the form, and run the connection test before adding it. The API key you enter is encrypted into the secret store (default backend `db`, encrypted with `TIMOTHY_MASTER_KEY`); the database only ever holds a reference to it, never the raw value, and it never appears in `.env`, logs, or API responses. Creating your first provider automatically bootstraps the 4 routes Timothy needs to work (`default`, `summarize`, `embedding`, `vision`); routes are otherwise fully user-managed (create, edit chain/strategy, delete) from **Settings → Routing**.

## Operating the stack

```sh
make up      # start (builds images as needed)
make down    # stop
make logs    # follow logs for all services
```

Rebuild and restart a single service after a code change:

```sh
make brain      # or gateway, memoryd, web, markitdown, whisper, sandboxd
```

### Backups

Postgres has no host port, so back it up through the container:

```sh
docker compose -f deploy/docker-compose.yml exec postgres pg_dump -U timothy timothy > backup.sql
```

A full backup is the `pgdata` volume plus `deploy/.env`: without `TIMOTHY_MASTER_KEY`, the encrypted secrets in that dump are unrecoverable.

### Upgrading

```sh
git pull
make up
```

Migrations are additive-only, never edited once applied, and run automatically at service startup; no separate migrate step.

## Local development

The Go toolchain runs fully containerized; no host Go install required.

```sh
make build   # compile everything
make test    # unit tests
make vet     # go vet
make lint    # golangci-lint
```

Frontend development with hot reload:

```sh
make dev   # Vite dev server on :3301, proxies /v1 to brain
```

`make test-integration` and `make canary` need the compose stack up (`make up` first).

Design decisions are documented as `D-0XX` markers in code comments next to the code they explain.

## License

[AGPL-3.0](LICENSE). Versions up to and including the last MIT-licensed commit remain available under MIT.
