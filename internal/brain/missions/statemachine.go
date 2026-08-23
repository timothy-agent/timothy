package missions

// Phase is where a mission sits in its fixed pipeline.
type Phase string

const (
	PhaseExplore Phase = "explore"
	PhasePlan    Phase = "plan"
	PhaseExecute Phase = "execute"
	PhaseReview  Phase = "review"
	PhaseDone    Phase = "done"
	PhaseFailed  Phase = "failed"
)

// phaseOrder is the fixed pipeline a mission advances through.
var phaseOrder = []Phase{PhaseExplore, PhasePlan, PhaseExecute, PhaseReview}

// Terminal reports whether phase ends the mission.
func (p Phase) Terminal() bool {
	return p == PhaseDone || p == PhaseFailed
}

// nextPhase returns what phase follows p in the fixed pipeline, and
// whether p has a successor (PhaseReview's success case is handled by
// the caller, since it depends on whether the reviewed unit was last).
func nextPhase(p Phase) (Phase, bool) {
	for i, cur := range phaseOrder {
		if cur == p && i+1 < len(phaseOrder) {
			return phaseOrder[i+1], true
		}
	}
	return "", false
}

// parsePhase never rejects: an unrecognized value degrades to a safe
// fallback rather than the caller treating the row as unreadable —
// corruption-safety over strictness (a future rollback that doesn't
// recognize a newer phase must still load the mission as paused, not
// fail to load it at all).
func parsePhase(raw string) (Phase, bool) {
	switch Phase(raw) {
	case PhaseExplore, PhasePlan, PhaseExecute, PhaseReview, PhaseDone, PhaseFailed:
		return Phase(raw), true
	default:
		return "", false
	}
}

// Status is orthogonal to Phase: what the mission is doing right now.
type Status string

const (
	StatusIdle            Status = "idle"
	StatusWorking         Status = "working"
	StatusWaitingForInput Status = "waiting_for_input"
	StatusPaused          Status = "paused"
	StatusDone            Status = "done"
	StatusError           Status = "error"
)

func parseStatus(raw string) (Status, bool) {
	switch Status(raw) {
	case StatusIdle, StatusWorking, StatusWaitingForInput, StatusPaused, StatusDone, StatusError:
		return Status(raw), true
	default:
		return "", false
	}
}

// PauseReason names WHY a mission paused, so notifications and resume
// logic can act on the reason rather than just "it's paused."
type PauseReason string

const (
	PauseBackoff    PauseReason = "backoff"     // consecutive worker failures
	PauseNoProgress PauseReason = "no_progress" // stall: identical reviewer rejection twice
	PauseInfra      PauseReason = "infra"       // harness/reviewer/driver error
	PauseBudget     PauseReason = "budget"      // spend >= cap
	// PauseMixedCurrency fires when the mission has recorded spend in a
	// currency other than its own budget currency AND the driver has no
	// usable stored fx rate to convert that amount (toStepState) —
	// comparing across currencies without one would require a guessed
	// rate, which this codebase never does (D-013's sibling invariant
	// for spend, not just price). A convertible other-currency amount
	// is folded into Spent instead and never reaches this pause.
	PauseMixedCurrency PauseReason = "mixed_currency"
)

// Input is one event the driver feeds into Step.
type Input string

const (
	InputPhaseComplete      Input = "phase_complete"
	InputWorkerRetry        Input = "worker_retry"
	InputWorkerBlocked      Input = "worker_blocked"
	InputWorkerFailed       Input = "worker_failed"
	InputReviewApprove      Input = "review_approve"
	InputReviewRework       Input = "review_rework"
	InputReviewInfraFailure Input = "review_infra_failure"
	InputResume             Input = "resume"
	InputCancel             Input = "cancel"
)

// StepState is the state-machine-relevant slice of a Mission — Step
// takes and returns this, never the full row, so it stays pure and
// trivially table-testable without a Store.
type StepState struct {
	Phase               Phase
	Status              Status
	PauseReason         PauseReason
	Iteration           int
	MaxIterations       int
	ConsecutiveFailures int
	LastGapFingerprint  string
	StallCount          int
	// Spent is this mission's spend in Budget's currency, INCLUDING any
	// other-currency spend the driver could convert via a stored fx
	// rate (toStepState) — 0 if there is no spend at all yet, honest
	// zero, not a guess. MixedCurrencySpend now means "the mission has
	// spend in a currency the driver could NOT convert" (no stored
	// rate for that currency, or the rate itself is stale) — a
	// convertible other-currency amount is folded into Spent instead
	// and never sets this. RateAsOf carries the date of whichever
	// stored rate participated in that conversion, "" when none did
	// (single-currency spend, or spend already recorded directly in
	// Budget's currency) — event payloads and API responses surface it
	// as provenance for the converted number.
	Spent              float64
	Budget             *float64
	MixedCurrencySpend bool
	RateAsOf           string
	// LastUnit reports whether the unit under review is the plan's
	// last unit — PhaseReview's approve transition needs this to
	// decide between advancing to execute (more units left) or done.
	LastUnit bool
	// ReplanUsed reports whether this mission already spent its one
	// automatic replan-on-stall attempt (stepWorkerRetry/
	// stepReviewRework) — a second stall pauses for a human same as
	// before this feature existed.
	ReplanUsed bool
	// Light marks a mission that skips explore/plan/review (D-069) —
	// a stall can never replan into PhasePlan for one, since it never
	// visits that phase.
	Light bool
}

// StepInput bundles the triggering Input with whatever data it
// carries (a reviewer's gap fingerprint, a blocked question, etc.)
type StepInput struct {
	Input          Input
	GapFingerprint string // set on InputReviewRework
	Message        string // set on InputWorkerBlocked / pause explanations
	// Reason is WHY this input happened (a worker's retry analysis, an
	// error string, flattened review findings) — carried into the
	// transition's event payloads so a mission's failure cause is
	// readable from its event log alone, never only from process logs.
	Reason string
}

// EventDraft is one event Step decided must be appended; the Store
// assigns its seq.
type EventDraft struct {
	Kind    string
	Payload map[string]any
}

// Transition is Step's pure output: the new state plus what the
// driver must DO as a result (never itself an I/O operation).
type Transition struct {
	Next   StepState
	Events []EventDraft
}

// Config carries tunables Step needs but that aren't per-mission
// state: the backoff/stall thresholds. Kept separate from StepState so
// tests can vary thresholds without faking a whole mission.
type Config struct {
	BackoffFailures int // consecutive worker_failed inputs before a backoff pause
	StallRounds     int // consecutive identical-fingerprint reworks before a no_progress pause
}

// DefaultConfig matches the reference design's thresholds.
var DefaultConfig = Config{BackoffFailures: 3, StallRounds: 2}

// Step is the pure state machine transition function: no I/O, fully
// deterministic given (state, input). Cross-cutting order, checked
// before any input-specific switch:
//  1. cancel is legal from any non-terminal state, checked FIRST.
//  2. budget check (Spent >= *Budget, or unconvertible spend present —
//     see StepState.MixedCurrencySpend) checked second, before any
//     input-specific handling — an over-budget or unconvertible-spend
//     mission pauses regardless of what input arrived.
//  3. input-specific switch on (Phase, Status, Input).
func Step(s StepState, in StepInput, cfg Config) Transition {
	if in.Input == InputCancel {
		if s.Phase.Terminal() {
			// Cancelling an already-finished mission is a no-op: the
			// state doesn't change, no new event is warranted.
			return Transition{Next: s}
		}
		return Transition{
			Next:   withPhaseFailed(s),
			Events: []EventDraft{{Kind: "mission.failed", Payload: map[string]any{"reason": "cancelled"}}},
		}
	}

	if s.Budget != nil && !s.Phase.Terminal() {
		if s.MixedCurrencySpend {
			return Transition{
				Next: withPause(s, PauseMixedCurrency),
				Events: []EventDraft{{Kind: "mission.paused", Payload: map[string]any{
					"reason": string(PauseMixedCurrency),
					"detail": "spend recorded in a currency with no usable stored exchange rate to convert against the mission's budget",
				}}},
			}
		}
		if s.Spent >= *s.Budget {
			payload := map[string]any{"reason": string(PauseBudget)}
			if s.RateAsOf != "" {
				payload["rate_as_of"] = s.RateAsOf
			}
			return Transition{
				Next:   withPause(s, PauseBudget),
				Events: []EventDraft{{Kind: "mission.paused", Payload: payload}},
			}
		}
	}

	switch in.Input {
	case InputResume:
		return stepResume(s)
	case InputPhaseComplete:
		return stepPhaseComplete(s)
	case InputWorkerRetry:
		return stepWorkerRetry(s, in, cfg)
	case InputWorkerBlocked:
		return Transition{
			Next:   withStatus(s, StatusWaitingForInput),
			Events: []EventDraft{{Kind: "mission.blocked", Payload: map[string]any{"question": in.Message}}},
		}
	case InputWorkerFailed:
		return stepWorkerFailed(s, in, cfg)
	case InputReviewApprove:
		return stepReviewApprove(s)
	case InputReviewRework:
		return stepReviewRework(s, in, cfg)
	case InputReviewInfraFailure:
		return Transition{
			Next:   withPause(s, PauseInfra),
			Events: []EventDraft{{Kind: "mission.paused", Payload: map[string]any{"reason": string(PauseInfra), "detail": in.Reason}}},
		}
	default:
		// Unrecognized input: no-op rather than a panic or a silent
		// wrong transition — the driver logs this as a bug elsewhere.
		return Transition{Next: s}
	}
}

func withStatus(s StepState, status Status) StepState {
	s.Status = status
	return s
}

func withPause(s StepState, reason PauseReason) StepState {
	s.Status = StatusPaused
	s.PauseReason = reason
	return s
}

func withPhaseFailed(s StepState) StepState {
	s.Phase = PhaseFailed
	s.Status = StatusError
	s.PauseReason = "" // terminal: any prior pause reason no longer describes the mission's state
	return s
}

// stepResume clears whatever parked the mission and hands it back to
// the driver as idle (ready to be claimed by the next work-slot
// sweep), preserving iteration/failure counters — resume is not a
// restart.
func stepResume(s StepState) Transition {
	if s.Phase.Terminal() {
		return Transition{Next: s} // nothing to resume
	}
	s.Status = StatusIdle
	s.PauseReason = ""
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.resumed"}}}
}

// stepPhaseComplete advances explore/plan to the next fixed phase.
// execute's completion goes through review (a worker verdict of DONE
// requests review, it doesn't itself complete the phase — the driver
// routes DONE into a review round, whose outcome arrives as
// InputReviewApprove/InputReviewRework, not InputPhaseComplete).
func stepPhaseComplete(s StepState) Transition {
	next, ok := nextPhase(s.Phase)
	if !ok {
		return Transition{Next: s}
	}
	s.Phase = next
	s.Status = StatusIdle
	s.Iteration = 0
	s.ConsecutiveFailures = 0
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.phase_started", Payload: map[string]any{"phase": string(next)}}}}
}

// stepWorkerFailed counts consecutive failures toward the backoff
// brake; a fresh success (any non-failed input) resets the counter
// elsewhere (the driver clears ConsecutiveFailures on progress).
//
// The MaxIterations ceiling is checked before the backoff pause: it is
// a hard stop, and a backoff pause must only ever happen below the
// ceiling. Checking backoff first would let a mission repeatedly
// resumed after backoff pauses exceed its iteration ceiling forever.
func stepWorkerFailed(s StepState, in StepInput, cfg Config) Transition {
	s.ConsecutiveFailures++
	s.Iteration++
	if s.Iteration >= s.MaxIterations {
		return Transition{
			Next:   withPhaseFailed(s),
			Events: []EventDraft{{Kind: "mission.failed", Payload: map[string]any{"reason": "max_iterations", "detail": in.Reason}}},
		}
	}
	if s.ConsecutiveFailures >= cfg.BackoffFailures {
		return Transition{
			Next:   withPause(s, PauseBackoff),
			Events: []EventDraft{{Kind: "mission.paused", Payload: map[string]any{"reason": string(PauseBackoff), "detail": in.Reason}}},
		}
	}
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.retry", Payload: map[string]any{"cause": "worker_failed", "reason": in.Reason}}}}
}

// stepWorkerRetry is a worker's own self-reported RETRY (failure
// analysis, distinct from a harness-detected WorkerFailed): it costs
// an iteration but does not count toward the ConsecutiveFailures
// backoff brake — the worker is still making an attempt, not silently
// failing. It still tracks the stall brake same as stepReviewRework:
// two consecutive rounds with an IDENTICAL gap fingerprint (e.g. the
// same harness verify_cmd failing the same way every time) mean no
// real progress is happening, most likely because the check itself
// can never pass — grinding to max_iterations wastes the rest of the
// budget on a foregone conclusion.
func stepWorkerRetry(s StepState, in StepInput, cfg Config) Transition {
	s.ConsecutiveFailures = 0
	if in.GapFingerprint != "" {
		if in.GapFingerprint == s.LastGapFingerprint {
			s.StallCount++
		} else {
			s.StallCount = 1
		}
		s.LastGapFingerprint = in.GapFingerprint
		// D-069: light missions never visit PhasePlan, so a stall skips
		// the replan/no-progress-pause brake entirely and falls straight
		// through to the plain retry/max_iterations path below, same as
		// stepWorkerFailed's backoff ceiling.
		if s.StallCount >= cfg.StallRounds && !s.Light {
			if !s.ReplanUsed {
				return replanTransition(s, in)
			}
			return Transition{
				Next:   withPause(s, PauseNoProgress),
				Events: []EventDraft{{Kind: "mission.paused", Payload: map[string]any{"reason": string(PauseNoProgress), "detail": in.Reason}}},
			}
		}
	}
	s.Iteration++
	if s.Iteration >= s.MaxIterations {
		return Transition{
			Next:   withPhaseFailed(s),
			Events: []EventDraft{{Kind: "mission.failed", Payload: map[string]any{"reason": "max_iterations", "detail": in.Reason}}},
		}
	}
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.retry", Payload: map[string]any{"cause": "worker_retry", "reason": in.Reason}}}}
}

// replanTransition sends a first-time stall back to planning instead of
// pausing: spends the mission's one replan attempt and resets the
// counters a fresh planning pass needs.
func replanTransition(s StepState, in StepInput) Transition {
	s.ReplanUsed = true
	s.Phase = PhasePlan
	s.Status = StatusIdle
	s.StallCount = 0
	s.LastGapFingerprint = ""
	s.Iteration = 0
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.replan", Payload: map[string]any{"reason": in.Reason}}}}
}

// stepReviewApprove clears the stall counter (progress was made) and
// either moves to the next unit (more execute work) or completes the
// mission, depending on whether the reviewed unit was the plan's last.
func stepReviewApprove(s StepState) Transition {
	s.StallCount = 0
	s.LastGapFingerprint = ""
	if s.LastUnit {
		s.Phase = PhaseDone
		s.Status = StatusDone
		return Transition{Next: s, Events: []EventDraft{{Kind: "mission.done"}}}
	}
	s.Phase = PhaseExecute
	s.Status = StatusIdle
	s.Iteration = 0
	s.ConsecutiveFailures = 0
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.unit_verified", Payload: map[string]any{"passed": true, "check": "review"}}}}
}

// stepReviewRework sends the mission back to execute for another
// attempt, tracking the stall brake: two consecutive rounds with an
// IDENTICAL gap fingerprint mean the reviewer keeps rejecting for the
// same reason — no real progress is happening.
func stepReviewRework(s StepState, in StepInput, cfg Config) Transition {
	if in.GapFingerprint != "" && in.GapFingerprint == s.LastGapFingerprint {
		s.StallCount++
	} else {
		s.StallCount = 1
	}
	s.LastGapFingerprint = in.GapFingerprint
	if s.StallCount >= cfg.StallRounds {
		if !s.ReplanUsed {
			return replanTransition(s, in)
		}
		return Transition{
			Next:   withPause(s, PauseNoProgress),
			Events: []EventDraft{{Kind: "mission.paused", Payload: map[string]any{"reason": string(PauseNoProgress), "detail": in.Reason}}},
		}
	}
	s.Phase = PhaseExecute
	s.Status = StatusIdle
	s.Iteration++
	if s.Iteration >= s.MaxIterations {
		return Transition{
			Next:   withPhaseFailed(s),
			Events: []EventDraft{{Kind: "mission.failed", Payload: map[string]any{"reason": "max_iterations", "detail": in.Reason}}},
		}
	}
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.review_verdict", Payload: map[string]any{"decision": "rework", "reason": in.Reason}}}}
}
