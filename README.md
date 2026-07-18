# Timothy

[![CI](https://github.com/SumonMSelim/timothy/actions/workflows/ci.yml/badge.svg)](https://github.com/SumonMSelim/timothy/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](https://react.dev)
[![TypeScript](https://img.shields.io/badge/TypeScript-7.0-3178C6?logo=typescript&logoColor=white)](https://www.typescriptlang.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18%20%2B%20pgvector-4169E1?logo=postgresql&logoColor=white)](https://www.postgresql.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Self-hosted personal AI assistant: chat, cost tracking, tasks, and agents — running on your own hardware, talking to whichever LLM providers you configure.

**Status: early development.** Not ready for use.

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

Web UI on `:3300`, API on `:8300`.

## Development

```sh
make test    # unit tests (containerized)
make lint    # golangci-lint
```

Design decisions are documented as `D-0XX` markers in code comments next to the code they explain.

## License

[MIT](LICENSE)
