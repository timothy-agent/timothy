# Missions harness

Loaded when working under `internal/brain/missions/`. Moved out of the root
CLAUDE.md so other work does not pay for it every session.

- `internal/brain/missions/`: `statemachine.go` (pure `Step()`, sole
  transition logic), `store.go` (`ApplyTransition` is the only state
  writer; append-only `mission_events`), `driver.go`, `runner.go`
  (native runner over `loop.Agent`), `policy.go` (per-kind/light
  behavior flags), `provision.go`, `budget.go`, `verifier.go`,
  `worktree.go`, `packet.go`, `sentinel.go`, `review.go`, `verify.go`,
  `scheduler.go`, `notify.go`, `sweep.go`, `memory.go`. Schema:
  `migrations/0001_init.sql` (edited in place pre-release, never new
  ALTER migrations).
- Mission phases (D-086 issue #455, renamed again by issue #611):
  discover -> plan -> build -> prove -> result -> done|failed.
  `parsePhase` (statemachine.go) still accepts the pre-rename names
  (explore/execute/review, and generate) at read time, mapping them to
  discover/build/prove, so a new binary reads old rows safely before
  the data migration in `scripts/pending-alters.md` runs; historical `mission_events` payloads keep their old phase
  names forever, tolerated by the web timeline renderer. Result is
  deterministic harness code (zero LLM turns): destinations delivery
  (including github push/PR, issue #561), artifact copy, and KB
  promotion all run there now, not on the old done transition; a
  failure parks the mission IN result with a visible pause reason
  instead of being lost.
- Light missions (D-069, kind=general only): born in phase=build,
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
  reopen a terminal mission. `followup_mission`/`CreateFollowUp` also
  accept `attach` (parent workspace files, copied into the child
  workspace by the provisioner before discover and recorded as "pdf"
  sources with `MissionID` set) and `brief` (a "brief" source rendered
  as referenced context).
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
- Batch verification (D-094, issue #518): after every build turn
  `verifier.verifyAll` checks every unit (unverified ones fully,
  already-passed ones as the regression subset) and the driver hands
  the outcomes to `Step` via `StepInput.Verified`; `applyVerification`
  records `harness_passed`, a 4 KB `verify_excerpt` and `regressed` on
  the plan units, persisted only by `ApplyTransition`. A failing or
  regressed unit costs a worker turn (`worker_retry`), never a review;
  `stepReviewApprove` flips `passes` on harness-passed units only; a
  build phase with every unit harness-passed and no open finding
  skips the worker turn (`mission.generate_skipped`: event type
  names are stable identifiers and keep their pre-#611 spelling, the
  same way `mission.review_*` survived the review -> prove rename).
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
  build turn that leaves units without harness evidence and no
  finding open stays in build (`mission.generate_continued`); prove
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
- Explicit worker harness is a contract (issue #704): when no chain
  entry can serve `harness` (cooldown, unusable, resolve failed,
  unknown) `RunWorker` returns `ExecutorUnavailableError` and the
  driver pauses as infra with `until` in the pause payload, which
  `autoResumeInfra` waits for. Never a native turn in its place.
  Cooldown fires only on provider signals (transport death, spawn
  failure, auth failure), never on local file errors, idle or
  run-budget kills. Gateway no-failover codes (400/404/413/422,
  invalid_request) surface as `ErrProviderRejected` and pause as infra.
- `make canary` is the regression gate for any harness change.
