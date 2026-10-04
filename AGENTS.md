# AGENTS.md

Guide for coding agents working in this repository.

## What this is

Timothy: self-hosted personal AI assistant. Go microservices + one
PostgreSQL database + React web UI, run via Docker Compose
(`deploy/docker-compose.yml`).

Local models: native host Ollama at host.docker.internal:11434,
registered as an openaicompat provider.

| Service      | Role                                                                  |
|--------------|-----------------------------------------------------------------------|
| `brain`      | Public API (:8300 host, :8080 in-network): chat, agent loop, missions |
| `gateway`    | Internal LLM gateway: provider routing, cost ledger                   |
| `memoryd`    | Internal memory service: pgvector recall                              |
| `sandboxd`   | Holds the Docker socket; per-mission sandbox containers               |
| `web`        | React UI (:3300)                                                      |
| `searxng`    | Metasearch backend for search_web                                     |
| `markitdown` | Python sidecar: file → markdown                                       |
| `ocr`        | Python sidecar: tesseract image OCR (always on)                       |
| `whisper`    | Python sidecar: local speech-to-text (off unless `COMPOSE_PROFILES=whisper`) |
| `pdfgen`     | Python sidecar: markdown → PDF via Typst (mission export)             |

## Commands

The Go toolchain runs containerized (`golang:1.26.6`); no host Go.

```sh
make build test vet lint     # canonical pre-commit verify — run before every commit
make up / down / logs        # compose stack; needs deploy/.env
make test-integration        # integration-tagged tests; stack must be up
make canary                  # golden-mission e2e gate; REQUIRED after any harness change
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

First run: `cp deploy/env.example deploy/.env`, set `POSTGRES_PASSWORD`.
Never read `.env*` files (hooks block it); get values into containers
via the existing `--env-file` Make targets.

## Layout

A local where-to-look index with `file:line` anchors may exist at `docs/codebase-map.md` (gitignored). Check the commit it names before trusting a line number.

- `cmd/{brain,gateway,memoryd,sandboxd,skills-validate}`: binaries;
  all wiring in each `main.go` (nil-able deps, env-gated features).
- `internal/brain/`: `api` (HTTP handlers, nil-gated `register*`),
  `loop` (THE tool loop, lives here only), `tools` + `tools/builtin`,
  `chat`, `session`, `agents`, `missions` (agent harness), `events` (mission notification inbox),
  `automations`, `channels`,
  `workflows` (orchestration above missions, env-gated
  `WORKFLOWS_ENABLED`), `connectors`, `destinations`, `kb`,
  `attachments`, `gwclient`, `memclient`, `sandboxclient`,
  `settings`, `skills`.
- `internal/gateway/`: `provider` (wire adapters only), `router`,
  `catalog`, `ledger`, `stream`, `admin`, `api`.
- `internal/memory/`: `api`, `store` (pgvector), `chunk`, `extract`
  (source-aware fact extraction), `retrieval` (hybrid, RRF-fused).
- `internal/platform/`: shared (`migrate`, `pgpool`, `sse`,
  `httpserver`, `metrics`, `logging`, `config`, `service`, `netguard`,
  `markitdown`, `whisper`, `pdfgen`, `tesseract`, `trustfence`).
- `internal/secretstore/`: secret backends. Raw values stay here.
- `migrations/`: numbered idempotent SQL, embedded via `embed.go`.
  Pre-release: schema changes edit the original migration in place
  (currently one file, `0001_init.sql`); never add iterative ALTERs.
- `web/`: React 19 + react-router 8 + TypeScript 7 + Vite 8 + Tailwind v4 + shadcn/ui.

## Missions harness

The full contract (phases, light missions, sentinel end-turn rule,
verification and review gates, attachments, PDF export) lives in
`internal/brain/missions/AGENTS.md` and loads when working there. Web
work on missions should read it too.

- `internal/brain/missions/`: `statemachine.go` (pure `Step()`, sole
  transition logic), `store.go` (`ApplyTransition` is the only state
  writer; append-only `mission_events`), `driver.go`, `runner.go`,
  `policy.go` (per-kind/light behavior), `provision.go`, `budget.go`,
  `verifier.go`, `sentinel.go`, `packet.go`, `template.go` (the mission
  template an automation action carries; automations themselves live
  in `internal/brain/automations`).
- Light missions (kind=general, `light` flag): born in phase=build,
  skip discover/plan/prove; the worker carries the deliverable in
  mission_status's `final_output` argument.
- Worker turns end on successful sentinel execution
  (`loop.Request.EndTurnTools`); never add a post-sentinel model call.
- Harness-owned verification: `CheckArtifacts` runs BEFORE any
  model-authored `check_cmd`; `passes` flags flip only on harness
  evidence, never on model claims.
- Follow-up missions: terminal mission → new mission with
  `parent_mission_id`; parent outcome digest snapshotted into
  `parent_context` at create, rendered into prompts. Never reopen a
  terminal mission.
- Mission attachments: PDF/text via markitdown, images captioned,
  audio via whisper, converted once at create and stored on the
  mission's `sources` jsonb (cap 8); rendered neutralized into every
  prompt; API responses strip the markdown.
- Model-derived text entering prompts goes through `NeutralizeSlot`.
- `make canary` gates every harness change.

## Sandbox toolchain cache

D-125 (issue #990): the base sandbox image carries mise (shims on PATH,
`MISE_TRUSTED_CONFIG_PATHS=/workspace`). The optional `sandbox-toolchains`
named volume holds mise's data dir (`/home/sandbox/.mise`)
and is shared by all mission containers so per-repo toolchains install
once. sandboxd resolves it like the `.claude` state volume; absent means
ephemeral toolchains. The PHP variant does not use it.

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
- No speculative abstractions: no interface with one implementation,
  no config nothing reads.

## Git workflow

- Never commit to `main`. Branch off an updated `main`:
  `git checkout main && git pull && git checkout -b <type>/<short-description>`.
- Branch names: `<type>/<short-description>`, kebab-case, matching the
  commit type of the work (`feat/mission-follow-up`,
  `fix/ledger-budget-test-isolation`, `docs/git-workflow`).
- Commits are Conventional Commits:
  `<type>[optional scope]: <short description>` with types
  `feat|fix|docs|style|refactor|perf|test|chore|build|ci|revert`.
  Subject ≤72 chars, lowercase, imperative, no trailing period. Body
  explains WHY, not what. Breaking changes get a `BREAKING CHANGE:`
  footer. No vague subjects ("fix stuff", "WIP", "update").
- One logical change per commit; run
  `make build test vet lint` (and the web suite when `web/` changed)
  before committing. `make canary` before merging any harness change.
- PRs: title = the conventional commit subject; body = Why / What /
  Verification. Squash-merge to main; the squash subject keeps the
  conventional format (history reads `type(scope): subject (#PR)`).
  Delete the branch on merge. Stacked PRs name their base PR in the
  body and are retargeted after it merges.
- No AI/tool attribution anywhere: commits, PR bodies, comments.
- Never force-push shared branches; resolve conflicts by merging
  `main` into the branch (squash-merge discards branch history anyway).

## Conventions

- Go: wrap errors with `%w`; sentinel errors + `errors.Is`; interfaces
  at point of use, none for a single implementation; table-driven
  tests; integration tests behind `//go:build integration`; race
  detector always (`go test -race`); parameterized SQL only;
  `filepath.Join`/`Clean` and validate paths stay inside their root;
  no `panic` outside `main`.
- Composed shell/git commands need real-shell round-trip tests; fakes
  forgive quoting bugs.
- New builtin tool: constructor in `internal/brain/tools/builtin/`
  returning `*tools.Tool`, register in `buildAgent`
  (`cmd/brain/main.go`), permission-exempt only for pure reads,
  table-test `Execute`.
- Design decisions are `D-0XX` markers in code comments.
- Comments state what the code can't show — no reasoning essays.
