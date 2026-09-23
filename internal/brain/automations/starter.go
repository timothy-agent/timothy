package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
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
	// destinationEnabled re-checks an action's destination ids; nil
	// drops every id.
	destinationEnabled func(ctx context.Context, id string) (bool, error)
	kick               chan struct{}
	log                *slog.Logger
	now                func() time.Time
}

// NewStarter wires the starter. create is Driver.Create and resolve
// the shared create-path lookups.
func NewStarter(store *Store, create func(ctx context.Context, m missions.Mission) (string, error), resolve missions.ResolveDeps, destinationEnabled func(ctx context.Context, id string) (bool, error), log *slog.Logger) *Starter {
	return &Starter{store: store, create: create, resolve: resolve, destinationEnabled: destinationEnabled,
		kick: make(chan struct{}, 1), log: log, now: time.Now}
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
		missionID, createErr = s.createMission(ctx, tx, automationID, runID)
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
	_, disabled, err := finalizeRun(ctx, tx, runID, automationID, "", true, reason, now, s.log)
	if err != nil {
		return err
	}
	if disabled {
		// notifications rows belong to a mission, and this run has none.
		s.log.Warn("automations: disabled with no mission to notify on", "automation_id", automationID, "run_id", runID)
	}
	return nil
}

// createMission builds the run's mission from its automation's action
// and hands it to create.
func (s *Starter) createMission(ctx context.Context, tx pgx.Tx, automationID, runID string) (string, error) {
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
	// Trigger tool_allowlist is not applied yet: missions carry no
	// per-mission allowlist (issue #857).
	req := missions.TemplateCreateRequest(*a.Action.Mission, a.Name, a.AgentID, s.filterDestinationIDs(ctx, a.Action.Mission.DestinationIDs), runID)
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
