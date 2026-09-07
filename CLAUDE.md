# CLAUDE.md

Guidance for Claude Code in this repository: layout, commands,
invariants, and conventions.

## What this is

Timothy: self-hosted personal AI assistant. Go microservices + one
PostgreSQL database + React web UI, run via Docker Compose
(`deploy/docker-compose.yml`, which lists every service and its role).
Local models: native host Ollama at host.docker.internal:11434,
registered as an openaicompat provider.

## Commands

The Go toolchain runs containerized (`golang:1.26.6`); no host Go.

```sh
make build test vet lint     # canonical pre-commit verify
make up / down / logs        # compose stack; needs deploy/.env
make test-integration        # integration-tagged tests; stack must be up
make canary                  # golden-mission e2e regression gate; stack must be up
make dev                     # Vite hot reload on :3301
make brain                   # rebuild+restart one service (also: gateway, web, ...)
docker run --rm -v "$PWD/web":/app -w /app node:24.18.0-alpine \
  sh -c "npm run build && npm run lint && npm test"
```

Single Go test:

```sh
docker run --rm -v "$PWD":/src -w /src \
  -v timothy-go-mod:/go/pkg/mod -v timothy-go-cache:/root/.cache/go-build \
  -e GOFLAGS=-buildvcs=false golang:1.26.6 \
  go test -race -run TestName ./internal/brain/missions/
```

First run: `cp deploy/env.example deploy/.env` and set
`POSTGRES_PASSWORD` (never read `.env*` files; local hooks block it).

## Layout

- `cmd/{brain,gateway,memoryd,sandboxd,skills-validate}`: binaries; all
  wiring in each `main.go` (nil-able deps, env-gated features).
- `internal/brain/`: `api` (HTTP handlers, nil-gated `register*`
  pattern), `loop` (THE tool loop, lives here only), `tools` +
  `tools/builtin` (registry, permission chain, builtin tools), `chat`,
  `session`, `agents`, `missions` (agent harness), `workflows`
  (orchestration above missions: steps + outcome-driven edges, env-gated
  `WORKFLOWS_ENABLED`), `connectors`
  (google/microsoft/github/mcp/imap/caldav, unified capability tools:
  `search_mail`, `read_mail`, `send_mail`, `list_calendar_events`,
  `create_calendar_event` route to the right connector/account via an
  `account` parameter — see `manager.go`'s `aggregateTools`),
  `destinations` (mission result delivery: email/webhook/
  telegram/github, the last pushing/opening a PR through a github
  connector via `GitHubAdapter`), `kb` (knowledge-base collections/documents; image
  captioning at ingest, issues #349/#350, is default-off behind
  `settings.KeyKBImageCaptioning` — `kb.Enricher` runs in the ingest
  funnel, mission promotion, and PDF conversion), `attachments`,
  `gwclient`, `memclient`, `sandboxclient`, `settings`, `skills`.
- `internal/gateway/`: `provider` (wire adapters only), `router`,
  `catalog` (LiteLLM-synced model/pricing catalog), `ledger`, `stream`,
  `admin`, `api`.
- `internal/memory/` (memoryd's implementation): `api`, `store`
  (pgvector), `chunk`, `extract` (source-aware contracts, echo fence,
  duplicate reinforcement, `changes_behavior` utility gate), `retrieval`
  (hybrid vector+text+entity, RRF-fused).
- `internal/platform/`: shared: `migrate`, `pgpool`, `sse`,
  `httpserver`, `metrics`, `logging`, `config`, `service`, `netguard`
  (SSRF-guarded outbound dialer), `markitdown`, `whisper`, `pdfgen`
  (sidecar clients).
- `migrations/`: numbered idempotent SQL, embedded via `embed.go`;
  never edit an applied migration.
- `skills/`: skill packs baked into the brain image.
- `web/`: React 19 + TypeScript + Vite + Tailwind v4 + shadcn/ui.

## Missions harness

The harness contract (phases, light missions, sentinel end-turn rule,
verification and review gates, attachments, PDF export) lives in
`internal/brain/missions/CLAUDE.md` and loads when working there. Web
work on missions should read it too. `make canary` is the regression
gate for any harness change.

## Key invariants (enforce, never relax)

- Append-only stores stay append-only: `session_events`,
  `mission_events`, `memories` (supersede, never UPDATE/DELETE).
- Safety invariants (allowlists, ceilings, permission gates) are Go
  code, never moved into a prompt.
- Secrets by `credential_ref` name only; raw values never in DB, API,
  logs, or frontend. Never read `.env*`, `~/.ssh`, credentials.
- Providers are wire adapters; routing/model choice is data
  (`providers`/`routes` rows), not code.
- Cost honesty: unknown price recorded as NULL, never guessed.
- No speculative abstractions: no interface with one implementation, no
  config nothing reads.

## Conventions

- Conventional Commits, subject ≤72 chars, lowercase, body explains WHY.
  **No AI/tool attribution anywhere**: commits, PRs, comments.
- Branches `feat/<short-description>`; never commit to main.
- New builtin tool: constructor in `internal/brain/tools/builtin/`
  returning `*tools.Tool`, register in `buildAgent`
  (`cmd/brain/main.go`), decide permission exemption (pure reads only),
  table-test `Execute`.
- Design decisions are `D-0XX` markers in code comments.
