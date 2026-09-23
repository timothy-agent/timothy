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

	SkipRouteUnusable = "route_unusable"
	SkipCreateFailed  = "create_failed"
)

var errAgentMissing = errors.New("automation agent does not exist")

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
	if routeErr != nil {
		reason = SkipRouteUnusable
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

// createMission builds the run's mission from its automation's action
// and hands it to create.
func (s *Starter) createMission(ctx context.Context, tx pgx.Tx, automationID, runID string, rawEvent []byte) (string, error) {
	a, err := getAutomation(ctx, tx, automationID, "")
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
	req, err := s.createRequest(ctx, tx, a, runID, runEvent(rawEvent))
	if err != nil {
		return "", err
	}
	m, err := missions.ResolveDefaults(ctx, req, s.resolve)
	if err != nil {
		return "", fmt.Errorf("resolve mission: %w", err)
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
func (s *Starter) createRequest(ctx context.Context, tx pgx.Tx, a Automation, runID string, event map[string]any) (missions.CreateRequest, error) {
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
	allowlist, err := runToolAllowlist(ctx, tx, a, runID, event)
	if err != nil {
		return missions.CreateRequest{}, err
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

// noToolsAllowlist is a mission allowlist that offers no tool beyond
// the always-offered sentinels.
const noToolsAllowlist = "mission_status"

// runToolAllowlist returns the tool_allowlist of the trigger that fired
// runID: its own trigger, or the automation's manual trigger for a
// run.now. nil when that trigger has none or is gone.
func runToolAllowlist(ctx context.Context, tx pgx.Tx, a Automation, runID string, event map[string]any) ([]string, error) {
	var triggerID *string
	if err := tx.QueryRow(ctx, `SELECT trigger_id::text FROM automation_runs WHERE id = $1`, runID).Scan(&triggerID); err != nil {
		return nil, fmt.Errorf("automations start: run trigger: %w", err)
	}
	for _, t := range a.Triggers {
		if triggerID != nil && t.ID == *triggerID ||
			triggerID == nil && event["kind"] == events.KindRunNow && t.Kind == TriggerManual {
			return t.ToolAllowlist, nil
		}
	}
	return nil, nil
}

// intersectToolAllowlist keeps the trigger entries the agent's Tools
// grant: an entry survives when some agent tool equals or matches it
// (tools.ToolMatches). Agent Tools are the ceiling, except for the
// note tools, which the automation grants itself.
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
