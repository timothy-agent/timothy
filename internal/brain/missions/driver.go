package missions

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// driveTimeBound bounds one Drive call: long enough for a real
// multi-iteration mission to make meaningful progress in one process
// lifetime, short enough that a runaway mission doesn't pin a
// goroutine indefinitely across a deploy. A mission that hits the
// bound is left in whatever state the last successful Advance
// persisted (working, most likely) — NOT a dead end, since sweep.go's
// boot-time recoverWorking re-Drives any mission still 'working' when
// a process starts, and the periodic work-slot sweep retries anything
// left idle.
const driveTimeBound = 4 * time.Hour

// notifier is the transition-notification hook Driver calls after
// every successful ApplyTransition; notify.go's Notifier satisfies it
// (added in M3). nil is valid — M2 has no notifications wired yet.
type notifier interface {
	OnTransition(ctx context.Context, missionID string, before, after Status) error
}

// driverStore is the narrow slice of *Store the Driver actually calls
// — kept as an interface so tests can fake it without a real Postgres
// pool.
type driverStore interface {
	Get(ctx context.Context, id string) (Mission, error)
	ApplyTransition(ctx context.Context, id string, t Transition) error
	AppendEvent(ctx context.Context, id, kind string, payload map[string]any) error
	SetSpec(ctx context.Context, id string, spec Spec) error
}

// Driver walks the state machine for one mission: calls Runner for the
// phase-appropriate session type, interprets the outcome into a
// StepInput, calls Step, and persists the Transition via
// Store.ApplyTransition — crash-resumable at every boundary because
// nothing advances in memory only.
type Driver struct {
	store     driverStore
	runner    Runner
	workspace *Workspace
	notify    notifier
	log       *slog.Logger
	cfg       Config

	// gatekeepers holds each mission's in-progress reviewer session
	// state, keyed by mission id, for the "delta recheck" resume on
	// rework. Process-local by design: lost on restart is acceptable —
	// a cold reviewer just re-checks everything from scratch.
	gatekeepers map[string]*GatekeeperState
}

func NewDriver(store driverStore, runner Runner, workspace *Workspace, notify notifier, log *slog.Logger) *Driver {
	return &Driver{
		store: store, runner: runner, workspace: workspace, notify: notify, log: log,
		cfg:         DefaultConfig,
		gatekeepers: map[string]*GatekeeperState{},
	}
}

// Advance performs exactly one worker turn, review round, or planning
// turn for mission id — whatever its current phase calls for — then
// persists the resulting transition and returns whether the mission
// can be Advanced again immediately (false on terminal, paused,
// waiting_for_input, or idle).
func (d *Driver) Advance(ctx context.Context, id string) (canContinue bool, err error) {
	m, err := d.store.Get(ctx, id)
	if err != nil {
		return false, fmt.Errorf("driver advance: %w", err)
	}
	if m.Phase.Terminal() || m.Status == StatusPaused || m.Status == StatusWaitingForInput {
		return false, nil
	}

	before := m.Status
	in, err := d.runPhase(ctx, m)
	if err != nil {
		d.log.Error("driver: phase run failed", "mission_id", id, "phase", m.Phase, "error", err)
		in = StepInput{Input: InputReviewInfraFailure}
		if m.Phase != PhaseReview {
			// review_infra_failure is review's error input; other phases
			// report the equivalent-shaped worker_failed so the same
			// backoff/pause machinery applies uniformly regardless of
			// which phase actually failed.
			in = StepInput{Input: InputWorkerFailed}
		}
	}

	state := toStepState(m)
	t := Step(state, in, d.cfg)
	if err := d.store.ApplyTransition(ctx, id, t); err != nil {
		return false, fmt.Errorf("driver advance: apply transition: %w", err)
	}
	if t.Next.Phase.Terminal() {
		delete(d.gatekeepers, id)
	}
	if d.notify != nil {
		if err := d.notify.OnTransition(ctx, id, before, t.Next.Status); err != nil {
			d.log.Warn("driver: notify failed", "mission_id", id, "error", err)
		}
	}
	return t.Next.Status == StatusIdle || t.Next.Status == StatusWorking, nil
}

// Drive loops Advance until it returns false (terminal, paused,
// waiting_for_input, or idle), bounded by driveTimeBound. Crash-
// resumable at every boundary: each Advance call persists before the
// next begins, so a process death mid-Drive leaves the mission in a
// valid, resumable state rather than a torn one.
func (d *Driver) Drive(ctx context.Context, id string) error {
	dctx, cancel := context.WithTimeout(ctx, driveTimeBound)
	defer cancel()
	for {
		canContinue, err := d.Advance(dctx, id)
		if err != nil {
			return err
		}
		if !canContinue {
			return nil
		}
		if dctx.Err() != nil {
			return nil
		}
	}
}

// toStepState projects a Mission onto the state machine's input shape.
func toStepState(m Mission) StepState {
	spent := 0.0 // Phase 1 has no spend-tracking wired into StepState yet beyond BudgetUSD's presence; see M3/M4 for ledger integration.
	return StepState{
		Phase: m.Phase, Status: m.Status, PauseReason: m.PauseReason,
		Iteration: m.Iteration, MaxIterations: m.MaxIterations,
		ConsecutiveFailures: m.ConsecutiveFailures, LastGapFingerprint: m.LastGapFingerprint,
		StallCount: m.StallCount, SpentUSD: spent, BudgetUSD: m.BudgetUSD,
		LastUnit: isLastUnit(m.Spec),
	}
}

func isLastUnit(spec Spec) bool {
	for i, u := range spec.Units {
		if !u.Passes {
			return i == len(spec.Units)-1
		}
	}
	return true // no unverified units left
}

// runPhase runs the phase-appropriate session and returns the StepInput
// its outcome maps to. It does not itself decide pass/fail semantics
// beyond what each phase's contract already defines (worker sentinel,
// review verdict, planner output).
func (d *Driver) runPhase(ctx context.Context, m Mission) (StepInput, error) {
	switch m.Phase {
	case PhaseResearch:
		return d.runResearch(ctx, m)
	case PhasePlan:
		return d.runPlan(ctx, m)
	case PhaseExecute:
		return d.runExecute(ctx, m)
	case PhaseReview:
		return d.runReview(ctx, m)
	default:
		return StepInput{}, fmt.Errorf("driver: mission %s in unhandled phase %q", m.ID, m.Phase)
	}
}

// runResearch is a placeholder single-turn phase in Phase 1: it
// completes immediately, carrying no findings forward beyond what the
// plan phase's own packet re-derives from the goal. A real research
// loop (multi-turn, tool-using) is out of scope for this milestone.
func (d *Driver) runResearch(ctx context.Context, m Mission) (StepInput, error) {
	return StepInput{Input: InputPhaseComplete}, nil
}

func (d *Driver) runPlan(ctx context.Context, m Mission) (StepInput, error) {
	spec, err := d.runner.PlanSession(ctx, m, "")
	if err != nil {
		return StepInput{}, err
	}
	if err := d.store.AppendEvent(ctx, m.ID, "mission.plan_created", map[string]any{"units": len(spec.Units)}); err != nil {
		return StepInput{}, fmt.Errorf("driver: record plan: %w", err)
	}
	if err := d.store.SetSpec(ctx, m.ID, spec); err != nil {
		return StepInput{}, err
	}
	return StepInput{Input: InputPhaseComplete}, nil
}

func (d *Driver) runExecute(ctx context.Context, m Mission) (StepInput, error) {
	packet, err := d.packet(ctx, m)
	if err != nil {
		return StepInput{}, err
	}
	verdict, text, err := d.runner.RunWorker(ctx, m, packet)
	if err != nil {
		return StepInput{}, err
	}
	if err := d.recordProgress(ctx, m.ID, text); err != nil {
		d.log.Warn("driver: record progress failed", "mission_id", m.ID, "error", err)
	}

	switch verdict.Outcome {
	case "done":
		if m.Worktree != "" {
			if err := d.workspace.CommitUnit(ctx, m.Worktree, "mission "+m.ID+" iteration "+fmt.Sprint(m.Iteration)); err != nil {
				d.log.Warn("driver: commit unit failed", "mission_id", m.ID, "error", err)
			}
		}
		return StepInput{Input: InputPhaseComplete}, nil
	case "blocked":
		return StepInput{Input: InputWorkerBlocked, Message: verdict.Question}, nil
	default: // "retry" or anything unrecognized
		if m.Worktree != "" {
			if err := d.workspace.Rollback(ctx, m.Worktree, m.Kind); err != nil {
				d.log.Warn("driver: rollback failed", "mission_id", m.ID, "error", err)
			}
		}
		return StepInput{Input: InputWorkerRetry}, nil
	}
}

func (d *Driver) runReview(ctx context.Context, m Mission) (StepInput, error) {
	var diff string
	if m.Worktree != "" {
		var err error
		diff, err = BaselineDiff(ctx, m.Worktree, m.BaseCommit)
		if err != nil {
			return StepInput{}, err
		}
	}
	gk := d.gatekeepers[m.ID]
	verdict, nextGK, err := d.runner.RunReview(ctx, m, diff, gk)
	if err != nil {
		return StepInput{}, err
	}
	d.gatekeepers[m.ID] = nextGK
	// The reviewer's own worktree side effects (it may run tests) are
	// rolled back unconditionally after every review round.
	if m.Worktree != "" {
		if err := d.workspace.Rollback(ctx, m.Worktree, m.Kind); err != nil {
			d.log.Warn("driver: post-review rollback failed", "mission_id", m.ID, "error", err)
		}
	}

	if verdict.Approved {
		if err := d.verifyCurrentUnit(ctx, m); err != nil {
			return StepInput{Input: InputReviewInfraFailure}, err
		}
		return StepInput{Input: InputReviewApprove}, nil
	}
	fp := GapFingerprint(verdict.Findings)
	if err := d.store.AppendEvent(ctx, m.ID, "mission.review_verdict", map[string]any{
		"decision": "rework", "findings": verdict.Findings,
	}); err != nil {
		d.log.Warn("driver: record review verdict failed", "mission_id", m.ID, "error", err)
	}
	return StepInput{Input: InputReviewRework, GapFingerprint: fp}, nil
}

// verifyCurrentUnit runs RunVerify for the plan's current (first
// unverified) unit and marks it passed — only this harness-run
// evidence may flip a unit's Passes flag, never model output.
func (d *Driver) verifyCurrentUnit(ctx context.Context, m Mission) error {
	for i, u := range m.Spec.Units {
		if u.Passes {
			continue
		}
		if u.VerifyCmd == "" {
			return d.markUnitPassed(ctx, m, i)
		}
		workRoot := m.Worktree
		if workRoot == "" {
			workRoot = m.Workspace
		}
		res, err := RunVerify(ctx, workRoot, u.VerifyCmd)
		if err != nil {
			return fmt.Errorf("driver: verify unit %d: %w", i, err)
		}
		if err := d.store.AppendEvent(ctx, m.ID, "mission.unit_verified", map[string]any{
			"unit": i, "passed": res.Passed, "exit_code": res.ExitCode, "output_sha256": res.OutputSHA256,
		}); err != nil {
			d.log.Warn("driver: record verify failed", "mission_id", m.ID, "error", err)
		}
		if !res.Passed {
			return fmt.Errorf("driver: unit %d verify_cmd failed with exit %d", i, res.ExitCode)
		}
		return d.markUnitPassed(ctx, m, i)
	}
	return nil
}

func (d *Driver) markUnitPassed(ctx context.Context, m Mission, unit int) error {
	m.Spec.Units[unit].Passes = true
	return d.store.SetSpec(ctx, m.ID, m.Spec)
}

// packet builds the WorkPacket for the current phase/iteration
// directly from mission fields — no unused indirection.
func (d *Driver) packet(ctx context.Context, m Mission) (WorkPacket, error) {
	gitLog := ""
	if m.Worktree != "" {
		gitLog, _ = gitLogSince(ctx, m.Worktree, m.BaseCommit)
	}
	return WorkPacket{
		Goal: m.Goal, Kind: m.Kind, Spec: m.Spec, Progress: m.Progress,
		GitLog: gitLog, Iteration: m.Iteration,
	}, nil
}

func (d *Driver) recordProgress(ctx context.Context, id, text string) error {
	if text == "" {
		return nil
	}
	return d.store.AppendEvent(ctx, id, "mission.progress", map[string]any{"note": NeutralizeSlot(truncate(text, 2000))})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// gitLogSince returns a capped, one-line-per-commit log of everything
// committed since baseCommit — a fresh worker's window into what prior
// iterations actually did, bounded so it never balloons the packet.
func gitLogSince(ctx context.Context, worktree, baseCommit string) (string, error) {
	if baseCommit == "" || baseCommit == unavailableCommit {
		return "", nil
	}
	cctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	out, err := runGit(cctx, worktree, "log", "--oneline", baseCommit+"..HEAD")
	if err != nil {
		return "", err
	}
	if len(out) > gitLogCap {
		out = out[:gitLogCap] + "…"
	}
	return out, nil
}
