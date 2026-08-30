package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Sentinel errors the HTTP layer maps onto status codes, mirroring
// missions.ErrNotFound / ErrTerminal.
var (
	ErrNotFound  = errors.New("not found")
	ErrDuplicate = errors.New("a workflow with this name already exists")
)

// Workflow is the API/DB shape of one workflows row.
type Workflow struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Definition json.RawMessage `json:"definition"`
	Enabled    bool            `json:"enabled"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// Run is the API/DB shape of one workflow_runs row.
type Run struct {
	ID          string            `json:"id"`
	WorkflowID  string            `json:"workflow_id"`
	Status      string            `json:"status"` // running | paused | done | failed | cancelled
	CurrentStep string            `json:"current_step"`
	Context     map[string]string `json:"context"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// RunEvent is one row from workflow_run_events. PRIMARY KEY is
// (run_id, seq), no surrogate id column.
type RunEvent struct {
	RunID     string          `json:"run_id"`
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// Store is workflows' + workflow_runs' + workflow_run_events' Postgres
// access.
type Store struct {
	db  *pgpool.Pool
	log *slog.Logger
}

func NewStore(db *pgpool.Pool, log *slog.Logger) *Store {
	return &Store{db: db, log: log}
}

const workflowColumns = `id, name, definition, enabled, created_at, updated_at`

func scanWorkflow(row pgx.Row) (Workflow, error) {
	var w Workflow
	if err := row.Scan(&w.ID, &w.Name, &w.Definition, &w.Enabled, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return Workflow{}, err
	}
	return w, nil
}

// Create validates the definition and inserts a workflow row.
func (s *Store) Create(ctx context.Context, name string, definition json.RawMessage) (string, error) {
	if _, err := ParseDefinition(definition); err != nil {
		return "", err
	}
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("workflows create: %w", err)
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO workflows (name, definition) VALUES ($1, $2) RETURNING id`,
		name, definition).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return "", ErrDuplicate
		}
		return "", fmt.Errorf("workflows create: %w", err)
	}
	return id, nil
}

// Get returns one workflow by id.
func (s *Store) Get(ctx context.Context, id string) (Workflow, error) {
	db, err := s.db.Get()
	if err != nil {
		return Workflow{}, fmt.Errorf("workflows get: %w", err)
	}
	w, err := scanWorkflow(db.QueryRow(ctx, `SELECT `+workflowColumns+` FROM workflows WHERE id = $1`, id))
	if err != nil {
		return Workflow{}, fmt.Errorf("workflow %s: %w", id, ErrNotFound)
	}
	return w, nil
}

// List returns every workflow, newest first.
func (s *Store) List(ctx context.Context) ([]Workflow, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("workflows list: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT `+workflowColumns+` FROM workflows ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("workflows list: %w", err)
	}
	defer rows.Close()
	out := []Workflow{}
	for rows.Next() {
		w, err := scanWorkflow(rows)
		if err != nil {
			return nil, fmt.Errorf("workflows list: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Update validates the new definition and replaces it plus enabled.
func (s *Store) Update(ctx context.Context, id string, definition json.RawMessage, enabled bool) error {
	if _, err := ParseDefinition(definition); err != nil {
		return err
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("workflows update: %w", err)
	}
	tag, err := db.Exec(ctx, `UPDATE workflows SET definition = $2, enabled = $3, updated_at = now() WHERE id = $1`,
		id, definition, enabled)
	if err != nil {
		return fmt.Errorf("workflows update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("workflow %s: %w", id, ErrNotFound)
	}
	return nil
}

// SetEnabled flips a workflow's enabled flag without touching its
// definition — a disabled workflow's StartRun is refused by the API
// layer, existing runs are unaffected.
func (s *Store) SetEnabled(ctx context.Context, id string, enabled bool) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("workflows set enabled: %w", err)
	}
	tag, err := db.Exec(ctx, `UPDATE workflows SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
	if err != nil {
		return fmt.Errorf("workflows set enabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("workflow %s: %w", id, ErrNotFound)
	}
	return nil
}

const runColumns = `id, workflow_id, status, current_step, context, created_at, updated_at`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var contextRaw []byte
	if err := row.Scan(&r.ID, &r.WorkflowID, &r.Status, &r.CurrentStep, &contextRaw, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Run{}, err
	}
	_ = json.Unmarshal(contextRaw, &r.Context)
	if r.Context == nil {
		r.Context = map[string]string{}
	}
	return r, nil
}

// CreateRun inserts a run row in status=running at currentStep, with
// the given initial context.
func (s *Store) CreateRun(ctx context.Context, workflowID, currentStep string, runContext map[string]string) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("workflows create run: %w", err)
	}
	if runContext == nil {
		runContext = map[string]string{}
	}
	contextJSON, err := json.Marshal(runContext)
	if err != nil {
		return "", fmt.Errorf("workflows create run: %w", err)
	}
	var id string
	err = db.QueryRow(ctx, `INSERT INTO workflow_runs (workflow_id, current_step, context) VALUES ($1, $2, $3) RETURNING id`,
		workflowID, currentStep, contextJSON).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("workflows create run: %w", err)
	}
	return id, nil
}

// GetRun returns one run by id.
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	db, err := s.db.Get()
	if err != nil {
		return Run{}, fmt.Errorf("workflows get run: %w", err)
	}
	r, err := scanRun(db.QueryRow(ctx, `SELECT `+runColumns+` FROM workflow_runs WHERE id = $1`, id))
	if err != nil {
		return Run{}, fmt.Errorf("workflow run %s: %w", id, ErrNotFound)
	}
	return r, nil
}

// ListRuns returns runs for workflowID, newest first. Empty workflowID
// lists every run.
func (s *Store) ListRuns(ctx context.Context, workflowID string) ([]Run, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("workflows list runs: %w", err)
	}
	query := `SELECT ` + runColumns + ` FROM workflow_runs`
	var args []any
	if workflowID != "" {
		query += ` WHERE workflow_id = $1`
		args = append(args, workflowID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("workflows list runs: %w", err)
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("workflows list runs: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunTransition is the ApplyRunTransition writer's input: the run's new
// status/current_step plus any events to append in the same
// transaction — same shape as missions.Transition.
type RunTransition struct {
	Status      string
	CurrentStep string
	Events      []RunTransitionEvent
}

type RunTransitionEvent struct {
	Kind    string
	Payload map[string]any
}

// ApplyRunTransition persists a RunTransition atomically: updates the
// run row and appends its events in order, all in one txn — the ONLY
// way a run's status/current_step changes after CreateRun, mirroring
// missions.Store.ApplyTransition's discipline.
func (s *Store) ApplyRunTransition(ctx context.Context, id string, t RunTransition) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("workflows apply run transition: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("workflows apply run transition begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM workflow_runs WHERE id = $1 FOR UPDATE`, id).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("workflow run %s: %w", id, ErrNotFound)
		}
		return fmt.Errorf("workflows apply run transition lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET status = $2, current_step = $3, updated_at = now() WHERE id = $1`,
		id, t.Status, t.CurrentStep); err != nil {
		return fmt.Errorf("workflows apply run transition update: %w", err)
	}
	for _, ev := range t.Events {
		if err := appendRunEventTx(ctx, tx, id, ev.Kind, ev.Payload); err != nil {
			return fmt.Errorf("workflows apply run transition event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("workflows apply run transition commit: %w", err)
	}
	return nil
}

// AppendRunEvent assigns seq under SELECT ... FOR UPDATE on the run row
// (serializes appends per-run only) and inserts, outside of
// ApplyRunTransition — for callers that need to log something without a
// status change.
func (s *Store) AppendRunEvent(ctx context.Context, id, kind string, payload map[string]any) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("workflows append run event: %w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("workflows append run event begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := appendRunEventTx(ctx, tx, id, kind, payload); err != nil {
		return fmt.Errorf("workflows append run event: %w", err)
	}
	return tx.Commit(ctx)
}

// appendRunEventTx does the actual locked-seq insert within an
// already-open transaction, shared by ApplyRunTransition and
// AppendRunEvent — copies missions/store.go's appendEventTx pattern.
// Append-only: never updated or deleted.
func appendRunEventTx(ctx context.Context, tx pgx.Tx, runID, kind string, payload map[string]any) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM workflow_runs WHERE id = $1 FOR UPDATE`, runID).Scan(&exists); err != nil {
		return fmt.Errorf("lock run %s: %w", runID, err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO workflow_run_events (run_id, seq, kind, payload)
		 SELECT $1, COALESCE(MAX(seq), 0) + 1, $2, $3 FROM workflow_run_events WHERE run_id = $1`,
		runID, kind, data,
	); err != nil {
		return fmt.Errorf("insert run event: %w", err)
	}
	return nil
}

// RunEvents returns a run's full event log in seq order.
func (s *Store) RunEvents(ctx context.Context, runID string) ([]RunEvent, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("workflows run events: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT run_id, seq, kind, payload, created_at
		FROM workflow_run_events WHERE run_id = $1 ORDER BY seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("workflows run events: %w", err)
	}
	defer rows.Close()
	out := []RunEvent{}
	for rows.Next() {
		var e RunEvent
		if err := rows.Scan(&e.RunID, &e.Seq, &e.Kind, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("workflows run events: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountEdgeFirings counts how many times an edge (identified by its
// from-step + on-event + to-step) has previously fired for run —
// Engine's per-edge iteration-cap input. Counts "edge.taken" run events
// matching all three fields.
func (s *Store) CountEdgeFirings(ctx context.Context, runID, from, on, to string) (int, error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, fmt.Errorf("workflows count edge firings: %w", err)
	}
	var n int
	err = db.QueryRow(ctx, `SELECT count(*) FROM workflow_run_events
		WHERE run_id = $1 AND kind = 'edge.taken'
		AND payload->>'from' = $2 AND payload->>'on' = $3 AND payload->>'to' = $4`,
		runID, from, on, to).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("workflows count edge firings: %w", err)
	}
	return n, nil
}

// isUniqueViolation reports whether err is Postgres's unique_violation
// (23505) — workflows.name's UNIQUE constraint. Mirrors
// missions/schedules.go's isUniqueViolation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
