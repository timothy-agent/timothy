# Timothy

[![CI](https://github.com/SumonMSelim/timothy/actions/workflows/ci.yml/badge.svg)](https://github.com/SumonMSelim/timothy/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](https://react.dev)
[![TypeScript](https://img.shields.io/badge/TypeScript-7.0-3178C6?logo=typescript&logoColor=white)](https://www.typescriptlang.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18%20%2B%20pgvector-4169E1?logo=postgresql&logoColor=white)](https://www.postgresql.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Self-hosted personal AI assistant: chat, cost tracking, tasks, and agents — running on your own hardware, talking to whichever LLM providers you configure.

**Status: early development.** The core works — chat, tools, memory, dashboard — but the agent harness and deployment story are still being built. Not ready for anyone else's use.

## What works today

- **Multi-provider chat** — Anthropic, Amazon Bedrock, and any OpenAI-compatible API behind one gateway; providers, models, and per-task routing are database configuration, editable at runtime from the settings panel with hot reload.
- **Sessions that survive** — every conversation is an append-only event log; kill a container mid-stream and the session resumes and replays exactly.
- **Tools, permissions, skills** — the agent loop executes tools behind a constraint/permission chain (destructive actions require explicit approval in the UI); skill packs load lazily by task.
- **Long-term memory** — staged fact extraction with a confirmation queue, hybrid pgvector retrieval (vector + text + entity, RRF-fused) under a strict token budget.
- **Cost accounting** — every request lands in a ledger with honest pricing (unknown price is recorded as null, never guessed); usage dashboard, spend budgets with alerts, Prometheus metrics on every service.

## Architecture

Go microservices behind a single public API, one PostgreSQL database, React web UI.

| Service   | Role                                                               |
|-----------|--------------------------------------------------------------------|
| `brain`   | Public API — chat orchestration, event-sourced sessions, streaming |
| `gateway` | Internal LLM gateway — multi-provider routing, cost ledger         |
| `memoryd` | Internal memory service — pgvector-backed recall                   |
| `web`     | React + Tailwind chat interface                                    |

Sessions are an append-only event log: every turn, tool run, and compaction is an immutable event, so conversations survive crashes mid-stream and replay exactly as they happened.

## Running

```sh
cp deploy/env.example deploy/.env   # fill in provider keys
make up
```

Web UI on `:3300`, API on `:8300`. For frontend work, `docker compose -f deploy/docker-compose.yml --profile dev up web-dev` serves a hot-reloading Vite dev server on `:3301`.

## Development

The Go toolchain runs containerized — no host Go install required.

```sh
make test               # unit tests
make test-integration   # against the running compose stack
make lint               # golangci-lint
```

Design decisions are documented as `D-0XX` markers in code comments next to the code they explain.

## License

[MIT](LICENSE)
