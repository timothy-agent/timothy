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
  model-authored `check_cmd`. `passes` flags flip only on harness
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
- Non-coding units whose artifacts + check_cmd pass harness checks skip
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
- A delegated DONE over an untouched worktree (WT summary clean) is a
  forced retry with `session_reset` on `executor.result` (issue #706);
  `LastRunState` reads it and the next run starts fresh. No retry of
  any kind rolls the worktree back (issue #718: a RETRY reports
  unfinished work); only a review rework does.
- Delegated failure classes (issue #718): a result whose error text is
  a provider rejection (`isProviderRejection`: billing, quota, 4xx/5xx,
  "use the v1/responses endpoint") is never a verdict; `finish` records
  it, cools the entry and returns `ExecutorUnavailableError` (infra
  pause with `until`). A sandbox launch error (`isSandboxError`,
  `sandboxclient:` prefix) pauses as infra for `sandboxRetryDelay` and
  never cools a provider. The router marks a `harnessChatOnly` harness
  (pi) unusable on a Responses-only catalog model, and any harness
  entry that resolves to no model (`emptyModelSkip`: chain pins none,
  provider row has no `default_model`) unusable instead of letting
  `BuildInvocation` fail three retries later.
- A tool result with no content (`git status --short` on a clean tree)
  used to 400 every OpenAI Responses turn: the API rejects
  `"output": ""` as missing, the continuation retry resends the full
  history with the same item and fails again, and the log shows only
  the second failure (`input[N].output`, N = the empty item's index in
  the full map). `openairesponses.appendMessage` substitutes
  `emptyToolOutput` ("(no output)"); the two-request shape is why the
  index never pointed at a one-item continuation body (issue #718).
- A run with zero tool calls did nothing (issue #718): `pollToVerdict`
  marks the verdict `noWork`, `finish` sets `session_reset`, and
  `RunWorker` relaunches once fresh (`executor.relaunched`,
  `maxNoWorkRelaunch`) before the verdict reaches the driver; a BLOCKED
  that names a plan defect is kept as information.
- A plan unit whose every artifact belongs to an earlier unit (a
  trailing "format and verify" unit) is rejected at plan acceptance
  (`checkOwnArtifacts`): its commands belong in the producing units.
- Plan defects travel back to the planner (issue #718): a worker
  BLOCKED note that names the plan (`namesPlanDefect`: a gate,
  criterion, artifact path, "cannot pass", "in isolation") is
  `InputPlanDefect`, which spends the one automatic replan with the
  note recorded as progress; once spent it parks like any block. A
  replan keeps harness evidence: `restorePassedUnits` matches prior
  verified units by title+check_cmd or by identical artifact set and
  carries `harness_passed` (and `passes`) forward.
- CLI session state lives in `<workspace>/executor/<harness>`
  (`InvocationSpec.StateDir`, issue #707): codex's CODEX_HOME is shared
  by every run of a mission so `codex exec resume` finds its rollout.
  A resume whose stderr says the session is gone dies as
  `session_lost` with `session_reset`, no cooldown, next run fresh.
- `RenderForDelegated(runDir)` (issue #705) orders the CLI prompt goal,
  lineage line, reference list, plan, findings, progress, git log,
  attachments, then the current unit last with artifacts and verify
  command. Parent digest and references are files under `runs/<id>/refs/`
  (returned as files, written by `launchRun`), never inline.
- Plan gates (issue #718): a unit's `check_cmd` (renamed from
  `verify_cmd`; `Plan.normalize` reads the old key until the
  pending-alters rename runs) is a gate, never a proof. `acceptPlan`
  rejects, with one planner recovery turn: the `| grep -q '^$'` idiom
  (exits 1 on empty output), a coding unit with source artifacts and no
  toolchain call (`checkCodeFloor`, per sandbox environment), and, via
  a 60 s sandbox probe against the pre-work tree, a gate that already
  exits 0 or names a command the environment lacks. Verifying that the
  criteria are met is the reviewer's job, not the gate's.
- `make canary` is the regression gate for any harness change.
