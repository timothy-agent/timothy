# Missions harness

Loaded when working under `internal/brain/missions/`. Moved out of the root
AGENTS.md so other work does not pay for it every session.

- `internal/brain/missions/`: `statemachine.go` (pure `Step()`, sole
  transition logic), `store.go` (`ApplyTransition` is the only state
  writer; append-only `mission_events`), `driver.go`, `runner.go`
  (native runner over `loop.Agent`), `policy.go` (per-kind/light
  behavior flags), `provision.go`, `budget.go`, `verifier.go`,
  `worktree.go`, `packet.go`, `sentinel.go`, `review.go`, `verify.go`,
  `template.go`, `notify.go`, `sweep.go`, `memory.go`. Schema:
  `migrations/0001_init.sql` (edited in place pre-release, never new
  ALTER migrations).
- Mission phases (D-086 issue #455, renamed again by issue #611):
  discover -> plan -> build -> prove -> result -> done|failed.
  `parsePhase` (statemachine.go) still accepts the pre-rename names
  (explore/execute/review, and generate) at read time, mapping them to
  discover/build/prove, so a new binary reads old rows safely before
  the operator's data migration runs; historical `mission_events` payloads keep their old phase
  names forever, tolerated by the web timeline renderer. Result is
  deterministic harness code (zero LLM turns): destinations delivery
  (including github push/PR, issue #561), artifact copy, and KB
  promotion all run there now, not on the old done transition; a
  failure parks the mission IN result with a visible pause reason
  instead of being lost.
- Light missions (D-069, kind=general only): born in phase=build,
  skip discover/plan/prove; the deliverable travels in mission_status's
  `final_output` argument (reasoning models emit tool calls with no
  plain text). Digest automations run light.
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
  as referenced context). The HTTP create path with `parent_mission_id`
  runs `InheritParent` (issue #923): a non-empty body field wins, an
  empty one inherits the parent's value, destinations never inherit,
  and a kind override skips the kind-bound fields
  (executor_session_policy, repo source, flow).
- Mission attachments (issue #359): PDF/text converted via markitdown,
  images captioned via the vision route (`chat.CaptionImageOverGateway`),
  audio transcribed via the whisper sidecar, all ONCE at create (prompt-
  cache stability), stored on the mission's `sources` jsonb column,
  rendered neutralized into every prompt labeled by mime (document/
  image/audio); capped at 8; API responses strip the markdown. Automation
  mission actions carry the same attachments, resolved once at
  automation create/patch time so a run never re-converts.
- PDF export: POST /v1/missions/{id}/export-pdf renders workspace
  markdown (single file, or all files merged book-style) through
  `internal/brain/pdfgen`, which caches by content hash in
  `pdf_renders` and stores output via the attachment store; enabled
  only when PDFGEN_URL is set (derived read-only setting
  `pdf_export_enabled`).
- Mission display names: generated fire-and-forget at create
  (`chat.TitleOverGateway`), backfilled once in the result phase's step
  (`Driver.SetNameMission`) if still empty. Only a mission that cuts a
  branch waits on the name first, at most `nameBeforeBranchTimeout`
  (5 s, issue #1081), so the slug can come from the title (issue #494).
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
  `BuildInvocation` fail three retries later. A retry the harness
  caused (`StepInput.HarnessCaused`: an unreadable sentinel, a runner
  error, an executor death, an idle timeout) spends no iteration and
  skips the `MaxIterations` ceiling, marked `harness_caused` on
  `mission.retry`; the stall and backoff brakes still count it, so a
  harness that keeps failing still pauses, including on a planless flow
  where the stall pause is the only stop left. `missions.harness_retries`
  is their lifetime count, capped by
  `settings.mission_harness_retry_cap` (pause cause
  `harness_retries_exhausted`) and reset by nothing. On a
  harness-caused FAILURE the cap is checked before the backoff brake:
  resume clears the pause but not `ConsecutiveFailures`, so a
  backoff-first order would re-pause as backoff on every resumed harness
  failure and the cap would be dead code. On a harness-caused RETRY the
  stall brake still comes first.
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
- Evidence-only units (D-123, issue #950): a unit whose deliverable is
  a side effect (a GitHub issue, an API call) sets `evidence_only` on
  `submit_plan` and lists no artifacts. The flag is explicit, never
  inferred from an empty artifacts list, so a unit that forgot its
  artifacts is still rejected. `check_cmd` still has to fail before the
  work and pass after. When a rejected plan's resubmission drops a unit
  instead of keeping it, marking it evidence-only, or reporting
  infeasible, the runner records `mission.plan_scope_dropped` and
  carries the dropped titles into review packets and the outcome digest.
- Bootstrap units (D-124, issue #980): when the sandbox lacks the
  project's toolchain, the plan's first unit sets `bootstrap` on
  `submit_plan` and installs it into the workspace (the sandbox has no
  root). At most one, always first, still gated by a `check_cmd`
  (`checkBootstrap`). With a bootstrap first unit the plan-acceptance
  probe accepts a missing command (exit 127 / "not found") in any
  unit's `check_cmd` as the expected pre-state; "already exits 0" is
  still rejected and post-turn verification is unchanged. The
  granularity merge never folds a bootstrap unit into the work units.
  Coding missions only (issue #996): the plan prompt carries the
  bootstrap rule (`planBootstrapRule`) only when the discover notes hold
  a harness `bootstrapAllowance` note, and `checkBootstrap` rejects a
  bootstrap unit on any other kind.
- Repo toolchain versions (D-126, issue #991): `detectToolchainVersions`
  (toolchain.go, marker-only, normalized to mise version selectors;
  `detectMissionToolchains` falls back to versions the goal names) fills `missions.toolchains`. Brain installs
  them with `mise use --global` through the sandbox exec path right
  after provisioning (`provisioner.installToolchains`, 10 minute
  ceiling), never inside sandboxd's create call (30 s header timeout).
  A failure never
  fails provisioning: `mission.toolchain_install_failed` carries the
  output tail and the discover nudge and notes allow a bootstrap unit
  (D-124). Executor CLIs keep the image's node via rewritten shebangs
  in `deploy/sandbox.Dockerfile`.
- One sandbox image (D-141, issue #1015): no `environment` field, no
  per-language images, no discover-driven sandbox recreate. Unpinned
  node, python and php run the image's own; a JVM build file with no
  java pin installs JDK 21 (`defaultJava`), plus Maven (`pom.xml`, no
  `mvnw`) or Gradle (no `gradlew`) through mise; rust installs the minimal
  rustup profile (`toolSpec`). The discover report's `stack` is checked
  against the image's toolchains plus the installed ones
  (`stackCovered`); an uncovered stack gets the bootstrap note. The
  create and automation APIs accept and drop a stale `environment` key
  (`RemovedField`), and so does sandboxd's exec API, so a brain and
  sandboxd version skew during a deploy cannot fail an exec.
  `collectEnvFacts` starts from the stored facts, so
  a re-collect never drops `EnvFacts.Prepare`.
- Every ecosystem (D-139, issue #1014): detection reads node, python,
  go, java, ruby, rust and php pins (a Laravel repo's `.nvmrc`
  counts). The image sets
  `MISE_IDIOMATIC_VERSION_FILE_ENABLE_TOOLS` so mise reads the repo's
  plain version files itself; Go reads them too for the facts and the
  pre-discover install, and alone parses what mise does not read
  (`engines.node`, `requires-python`, composer.json). `.sdkmanrc` is
  mise-only. An open lower bound never installs its floor:
  `normalizeToolVersion` emits `latest` for `>=`, the caret prefix for
  `^`, one part less for `~=`, and mise resolves the newest match at
  install time from the index the install downloads anyway; a range
  with `<` keeps major.minor of its lower bound. The code floor keys on
  the artifact's language (`codeExtensions` -> `toolchainByLanguage`):
  a unit passes with a call to the toolchain of any language its
  source artifacts are in.
- PHP minors (D-127, issue #992): the image bakes 8.1 to 8.4
  (default 8.4, `phpMinors` mirrors the Dockerfile). For any repo
  with composer.json, `composerPHP` (phpversion.go) picks a minor:
  `config.platform.php` wins; otherwise the newest baked minor that
  composer.json's `require.php` and every composer.lock package's
  `require.php` (packages and packages-dev) allow (D-139), matched by
  `phpSatisfies` (`|`/`||`, `^`, `~`, comparison ranges, wildcards).
  When no baked minor satisfies the lock it falls back to composer.json
  alone and `EnvFacts.PHPNote` names the blocking packages; a
  constraint no baked minor satisfies keeps its own minor.
  `buildPHPSelectCmd` links `php`, `phar`, `phar.phar` from
  `/usr/bin/<name><minor>` into `/home/sandbox/.local/bin` as the
  sandbox uid. No root exec: the rootfs is read-only with all caps
  dropped, so `update-alternatives` cannot run. An unbaked minor fails
  the same way as a mise install.
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
  the operator's rename alter runs) is a gate, never a proof. `acceptPlan`
  rejects, with one planner recovery turn: the `| grep -q '^$'` idiom
  (exits 1 on empty output), a coding unit with source artifacts and no
  toolchain call (`checkCodeFloor`, per artifact language), and, via
  a 60 s sandbox probe against the pre-work tree, a gate that already
  exits 0 or names a command the sandbox lacks. Verifying that the
  criteria are met is the reviewer's job, not the gate's.
- Honest plans (issue #1007): the granularity merge counts code-extension
  artifacts only and fires only when 2 or more units carry them in one
  dir. With a granularity rejection on record (in-session, or a failed
  plan `mission.turn` reason) a resubmitted split plan is waived once
  per mission (`mission.plan_granularity_waived`). The probe accepts
  exit 127 from a first token under `vendor/`, `node_modules/`, `.venv/`
  or `bin/`. The recovery turn quotes the rejected `submit_plan` JSON,
  and `replanNotes` carries the last failed plan turn's reason
  (`Mission.PlanGate`, derived from events by `Driver.planGateState`).
  With a repo destination a push/PR unit is rejected: the result phase
  delivers the branch and the PR.
- Report placement (D-134, issue #1039): on a coding mission over a
  repo source, `checkReportArtifacts` rejects a new `.md`/`.markdown`/
  `.txt`/`.rst` artifact whose base name the goal does not contain and
  whose top directory the goal does not name as a word; files already
  in the worktree pass. `checkUntrackedAssumptions` rejects an
  assumption saying a path stays untracked / not committed while a
  unit lists that exact path in artifacts or scope or as a check_cmd
  word, matched per clause. A claim clause naming no path that says
  "lockfile" (or whose assumption does) covers every known lockfile
  name a unit uses that git does not track yet (`trackedIn`); tracked
  lockfiles stay allowed. A planned worker carries the report in
  `final_output` (mission_status natively, the optional field of the
  delegated result object otherwise); the driver stores the latest
  non-empty one as `missions.final_output`, the web Result panel
  prefers it over `last_evidence`, and `PRBody` renders it under
  `## Summary` (D-152, 60k rune cap).
- Scope rule (D-151, issue #1173): a coding mission's plan prompt and
  both worker system prompts (native, and the delegated system append
  every CLI adapter carries) end with `codingScopeRule`: change only
  what the goal needs, evidence belongs in the PR the harness fills, no
  new report, test-log or audit-output file unless the goal asks (the
  D-134 gate enforces new report artifacts). Prompt only. Generic
  default only: stricter preferences (e.g. leave existing report
  files alone) go in the agent's prompt overlay, which `PlanSession`
  appends to the plan system prompt as the worker packet does.
- PR summary (D-152, issue #1174): when a repo destination's mode is
  push_pr, or the mission has a repo connection that "Push & open PR"
  can open a PR on (`deliversPR`), and at most one
  unit lacks harness evidence, the worker packet (native and
  delegated) carries `prSummaryRequest`: start final_output with a
  short what-and-why summary, then any report the goal asks for.
  D-153 (issue #1195): the same request puts a `Title: <conventional
  subject>` line first; `PRTitleFromOutput` validates it (known type,
  lowercase start, no trailing period, at most 72 bytes) and `openPRFor`
  uses it as the PR title, else `ConventionalPRTitle`; `PRBody` drops
  the line. The mission name is unchanged.
  `PRBody` is summary (neutralized, capped), dependency evidence,
  units, attribution; the goal is never in it, not even as a fallback.
- Environment facts (issue #1008): `renderEnvFacts` (envfacts.go)
  appends one deterministic block to the discover, plan, reviewer
  (native and delegated) and worker (native and delegated) prompts:
  repo URL, base branch and commit, mission branch, each repo
  destination's kind and mode with the fixed delivery text, no
  credentials, manifests and lockfiles to depth 3, probed tool versions
  and absent tools, sandbox limits, and gaps (Testcontainers, a
  Windows-only .NET solution) the planner may declare infeasible. The
  probed part (`EnvFacts`) is collected once at provisioning
  (`provisioner.collectEnvFacts`, coding missions only) and stored on
  `missions.env_facts`. The limit
  constants mirror `internal/sandboxd/manager.go`; change both together.
- Prepare step (D-130, issue #1010): `Driver.Advance` runs
  `provisioner.prepareWorkspace` (prepare.go) for a coding mission in
  discover, after provisioning and before the turn, once per mission
  (`mission.prepare_complete` on record means done). It writes a
  harness `mise.local.toml` at the worktree root carrying only keys the
  repo's own mise config does not declare (`repoMiseKeys`, `tomlKeys`;
  mise loads the local file over `mise.toml`, so omission is how the
  repo wins), then runs through the sandbox exec path, each step a
  `mission.prepare_step` event with exit code, duration and output tail
  under a 25 minute ceiling: `mise install` (tools, osv-scanner via
  aqua; under `miseLocked` like `installToolchains`, D-131), `mise deps install <provider>` per root lockfile
  (`depsProviders`; a root package.json with no node lockfile runs
  `npm install --no-package-lock` instead, so no lockfile lands in the
  repo), the `env-template` custom provider (copies
  `.env.example` and runs `php artisan key:generate` only right after
  the copy), the test ladder (`testLadder`: repo mise task, manifest
  rules, Makefile target; first candidate that exits 0 becomes the
  baseline and `tasks.test`), and osv-scanner over every lockfile into
  `<workspace>/prepare/osv.json`, plus, for that no-lockfile
  package.json, a package-lock.json generated in
  `<workspace>/prepare/npm/` (labeled harness-generated in the facts).
  Step output is stripped of ANSI escapes before it is parsed or
  stored (Collision colors whatever NO_COLOR says). Every outcome lands on
  `EnvFacts.Prepare` and renders in the facts block; a failure is a
  fact, never a mission failure. `mise.local.toml` and `.env` are never
  staged (`harnessWrittenPaths`). `Rollback` keeps them (D-133), and
  `verifyAll` rewrites `mise.local.toml` from the copy in
  `<workspace>/prepare/` when it is missing or lost its header. Not
  here: devcontainer and CI-workflow test sources, nested manifests,
  repo custom providers.
- Lockfile evidence (D-140, issue #1011): when a coding mission's diff
  against its base (`touchedFiles`, lockfiles included) changes a
  lockfile (`lockfileNames`), `verifyAll` judges two harness criteria
  for the units owning it (artifacts or scope, else the current unit):
  the prepare baseline test command (`buildTestCmd`) exits 0 with no
  fewer passed and no more failures or warnings, and the prepare audit
  (`buildAuditCmd` into `<workspace>/prepare/osv-after.json`) finds no
  more advisories than the baseline. Measured at most once per pass and
  reused while HEAD, status and the uncommitted diff are unchanged;
  `mission.lockfile_evidence` records it and `EnvFacts.Lockfile` stores
  it. A failure is a `lockfile_evidence` check failure, so `passes`
  stays false. A missing baseline is `not_measured`, never a failure.
  Full review rounds get package-level lockfile summaries
  (`lockfile_summary.go`: composer.lock and npm lockfiles parsed, others
  a line count) and the criteria; the PR body gets a before/after table.
- Test databases (D-142, issue #1017): `detectServices` (daemons.go)
  reads CI workflow and GitLab services, compose images, Rails
  `config/database.yml`, Django settings and Laravel `phpunit.xml` over
  the env template for PostgreSQL and Redis. Each need becomes a mise
  `[daemons.<preset>]` table in `mise.local.toml` (unless the repo
  declares it) with `port = "auto"` and `data_dir` under
  `/tmp/timothy-daemons`, plus `pitchfork` in `[tools]` (without it
  `mise daemons stop` finds no version). mise's default data dir is
  under `MISE_STATE_DIR` on the shared cache volume; the /tmp tmpfs
  makes the data die with the container. Prepare starts each daemon
  before the baseline test (`daemon:<name>` steps), reads the
  connection variables from `mise env --json` onto
  `EnvFacts.Prepare.Services`, and the harness `tasks.test` lists them
  under `daemons` so `mise run test` restarts them after a sandbox
  restart. A daemon that does not start is stopped, dropped from
  `mise.local.toml` and recorded; the facts say "no database service;
  use sqlite where the project supports it". Both presets have no auth
  and bind 127.0.0.1 only (a peer container on the bridge cannot
  connect); prepare reads `/proc/net/tcp*` for each service port into
  `ServiceFact.Listen`, and the facts warn when a repo-declared daemon
  listens beyond loopback. No limit change: both
  presets idle at about 110 MiB and 30 tasks. MySQL waits for a preset.
- Per-criterion review rubric (issue #718): `review_verdict` carries
  `criteria` (unit index, criterion index, met/not_met/cannot_tell,
  evidence), optional so existing fixtures still parse and an unknown
  status reads as cannot_tell. `Driver.runReview` decides in Go, not in
  the prompt: a not_met criterion opens a blocking finding titled with
  the criterion and forces rework even on an approve decision (Warn
  logged), a cannot_tell opens a minor finding, a title already open
  opens nothing, and unanswered criteria are only logged. Skipped: a
  criterion on a unit outside the round (`mergeFindings` stamps every
  finding with the reviewed unit) and a not_met quoting no evidence,
  read as cannot_tell since the D-095 gate would demote it anyway. The
  full-round packet numbers each unit's criteria and heads each unit
  with its plan index; a findings-only round asks no rubric.
- Every retry, iteration and auto-resume ceiling is an operator
  setting, read per turn rather than compiled in (issue #718):
  `mission_default_max_iterations` (Store.Create via
  `SetDefaultMaxIterations`), `mission_backoff_failures`,
  `mission_stall_rounds` and `mission_harness_retry_cap` (the
  statemachine `Config`, built per Advance by `Driver.config` from
  `SetCeilings`), `mission_auto_resume_backoff_max` and
  `mission_auto_resume_infra_max` (the sweep ladders' exhausted caps via
  `SetAutoResumeMax`). `DefaultConfig` and the `sweep.go` constants stay
  as the fallbacks. Delegated CLI runs are capped too:
  `executor_worker_max_turns` (40) alongside
  `executor_review_max_turns` (6), both on `SetExecutorKnobs`.
- `submit_plan` decodes strictly in Execute (issue #844): a schema
  error is a tool error the planner sees in-turn, and the harness-retry
  cap stays the backstop for a planner that never resubmits a usable
  plan.
- Mission notifications ride the events inbox (D-117, issues #843 and
  #922): `ApplyTransition` commits a `mission.done`/`mission.failed`
  row on a terminal phase and a `mission.paused`/
  `mission.waiting_for_input` row on arriving at that status, and
  `NotifyConsumer` sends them. The driver never notifies directly; it
  only kicks the drainer. Terminal sends are once per mission,
  actionable ones once per events row (`mission.notified` marker with
  the row's dedup key). Leaving paused or waiting_for_input
  (`ApplyTransition`, `AnswerPendingInput`) marks the mission's unread
  paused/waiting rows read in the same tx, and the consumer skips an
  actionable event whose mission already left that status, so the next
  pause notifies again (issue #935).
- `CommitUnit` never runs a bare `git add -A` (D-121, issue #948):
  tracked edits and deletions stage tree-wide (`git add -u`); untracked
  files stage only under the unit's artifacts, scope, or an artifact's
  parent dir (never the workspace root). Left-out untracked files are
  reported in `mission.commit_skipped_paths`.
- Output tail and pause causes (D-136, issue #1013): shell output (native
  and sandbox runner) keeps its first 24 KB and last 40 KB with a
  `[N bytes dropped]` marker (`builtin.HeadTailWriter`), never head only.
  Every `mission.paused` payload (and `mission.plan_awaiting_approval`)
  carries a `cause` (`Cause*` in statemachine.go), finer than
  `PauseReason`; `harness_retries_exhausted` also carries `phase`. The web
  banner (`web/src/lib/pauseCause.ts`) labels it, e.g. "plan rejected 3
  times" plus the last rejection. A new pause path adds a cause and a
  label.
- `make canary` is the regression gate for any harness change.
