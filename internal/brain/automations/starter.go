package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/events"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

const (
	// starterPoll is how often the starter looks for starting runs
	// without a Kick.
	starterPoll = 30 * time.Second
	// startBatch caps the runs one pass starts.
	startBatch = 10
	// startRetryEvery spaces retries of a run whose route is unusable.
	startRetryEvery = 30 * time.Second
	// maxStartAttempts fails a run after this many route-unusable
	// attempts, about 30 minutes.
	maxStartAttempts = 60
	// startIdleTimeout replaces pgpool's idle-in-transaction timeout
	// for the claim tx, which stays idle while the mission provisions.
	startIdleTimeout = 10 * time.Minute

	SkipRouteUnusable    = "route_unusable"
	SkipCreateFailed     = "create_failed"
	SkipTriggerGone      = "trigger_gone"
	SkipAllowlistHarness = "harness_ignores_allowlist"
)

var errAgentMissing = errors.New("automation agent does not exist")

// errTriggerGone reports that the run's trigger no longer exists (issue
// #865): trigger_id went NULL between dispatch and start (ON DELETE SET
// NULL) on a run whose event is not run.now, so there is no trigger
// left to read a tool_allowlist from. Starting it unrestricted would
// silently drop whatever the deleted trigger scoped it to, so the run
// is skipped instead.
var errTriggerGone = errors.New("automations: run's trigger no longer exists")

// Starter creates the missions of runs the dispatcher committed as
// starting. It never decides whether a run starts.
type Starter struct {
	store *Store
	// create is Driver.Create.
	create  func(ctx context.Context, m missions.Mission) (string, error)
	resolve missions.ResolveDeps
	// lineage is missions.Store.ParentLineage; nil turns continuity off.
	lineage func(ctx context.Context, missionID string) (missions.SourceEntry, error)
	// destinationEnabled re-checks an action's destination ids; nil
	// drops every id.
	destinationEnabled func(ctx context.Context, id string) (bool, error)
	// notify is Notifier.NotifyMessage; nil skips the notification.
	notify func(ctx context.Context, missionID, kind, message string) error
	kick   chan struct{}
	log                *slog.Logger
	now                func() time.Time
}

// NewStarter wires the starter. create is Driver.Create, resolve the
// shared create-path lookups, lineage missions.Store.ParentLineage and
// notify Notifier.NotifyMessage.
func NewStarter(store *Store, create func(ctx context.Context, m missions.Mission) (string, error), resolve missions.ResolveDeps, lineage func(ctx context.Context, missionID string) (missions.SourceEntry, error), destinationEnabled func(ctx context.Context, id string) (bool, error), notify func(ctx context.Context, missionID, kind, message string) error, log *slog.Logger) *Starter {
	return &Starter{store: store, create: create, resolve: resolve, lineage: lineage, destinationEnabled: destinationEnabled,
		notify: notify, kick: make(chan struct{}, 1), log: log, now: time.Now}
}

// Kick asks for a pass now. Never blocks.
func (s *Starter) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run passes once at boot, then on every poll or Kick, until ctx is
// done.
func (s *Starter) Run(ctx context.Context) {
	ticker := time.NewTicker(starterPoll)
	defer ticker.Stop()
	s.passLogged(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.passLogged(ctx)
		case <-s.kick:
			s.passLogged(ctx)
		}
	}
}

func (s *Starter) passLogged(ctx context.Context) {
	if _, err := s.Pass(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("automations: starter pass failed", "error", err)
	}
}

// Pass handles up to startBatch starting runs, one transaction each,
// and reports how many it handled.
func (s *Starter) Pass(ctx context.Context) (int, error) {
	tried := []string{}
	for len(tried) < startBatch {
		runID, err := s.startOne(ctx, tried)
		if err != nil {
			return len(tried), err
		}
		if runID == "" {
			break
		}
		tried = append(tried, runID)
	}
	return len(tried), nil
}

// startOne claims the oldest starting run not in tried and creates its
// mission. Returns "" when no run is due.
func (s *Starter) startOne(ctx context.Context, tried []string) (string, error) {
	db, err := s.store.db.Get()
	if err != nil {
		return "", fmt.Errorf("automations start: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("automations start: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('idle_in_transaction_session_timeout', $1, true)`,
		strconv.FormatInt(startIdleTimeout.Milliseconds(), 10)); err != nil {
		return "", fmt.Errorf("automations start: idle timeout: %w", err)
	}
	now := s.now()
	var runID, automationID string
	var event []byte
	// NO KEY UPDATE leaves the key-share lock the mission insert's
	// automation_run_id foreign key takes free.
	err = tx.QueryRow(ctx, `SELECT id, automation_id, event FROM automation_runs
		WHERE status = 'starting' AND mission_id IS NULL AND NOT (id = ANY($1::uuid[]))
			AND (event->>'start_attempt_at' IS NULL OR (event->>'start_attempt_at')::timestamptz <= $2)
		ORDER BY created_at, id LIMIT 1 FOR NO KEY UPDATE SKIP LOCKED`, tried, now.Add(-startRetryEvery)).Scan(&runID, &automationID, &event)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", tx.Commit(ctx)
	}
	if err != nil {
		return "", fmt.Errorf("automations start: claim: %w", err)
	}
	if err := s.start(ctx, tx, runID, automationID, event, now); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("automations start: commit: %w", err)
	}
	return runID, nil
}

// start creates the claimed run's mission and records the outcome on
// the run through tx.
func (s *Starter) start(ctx context.Context, tx pgx.Tx, runID, automationID string, event []byte, now time.Time) error {
	// A mission already linked to the run (a crash after create) is
	// adopted, never created twice.
	var missionID string
	err := tx.QueryRow(ctx, `SELECT id FROM missions WHERE automation_run_id = $1 ORDER BY created_at, id LIMIT 1`, runID).Scan(&missionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("automations start: linked mission: %w", err)
	}
	var createErr error
	if missionID == "" {
		missionID, createErr = s.createMission(ctx, tx, automationID, runID, event)
	}
	if createErr == nil {
		if _, err := tx.Exec(ctx, `UPDATE automation_runs SET status = 'running', mission_id = $2 WHERE id = $1`, runID, missionID); err != nil {
			return fmt.Errorf("automations start: mark running: %w", err)
		}
		s.log.Info("automations: run started", "automation_id", automationID, "run_id", runID, "mission_id", missionID)
		return nil
	}
	if errors.Is(createErr, errTriggerGone) {
		// The trigger wins over every other reason (issue #865): a run
		// whose trigger vanished never had a tool_allowlist left to
		// enforce, so it is skipped rather than started unrestricted or
		// counted as a failure against the automation.
		return s.skipRun(ctx, tx, runID, automationID, SkipTriggerGone, createErr, now)
	}
	var prev struct {
		StartAttempts int `json:"start_attempts"`
	}
	_ = json.Unmarshal(event, &prev)
	attempts := prev.StartAttempts + 1
	var routeErr *missions.RouteUnusableError
	if errors.As(createErr, &routeErr) && attempts < maxStartAttempts {
		if err := mergeRunEvent(ctx, tx, runID, map[string]any{
			"start_error": createErr.Error(), "start_attempts": attempts, "start_attempt_at": now.UTC().Format(time.RFC3339),
		}); err != nil {
			return err
		}
		s.log.Warn("automations: run waits for a usable route", "automation_id", automationID, "run_id", runID, "attempts", attempts, "error", createErr)
		return nil
	}
	reason := SkipCreateFailed
	switch {
	case routeErr != nil:
		reason = SkipRouteUnusable
	case errors.Is(createErr, missions.ErrToolAllowlistHarness):
		reason = SkipAllowlistHarness
	}
	if err := mergeRunEvent(ctx, tx, runID, map[string]any{"start_error": createErr.Error(), "start_attempts": attempts}); err != nil {
		return err
	}
	s.log.Warn("automations: run failed to start", "automation_id", automationID, "run_id", runID, "reason", reason, "error", createErr)
	name, disabled, err := finalizeRun(ctx, tx, runID, automationID, "", true, reason, now, s.log)
	if err != nil {
		return err
	}
	if disabled && s.notify != nil {
		// No mission exists, so the notification is operator-level.
		if err := s.notify(ctx, "", "automation_disabled", disabledMessage(name)); err != nil {
			s.log.Warn("automations: disable notification failed", "automation_id", automationID, "run_id", runID, "error", err)
		}
	}
	return nil
}

// skipRun records runID skipped for reason instead of starting or
// failing it (issue #865): unlike finalizeRun's failed path, it never
// touches the automation's consecutive_failures, since the run never
// got a fair chance to start and must not trip the breaker, but still
// frees its concurrency slot for a queued run behind it.
func (s *Starter) skipRun(ctx context.Context, tx pgx.Tx, runID, automationID, reason string, causeErr error, now time.Time) error {
	if err := mergeRunEvent(ctx, tx, runID, map[string]any{"start_error": causeErr.Error()}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE automation_runs SET status = 'skipped', skip_reason = $2, finished_at = $3 WHERE id = $1`,
		runID, reason, now); err != nil {
		return fmt.Errorf("automations start: skip run: %w", err)
	}
	s.log.Warn("automations: run skipped before starting", "automation_id", automationID, "run_id", runID, "reason", reason, "error", causeErr)
	return releaseSlot(ctx, tx, automationID, now)
}

// createMission builds the run's mission from its automation's action
// and hands it to create.
func (s *Starter) createMission(ctx context.Context, tx pgx.Tx, automationID, runID string, rawEvent []byte) (string, error) {
	a, err := getAutomation(ctx, tx, automationID, "")
	if err != nil {
		return "", err
	}
	event := runEvent(rawEvent)
	allowlist, err := runToolAllowlist(ctx, tx, a, runID, event)
	if err != nil {
		return "", err
	}
	if a.Action.Kind != ActionMission || a.Action.Mission == nil {
		return "", fmt.Errorf("automation %s has no mission action", a.ID)
	}
	if err := missions.ValidateTemplate(*a.Action.Mission); err != nil {
		return "", err
	}
	var agentExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agents WHERE id = $1)`, a.AgentID).Scan(&agentExists); err != nil {
		return "", fmt.Errorf("automations start: check agent: %w", err)
	}
	if !agentExists {
		return "", fmt.Errorf("agent %s: %w", a.AgentID, errAgentMissing)
	}
	req, err := s.createRequest(ctx, tx, a, runID, event, allowlist)
	if err != nil {
		return "", err
	}
	m, err := missions.ResolveDefaults(ctx, req, s.resolve)
	if err != nil {
		return "", fmt.Errorf("resolve mission: %w", err)
	}
	if err := missions.CheckToolAllowlistHarness(m); err != nil {
		return "", err
	}
	id, err := s.create(ctx, m)
	if err != nil {
		return "", fmt.Errorf("create mission: %w", err)
	}
	return id, nil
}

// createRequest maps a's mission action onto the run's CreateRequest.
// Goal and name are interpolated before any source is attached, so
// outcome text is never interpolated. Sources are the parent lineage
// (continuity), the trigger, the notes, then the action's attachments.
// allowlist is the firing trigger's tool_allowlist (runToolAllowlist),
// resolved by the caller so a gone trigger is caught before any of this
// runs.
func (s *Starter) createRequest(ctx context.Context, tx pgx.Tx, a Automation, runID string, event map[string]any, allowlist []string) (missions.CreateRequest, error) {
	var notes []Note
	if a.NotesEnabled {
		var err error
		if notes, err = listNotes(ctx, tx, a.ID); err != nil {
			return missions.CreateRequest{}, err
		}
	}
	tmpl := *a.Action.Mission
	byName := noteMap(notes)
	tmpl.Goal = Interpolate(tmpl.Goal, event, byName)
	tmpl.Name = Interpolate(tmpl.Name, event, byName)
	req := missions.TemplateCreateRequest(tmpl, a.Name, a.AgentID, s.filterDestinationIDs(ctx, tmpl.DestinationIDs), runID)
	req.ChannelConversationID = eventConversationID(event)

	var sources []missions.SourceEntry
	if a.Continuity {
		parentID, lineage, err := s.previousLineage(ctx, tx, a.ID, runID)
		if err != nil {
			return missions.CreateRequest{}, err
		}
		if parentID != "" {
			req.ParentMissionID = parentID
			sources = append(sources, lineage)
		}
	}
	if e, ok := triggerSource(event); ok {
		sources = append(sources, e)
	}
	if e, ok := notesSource(notes); ok {
		sources = append(sources, e)
	}
	if len(allowlist) > 0 {
		var agentTools []string
		if s.resolve.Agent != nil {
			if d, ok := s.resolve.Agent(ctx, a.AgentID); ok {
				agentTools = d.Tools
			}
		}
		req.ToolAllowlist = intersectToolAllowlist(allowlist, agentTools)
		if len(req.ToolAllowlist) == 0 {
			s.log.Info("automations: trigger tool_allowlist shares no tool with the agent, run gets no tools", "automation_id", a.ID, "run_id", runID)
			req.ToolAllowlist = []string{noToolsAllowlist}
		}
	}
	req.Sources = append(sources, req.Sources...)
	return req, nil
}

// eventConversationID is the channel conversation a channel run came
// from, "" for every other run. Its mission reports the outcome there.
func eventConversationID(event map[string]any) string {
	if event["kind"] != events.KindChannelMessage {
		return ""
	}
	conv, _ := event["conversation_id"].(string)
	return conv
}

// noToolsAllowlist is a mission allowlist that offers no tool beyond
// the always-offered sentinels.
const noToolsAllowlist = "mission_status"

// runToolAllowlist returns the tool_allowlist of the trigger that fired
// runID: its own trigger, or the automation's manual trigger for a
// run.now. Wraps errTriggerGone when a trigger the run needed no
// longer exists (see triggerAllowlist).
func runToolAllowlist(ctx context.Context, tx pgx.Tx, a Automation, runID string, event map[string]any) ([]string, error) {
	var triggerID *string
	if err := tx.QueryRow(ctx, `SELECT trigger_id::text FROM automation_runs WHERE id = $1`, runID).Scan(&triggerID); err != nil {
		return nil, fmt.Errorf("automations start: run trigger: %w", err)
	}
	return triggerAllowlist(a.Triggers, triggerID, event["kind"])
}

// triggerAllowlist is runToolAllowlist's pure decision: triggerID nil
// with a run.now event reads the automation's manual trigger (nil when
// it has none, or the automation carries none at all: a run.now needs
// no manual trigger row to fire); triggerID nil with any OTHER named
// event kind means trigger_id went NULL on a run that needed a real
// trigger (fire() always records one for cron/webhook/channel/connector
// events), so it wraps errTriggerGone. An absent event kind (a run row
// with no event at all, e.g. a crash-recovery row inserted directly
// rather than through fire()) carries no evidence either way and stays
// legacy-compatible: nil, nil. triggerID set names a trigger that no
// longer matches any of triggers, also errTriggerGone.
func triggerAllowlist(triggers []Trigger, triggerID *string, eventKind any) ([]string, error) {
	if triggerID == nil {
		switch eventKind {
		case events.KindRunNow:
			for _, t := range triggers {
				if t.Kind == TriggerManual {
					return t.ToolAllowlist, nil
				}
			}
			return nil, nil
		case nil:
			return nil, nil
		default:
			return nil, errTriggerGone
		}
	}
	for _, t := range triggers {
		if t.ID == *triggerID {
			return t.ToolAllowlist, nil
		}
	}
	return nil, errTriggerGone
}

// intersectToolAllowlist keeps the trigger entries the agent's Tools
// grant: an entry survives when some agent tool equals or matches it
// (tools.ToolMatches). Agent Tools are the ceiling, except for the
// note tools, which the automation grants itself. A builtin the agent
// lacks is dropped even though an unrestricted mission on the same
// agent gets every builtin (only a trigger's allowlist narrows; the
// agent's Tools are never widened by it). A delegated coding harness
// needs both shell and write_file present in the agent's Tools AND
// surviving this intersection, or CheckToolAllowlistHarness rejects the
// run at create.
func intersectToolAllowlist(trigger, agentTools []string) []string {
	var out []string
	for _, entry := range trigger {
		if entry == readNoteToolName || entry == writeNoteToolName ||
			slices.ContainsFunc(agentTools, func(tool string) bool { return tools.ToolMatches(tool, entry) }) {
			out = append(out, entry)
		}
	}
	return out
}

// previousLineage finds the mission of automationID's newest finished
// run other than runID and returns its lineage source. "" when there is
// none, continuity is unwired or that mission was deleted.
func (s *Starter) previousLineage(ctx context.Context, tx pgx.Tx, automationID, runID string) (string, missions.SourceEntry, error) {
	if s.lineage == nil {
		return "", missions.SourceEntry{}, nil
	}
	var missionID string
	err := tx.QueryRow(ctx, `SELECT mission_id FROM automation_runs
		WHERE automation_id = $1 AND id <> $2 AND status IN ('done', 'failed') AND mission_id IS NOT NULL
		ORDER BY created_at DESC, id DESC LIMIT 1`, automationID, runID).Scan(&missionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", missions.SourceEntry{}, nil
	}
	if err != nil {
		return "", missions.SourceEntry{}, fmt.Errorf("automations start: previous run: %w", err)
	}
	src, err := s.lineage(ctx, missionID)
	if errors.Is(err, missions.ErrNotFound) {
		s.log.Info("automations: previous mission is gone, starting without continuity", "automation_id", automationID, "run_id", runID, "mission_id", missionID)
		return "", missions.SourceEntry{}, nil
	}
	if err != nil {
		return "", missions.SourceEntry{}, fmt.Errorf("automations start: previous mission lineage: %w", err)
	}
	return missionID, src, nil
}

// mergeRunEvent merges patch into a run's event.
func mergeRunEvent(ctx context.Context, tx pgx.Tx, runID string, patch map[string]any) error {
	raw, _ := json.Marshal(patch)
	if _, err := tx.Exec(ctx, `UPDATE automation_runs SET event = event || $2::jsonb WHERE id = $1`, runID, raw); err != nil {
		return fmt.Errorf("automations start: run event: %w", err)
	}
	return nil
}

// filterDestinationIDs drops destination ids that are unknown or
// disabled at start time; they never block the run.
func (s *Starter) filterDestinationIDs(ctx context.Context, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	if s.destinationEnabled == nil {
		s.log.Warn("automations: dropping destination_ids, destinations are not enabled", "destination_ids", ids)
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		ok, err := s.destinationEnabled(ctx, id)
		if err != nil {
			s.log.Warn("automations: dropping destination id, lookup failed", "destination_id", id, "error", err)
			continue
		}
		if !ok {
			s.log.Warn("automations: dropping destination id, unknown or disabled", "destination_id", id)
			continue
		}
		out = append(out, id)
	}
	return out
}
