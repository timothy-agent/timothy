-- Missions are long-running, agent-driven units of work distinct from
-- chat sessions: a mission survives across many model turns, tracks a
-- plan and progress log, and drives itself through a fixed phase
-- pipeline under a state machine (internal/brain/missions).
--
-- phase and status are deliberately NOT CHECK-constrained. A future
-- code rollback that doesn't recognize a newer phase/status value must
-- degrade the row to a safe paused/infra state in Go (parsePhase/
-- parseStatus), not have Postgres reject it outright — corruption-
-- safety over strictness at the schema layer.
CREATE TABLE IF NOT EXISTS missions (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal                  text NOT NULL,
    kind                  text NOT NULL CHECK (kind IN ('coding', 'general')),
    agent_id              uuid REFERENCES agents(id),
    phase                 text NOT NULL DEFAULT 'explore',
    status                text NOT NULL DEFAULT 'idle',
    pause_reason          text NOT NULL DEFAULT '',
    pause_message         text NOT NULL DEFAULT '',
    workspace             text NOT NULL DEFAULT '',
    worktree              text NOT NULL DEFAULT '',
    branch                text NOT NULL DEFAULT '',
    base_commit           text NOT NULL DEFAULT '',
    spec                  jsonb NOT NULL DEFAULT '{}',
    progress              jsonb NOT NULL DEFAULT '[]',
    iteration             integer NOT NULL DEFAULT 0,
    max_iterations        integer NOT NULL DEFAULT 8,
    consecutive_failures  integer NOT NULL DEFAULT 0,
    last_gap_fingerprint  text NOT NULL DEFAULT '',
    stall_count           integer NOT NULL DEFAULT 0,
    budget_amount         numeric(12,2),
    budget_currency       char(3) NOT NULL DEFAULT 'USD',
    route                 text NOT NULL DEFAULT '',
    review_route          text NOT NULL DEFAULT '',
    -- PlanRoute, when set, is the route oversight phases (explore, plan,
    -- replan) run on instead of route -- "GLM plans, local executes":
    -- worker/execute turns keep running on route while oversight runs on
    -- a stronger model. '' (the default) means route covers everything,
    -- exact prior behavior. review_route still overrides for review
    -- specifically: precedence there is review_route > plan_route >
    -- route (internal/brain/missions/runner.go's reviewRoute).
    plan_route            text NOT NULL DEFAULT '',
    -- RouteModel/PlanRouteModel/ReviewRouteModel (D-078) each pin one
    -- phase axis to one exact chain entry in the route it would
    -- otherwise resolve, as "provider name/model" (router.go's
    -- splitProviderModelHint) -- '' means today's first-usable walk.
    -- Precedence mirrors the route helpers exactly: route_model backs
    -- execute (workerRoute), plan_route_model backs explore/plan
    -- (oversightRoute), review_route_model falls back review_route_model
    -- > plan_route_model > route_model (runner.go's reviewRouteModel).
    -- Escalation is never pinned: it is a failure-path fallback and a
    -- stuck pin would defeat its purpose (workerRoute clears route_model
    -- when it swaps to escalation_route). A pin naming an entry the
    -- chain no longer has just fails to match and the normal
    -- first-usable walk runs -- never validated to exist at write time.
    route_model           text NOT NULL DEFAULT '',
    plan_route_model      text NOT NULL DEFAULT '',
    review_route_model    text NOT NULL DEFAULT '',
    pending_permission    text,
    schedule_id           uuid,
    -- ParentMissionID names the terminal mission this one follows up
    -- on (api/missions.go's create); parents are terminal, exactly the
    -- rows Delete can remove, so SET NULL keeps a follow-up mission
    -- valid rather than blocking its parent's deletion.
    parent_mission_id     uuid REFERENCES missions(id) ON DELETE SET NULL,
    -- ParentContext is an immutable outcome-digest snapshot of the
    -- parent mission taken at follow-up create time (missions.OutcomeDigest)
    -- — rendered into the follow-up's explore/plan/work prompts.
    parent_context        text NOT NULL DEFAULT '',
    -- ReferencedContext is an immutable digest of the composer #-mention
    -- references (missions/sessions/kb docs) picked at create time,
    -- resolved via chat.Service's reference resolver -- rendered into
    -- explore/plan/work prompts additive to parent_context, not a
    -- replacement for it (a mission can be both a follow-up AND carry
    -- its own picked references).
    referenced_context    text NOT NULL DEFAULT '',
    -- Attachments is a jsonb array of {id, mime, name, markdown}: id
    -- names an attachments-store row, markdown is the PDF's markitdown
    -- conversion snapshotted ONCE at create time — re-conversion drift
    -- would rewrite earlier prompts (api/missions.go's create).
    attachments           jsonb NOT NULL DEFAULT '[]',
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    -- Opt-in escalation ladder: when set, worker turns switch to this
    -- route after a worker failure or review rework instead of burning
    -- more iterations on a model that already proved too weak for the
    -- unit. Empty (the default) disables escalation entirely -- route
    -- changes must never be a surprise cost jump.
    escalation_route      text NOT NULL DEFAULT '',
    -- PromptOverlay snapshots the creating agent's prompt_overlay at
    -- create time (like route/review_route already do) — a mission
    -- outlives the request that made it, so it can't re-resolve a live
    -- agent lookup later without risking a surprise prompt change
    -- mid-mission if the agent row is edited while the mission runs.
    prompt_overlay        text NOT NULL DEFAULT '',
    -- Knowledge snapshots the creating agent's kb_collections allowlist
    -- at create time, same reasoning as prompt_overlay above — a
    -- mission outlives the request that made it, so search_kb's
    -- collection scoping can't re-resolve a live agent lookup later.
    -- Empty array means search_kb is never offered on this mission's
    -- turns, regardless of what the agent row says today.
    knowledge             jsonb NOT NULL DEFAULT '[]',
    -- Harness snapshots the operator's execution-strategy choice for a
    -- coding mission's worker turns at create time, never re-read from
    -- settings at dispatch. "" is native; "claude-cli" (etc) names a
    -- registered delegated executor (internal/brain/missions/executor).
    harness               text NOT NULL DEFAULT '',
    -- Light missions (D-069, general kind only) skip explore/plan/
    -- review: born in phase=execute, one bare worker turn, the final
    -- worker message is the deliverable.
    light                 boolean NOT NULL DEFAULT false,
    -- Mission worker turns run through loop.Agent same as chat, but
    -- tool-call bookkeeping (session_events, tools audit) hard-requires
    -- a real session_id uuid FK -- a mission has no chat session of its
    -- own. Give every mission a hidden session row purely so that
    -- bookkeeping has something real to attach to; nothing about it is
    -- chat-facing (no title shown, sessions list can filter it out by
    -- join).
    session_id            uuid REFERENCES sessions(id),
    -- pending_permission already holds the broker-issued id a mission
    -- is parked on; these columns carry what the UI needs to render a
    -- real decision prompt (tool name, args, danger level, rationale)
    -- instead of a bare "waiting" banner -- mirrors chat's
    -- PermissionRequestEvent.
    pending_permission_tool      text NOT NULL DEFAULT '',
    pending_permission_args      text NOT NULL DEFAULT '',
    pending_permission_danger    text NOT NULL DEFAULT '',
    pending_permission_rationale text NOT NULL DEFAULT '',
    -- Missions run for hours unattended; per-command-shape approval
    -- (built for a human watching a chat session) would otherwise park
    -- a mission on every novel-but-harmless shell call. Default true:
    -- new missions auto-approve DangerSafe shell calls via a standing
    -- session grant set at creation (Driver.Create) -- destructive-
    -- classified commands still always ask, unaffected by this column
    -- or any grant.
    auto_approve_safe     boolean NOT NULL DEFAULT true,
    -- The reviewer judges the baseline git diff, but a general
    -- mission never touches tracked files -- its diff is
    -- always empty, so the reviewer previously had zero evidence to
    -- judge and rejected every round. This carries the worker's own
    -- mission_status evidence text forward from execute to review,
    -- alongside (not instead of) the diff for coding missions.
    last_evidence         text NOT NULL DEFAULT '',
    -- The explore phase's findings, carried into the plan phase's
    -- prompt (internal/brain/missions/driver.go's runPlan) so the
    -- planner sees what exploration turned up, not just the bare goal.
    explore_notes         text NOT NULL DEFAULT '',
    -- A mission gets exactly one automatic replan attempt on stall
    -- (statemachine.go's stepWorkerRetry/stepReviewRework) before a
    -- second identical stall pauses for a human, same as always.
    replan_used           boolean NOT NULL DEFAULT false,
    -- Environment selects the per-language sandbox image (D-05x,
    -- sandboxd's image allowlist) a coding mission's container runs.
    -- Unlike harness, this has NO settings default: precedence is
    -- explicit request -> auto-detect from repo markers at
    -- provisioning (driver.go's ensureProvisioned) -> base ("").
    -- Sticky once detected (store.SetEnvironment) so a mission never
    -- re-detects mid-run. General missions never set this.
    environment           text NOT NULL DEFAULT '',
    -- FinalOutput is a light mission's verbatim final worker message
    -- (D-069) — the deliverable itself, since destinations delivery has
    -- no other body content for a mission with no review/artifacts.
    final_output          text NOT NULL DEFAULT '',
    -- Short display name, generated once (store.SetNameIfEmpty) the
    -- same way a chat session's title is (chat.go's autoTitle) — a
    -- one-shot best-effort gateway call after creation, never blocking
    -- or failing creation. Scheduler-fired missions get the schedule's
    -- own name directly, no LLM call. Empty means generation hasn't
    -- landed yet (or a scheduler mission predates this column); the UI
    -- falls back to a truncated goal. Never re-summarized once set.
    name                  text NOT NULL DEFAULT '',
    -- A coding mission can clone an existing GitHub repo instead of
    -- self-initializing an empty one (Workspace.Provision): repo_url is
    -- the repo's https clone URL, connector_id names the github-kind
    -- connectors row whose PAT authenticates the clone. Both empty
    -- (the default) is the existing self-init behavior; repo_url
    -- without a connector_id is rejected at create time (api/missions.go)
    -- -- v1 has no anonymous-clone path. The clone auth token itself is
    -- never persisted here or anywhere else: it's resolved fresh from
    -- connector_id's credential_ref at provisioning time only.
    repo_url              text NOT NULL DEFAULT '',
    connector_id          text NOT NULL DEFAULT '',
    -- Consent-at-create for the mission's auto-completion action: ''
    -- (default) does nothing when the mission reaches done; 'push'
    -- pushes the branch; 'push_pr' pushes then opens a pull request.
    -- Chosen by the operator at create time (api/missions.go), never
    -- decided by the model -- keeps the pushes-stay-human invariant:
    -- the harness only ever executes a choice a human already made.
    -- Requires repo_url+connector_id and kind='coding', same guards as
    -- the manual push/pr endpoints.
    on_complete           text NOT NULL DEFAULT '',
    -- This mission's own override of the settings-configured git
    -- strategy defaults (settings.ValueGitBranchPattern/
    -- ValueGitCommitStyle): '' (the default) means "use the settings
    -- default," resolved fresh at provisioning/commit time
    -- (internal/brain/missions/driver.go), never baked in here.
    -- branch_pattern is a validated template (internal/brain/missions/
    -- branchtemplate.go); commit_style is 'conventional' or 'plain'.
    branch_pattern        text NOT NULL DEFAULT '',
    commit_style          text NOT NULL DEFAULT '',
    -- Destination ids to deliver this mission's outcome digest to on
    -- the terminal done transition (destinations.go's Deliverer,
    -- driver.go's terminal-transition hook). Never model-decided —
    -- api/missions.go's create validates every id against the
    -- operator-owned destinations table before it lands here.
    destination_ids       uuid[] NOT NULL DEFAULT '{}',
    -- workflow_run_id/workflow_step name the workflow run and step
    -- (internal/brain/workflows) this mission was spawned as, if any.
    -- NULL/'' for an ordinary mission. The workflow engine reads these
    -- via mission terminal events; it never writes mission state.
    workflow_run_id        uuid,
    workflow_step          text NOT NULL DEFAULT '',
    -- ArtifactRefs: this mission's declared artifact files, best-effort
    -- copied into the attachment store on the terminal done transition
    -- (driver.go's copyArtifacts) — a jsonb array of {id, mime, name},
    -- mirroring attachments' own shape. Never bytes (D-045). Lets a
    -- mission's result artifacts survive workspace deletion, unlike the
    -- live-workspace files ArtifactsSection browses.
    artifact_refs          jsonb NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS missions_status_idx ON missions (status);
-- The work-slot cap (ClaimWorkSlot) and the boot sweep both need
-- "which missions are actively occupying a slot," cheaply.
CREATE INDEX IF NOT EXISTS missions_active_idx ON missions (phase) WHERE phase NOT IN ('done', 'failed');

-- Append-only event log, the mission's audit trail and the Timeline
-- UI's data source. seq is assigned under a SELECT ... FOR UPDATE on
-- the parent mission row (serializes appends per-mission only, not
-- globally) — see internal/brain/missions/store.go AppendEvent.
CREATE TABLE IF NOT EXISTS mission_events (
    mission_id  uuid NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    seq         bigint NOT NULL,
    kind        text NOT NULL,
    payload     jsonb NOT NULL DEFAULT '{}',
    -- provenance distinguishes real driver output from test/replay
    -- fixtures written into the same table for scenario tests.
    provenance  text NOT NULL DEFAULT 'live' CHECK (provenance IN ('live', 'test', 'replay')),
    fingerprint text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mission_id, seq)
);

-- Schedules fire mission templates on a cron cadence
-- (internal/brain/missions/scheduler.go). mission_template is applied
-- verbatim as the new mission's initial columns each firing.
CREATE TABLE IF NOT EXISTS schedules (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name              text UNIQUE NOT NULL,
    cron              text NOT NULL,
    mission_template  jsonb NOT NULL DEFAULT '{}',
    enabled           boolean NOT NULL DEFAULT true,
    expires_at        timestamptz,
    last_run          timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    -- A due fire skipped because a mission from this schedule was still
    -- active is not lost: pending_fire carries it forward so the next
    -- tick with no active mission fires it, instead of the schedule
    -- silently missing that boundary forever (scheduler.go's fireOne).
    pending_fire      boolean NOT NULL DEFAULT false,
    -- Records the most recent skip (backfill grace or active-mission
    -- dedup) for the schedules API to surface; cleared on any
    -- successful fire.
    last_skipped_at   timestamptz,
    skip_reason       text NOT NULL DEFAULT ''
);

-- Deferred FK: a mission may name the schedule that spawned it. NOT
-- VALID skips a table scan on possibly-large existing data at
-- migration time; VALIDATE CONSTRAINT can run later out-of-band if
-- ever needed. New rows are checked immediately either way. Guarded
-- for idempotency: ADD CONSTRAINT has no IF NOT EXISTS form, so check
-- pg_constraint directly. NOT VALID must be preserved so fresh
-- installs match upgraded databases in pg_catalog.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'missions_schedule_id_fkey'
    ) THEN
        ALTER TABLE missions ADD CONSTRAINT missions_schedule_id_fkey
            FOREIGN KEY (schedule_id) REFERENCES schedules(id) NOT VALID;
    END IF;
END $$;

-- Durable notification inbox (internal/brain/missions/notify.go):
-- always written for actionable transitions regardless of whether the
-- best-effort webhook fan-out succeeds.
CREATE TABLE IF NOT EXISTS notifications (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mission_id  uuid NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    kind        text NOT NULL,
    message     text NOT NULL DEFAULT '',
    read        boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS notifications_unread_idx ON notifications (mission_id) WHERE NOT read;
