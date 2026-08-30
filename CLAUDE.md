# CLAUDE.md

Guidance for Claude Code in this repository: layout, commands,
invariants, and conventions.

## What this is

Timothy: self-hosted personal AI assistant. Go microservices + one
PostgreSQL database + React web UI, run via Docker Compose
(`deploy/docker-compose.yml`).

| Service        | Role                                                                                                           |
|----------------|----------------------------------------------------------------------------------------------------------------|
| `brain`        | Public API (:8300): chat, agent loop, missions, sessions                                                       |
| `gateway`      | Internal LLM gateway: provider routing, cost ledger                                                            |
| `memoryd`      | Internal memory service: pgvector recall                                                                       |
| `sandboxd`     | Internal service holding the Docker socket: per-mission sandbox containers, brain calls it via `sandboxclient` |
| `web`          | React UI (:3300)                                                                                               |
| `searxng`      | Internal metasearch backend for the search_web tool                                                            |
| `markitdown`   | Internal Python sidecar (`markitdown-svc/`): file→markdown                                                     |
| `whisper`      | Internal Python sidecar (`whisper-svc/`): local speech-to-text                                                 |
| `pdfgen`       | Internal Python sidecar (`pdfgen-svc/`): markdown→PDF via Typst                                                |
| (local models) | Native host Ollama at host.docker.internal:11434, registered as openaicompat provider                          |

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
  telegram), `kb` (knowledge-base collections/documents), `attachments`,
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

- `internal/brain/missions/`: `statemachine.go` (pure `Step()`, sole
  transition logic), `store.go` (`ApplyTransition` is the only state
  writer; append-only `mission_events`), `driver.go`, `runner.go`
  (native runner over `loop.Agent`), `policy.go` (per-kind/light
  behavior flags), `provision.go`, `budget.go`, `verifier.go`,
  `worktree.go`, `packet.go`, `sentinel.go`, `review.go`, `verify.go`,
  `scheduler.go`, `notify.go`, `sweep.go`, `memory.go`. Schema: `migrations/0010_missions.sql` (edited in place
  pre-release, never new ALTER migrations), attachments in 0011.
- Light missions (D-069, kind=general only): born in phase=execute,
  skip explore/plan/review; the deliverable travels in mission_status's
  `final_output` argument (reasoning models emit tool calls with no
  plain text). Digest schedules run light.
- Worker turns END on successful sentinel execution
  (`loop.Request.EndTurnTools`, D-075): never reintroduce a
  post-sentinel model call — chat models ramble through it, reasoning
  models return empty and fail the turn.
- Mission workers get their agent's skill index in the system prompt
  (packet `SkillsIndex`, resolved at packet build); delegated CLI
  packets never include it. Every mission prompt carries the current
  date via `execEnvironmentNote` — models otherwise anchor on stale
  dates in tool descriptions.
- Follow-up missions: a terminal mission spawns a new one via
  `parent_mission_id`; the parent's outcome digest (`OutcomeDigest`,
  shared with memory extraction) is snapshotted into `parent_context`
  at create and rendered into explore/plan/work prompts. Worktree bases
  on the parent branch when reachable, else the repo default. Never
  reopen a terminal mission.
- PDF attachments: converted via markitdown ONCE at create (prompt-
  cache stability), markdown stored in the `attachments` jsonb column,
  rendered neutralized into every prompt; capped at 8; API responses
  strip the markdown. Images/audio unsupported.
- PDF export: POST /v1/missions/{id}/export-pdf renders workspace
  markdown (single file, or all files merged book-style) through
  `internal/brain/pdfgen`, which caches by content hash in
  `pdf_renders` and stores output via the attachment store; enabled
  only when PDFGEN_URL is set (derived read-only setting
  `pdf_export_enabled`).
- Mission display names: generated fire-and-forget at create
  (`chat.TitleOverGateway`), backfilled once at terminal transition
  (`Driver.SetNameMission`) if still empty.
- Harness-owned verification: `CheckArtifacts` (declared artifact paths
  must exist, non-empty, inside the workspace) runs BEFORE any
  model-authored `verify_cmd`. `passes` flags flip only on harness
  evidence, never on model claims.
- Workers get per-mission `shell` + `write_file` tools via turn-scoped
  `ExtraTools` that shadow base tools by name. Workers must create files
  with `write_file` only; shell redirects/heredocs classify destructive
  and park the turn.
- Non-coding units whose artifacts + verify_cmd pass harness checks skip
  LLM review entirely (`mission.review_skipped`).
- `make canary` is the regression gate for any harness change.

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
