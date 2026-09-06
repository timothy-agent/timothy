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

- `internal/brain/missions/`: `statemachine.go` (pure `Step()`, sole
  transition logic), `store.go` (`ApplyTransition` is the only state
  writer; append-only `mission_events`), `driver.go`, `runner.go`
  (native runner over `loop.Agent`), `policy.go` (per-kind/light
  behavior flags), `provision.go`, `budget.go`, `verifier.go`,
  `worktree.go`, `packet.go`, `sentinel.go`, `review.go`, `verify.go`,
  `scheduler.go`, `notify.go`, `sweep.go`, `memory.go`. Schema:
  `migrations/0001_init.sql` (edited in place pre-release, never new
  ALTER migrations).
- Mission phases (D-086, issue #455): discover -> plan -> generate ->
  prove -> result -> done|failed. `parsePhase` (statemachine.go) still
  accepts the pre-rename names (explore/execute/review) at read time,
  mapping them to discover/generate/prove, so a new binary reads old
  rows safely before the data migration in `scripts/pending-alters.md`
  runs; historical `mission_events` payloads keep their old phase
  names forever, tolerated by the web timeline renderer. Result is
  deterministic harness code (zero LLM turns): destinations delivery
  (including github push/PR, issue #561), artifact copy, and KB
  promotion all run there now, not on the old done transition; a
  failure parks the mission IN result with a visible pause reason
  instead of being lost.
- Light missions (D-069, kind=general only): born in phase=generate,
  skip discover/plan/prove; the deliverable travels in mission_status's
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
  at create and rendered into discover/plan/work prompts. Worktree bases
  on the parent branch when reachable, else the repo default. Never
  reopen a terminal mission.
- Mission attachments (issue #359): PDF/text converted via markitdown,
  images captioned via the vision route (`chat.CaptionImageOverGateway`),
  audio transcribed via the whisper sidecar, all ONCE at create (prompt-
  cache stability), stored on the mission's `sources` jsonb column,
  rendered neutralized into every prompt labeled by mime (document/
  image/audio); capped at 8; API responses strip the markdown. Schedule
  templates carry the same attachments, resolved once at schedule
  create/patch time so a fire never re-converts.
- PDF export: POST /v1/missions/{id}/export-pdf renders workspace
  markdown (single file, or all files merged book-style) through
  `internal/brain/pdfgen`, which caches by content hash in
  `pdf_renders` and stores output via the attachment store; enabled
  only when PDFGEN_URL is set (derived read-only setting
  `pdf_export_enabled`).
- Mission display names: generated fire-and-forget at create
  (`chat.TitleOverGateway`), backfilled once in the result phase's step
  (`Driver.SetNameMission`) if still empty.
- Harness-owned verification: `CheckArtifacts` (declared artifact paths
  must exist, non-empty, inside the workspace) runs BEFORE any
  model-authored `verify_cmd`. `passes` flags flip only on harness
  evidence, never on model claims.
- Batch verification (D-094, issue #518): after every generate turn
  `verifier.verifyAll` checks every unit (unverified ones fully,
  already-passed ones as the regression subset) and the driver hands
  the outcomes to `Step` via `StepInput.Verified`; `applyVerification`
  records `harness_passed`, a 4 KB `verify_excerpt` and `regressed` on
  the plan units, persisted only by `ApplyTransition`. A failing or
  regressed unit costs a worker turn (`worker_retry`), never a review;
  `stepReviewApprove` flips `passes` on harness-passed units only; a
  generate phase with every unit harness-passed and no open finding
  skips the worker turn (`mission.generate_skipped`).
- Unit criteria and scoped review (D-095, issue #520): every plan unit
  carries `criteria` (2 to 6 lines, `parsePlan` rejects the plan with
  `plan_invalid` otherwise, one planner retry) and `scope` (paths,
  default the artifact directories). The reviewer packet carries the
  reviewed units' title, criteria, harness status and verify excerpt in
  place of the goal (legacy plans without criteria still get the goal),
  `git diff --stat` for the whole change and the diff restricted to the
  units' scope, cut on a file boundary at the byte budget, plus
  artifacts a criterion names (8 KB each). Evidence gate in
  `Driver.runReview`: a blocking finding must name a changed or declared
  file and quote `evidence`, else it is demoted to minor with a
  `mission.finding_demoted` event; a round left with only minor findings
  and no unresolved prior blocking one counts as approval. Light
  missions never plan, so none of this applies to them.
- One review round, findings-only re-review (D-096, issue #524): a
  generate turn that leaves units without harness evidence and no
  finding open stays in generate (`mission.generate_continued`); prove
  runs one full round once every unit is harness-passed (or a finding
  is open). A rework records the worktree HEAD in the plan jsonb as
  `last_review_commit` (written only by `ApplyTransition`); every later
  round with open findings is findings-only
  (`Driver.findingsReviewPacket`): the open findings, the diff since
  that commit scoped to the finding files and the affected units'
  scope, the finding files (8 KB each), the affected units' harness
  state, and a "Changed outside unit scope" list the harness opens no
  finding for. The reviewer answers with `resolved` ids plus gated new
  findings; all blocking ones resolved counts as approval. Missions
  without `last_review_commit` get the full packet.
- Workers get per-mission `shell` + `write_file` tools via turn-scoped
  `ExtraTools` that shadow base tools by name. Workers must create files
  with `write_file` only; shell redirects/heredocs classify destructive
  and park the turn.
- Non-coding units whose artifacts + verify_cmd pass harness checks skip
  LLM review entirely (`mission.review_skipped`).
- Delegated reviewer (issue #582): a mission's opt-in `review_harness`
  runs the prove round as a read-only CLI (`executor.InvocationSpec.
  ReadOnly`, enforced per adapter in Go) in the sandbox/worktree with
  the rendered review packet as prompt; native review is the floor,
  every failure records `review.delegated_fallback` and runs it.
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
