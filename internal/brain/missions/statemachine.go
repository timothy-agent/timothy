package missions

// Phase is where a mission sits in its fixed pipeline.
type Phase string

const (
	PhaseDiscover Phase = "discover"
	PhasePlan     Phase = "plan"
	PhaseGenerate Phase = "generate"
	PhaseProve    Phase = "prove"
	// PhaseResult runs deterministic delivery/copy/promote/on_complete
	// work with zero LLM turns (slice 1 of the phase redesign): the last
	// stop before done, so a delivery failure parks the mission
	// observably instead of being lost on the old terminal transition.
	PhaseResult Phase = "result"
	PhaseDone   Phase = "done"
	PhaseFailed Phase = "failed"
)

// phaseOrder is the fixed pipeline stepPhaseComplete walks: prove is
// its last entry since a mission never reaches result via
// InputPhaseComplete, only via stepReviewApprove/trySkipReview once
// the plan's last unit is verified (reviewSkippedOrProvePassTransition).
var phaseOrder = []Phase{PhaseDiscover, PhasePlan, PhaseGenerate, PhaseProve}

// Terminal reports whether phase ends the mission.
func (p Phase) Terminal() bool {
	return p == PhaseDone || p == PhaseFailed
}

// nextPhase returns what phase follows p in the fixed pipeline, and
// whether p has a successor (PhaseProve's success case is handled by
// the caller, since it depends on whether the reviewed unit was last).
func nextPhase(p Phase) (Phase, bool) {
	for i, cur := range phaseOrder {
		if cur == p && i+1 < len(phaseOrder) {
			return phaseOrder[i+1], true
		}
	}
	return "", false
}

// parsePhase reports whether raw is a recognized Phase; on an
// unrecognized value it returns ("", false) and leaves degrading to
// the caller (e.g. scanMission treats it as failed; ApplyTransition's
// terminal re-check treats it as terminal) — corruption-safety over
// strictness, each call site choosing the safe fallback for its own
// context.
//
// Legacy mapping (slice 1 of the phase redesign): explore/execute/
// review are the pre-rename phase names, still readable from rows a
// data migration hasn't touched yet or from historical mission_events
// payloads after a rollback. Drop this branch once the migration in
// scripts/pending-alters.md has run everywhere and the first stable
// release ships.
func parsePhase(raw string) (Phase, bool) {
	switch Phase(raw) {
	case PhaseDiscover, PhasePlan, PhaseGenerate, PhaseProve, PhaseResult, PhaseDone, PhaseFailed:
		return Phase(raw), true
	case "explore":
		return PhaseDiscover, true
	case "execute":
		return PhaseGenerate, true
	case "review":
		return PhaseProve, true
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
	// PauseApproval parks a mission whose plan just landed with
	// auto_approve_plan=false (D-087, issue #456), waiting on one of
	// the three operator verbs (approve/replan/rediscover). Distinct
	// from PauseInfra/PauseBackoff/PauseNoProgress/PauseBudget: those
	// all describe something going wrong that a sweep may self-heal or
	// escalate; this describes an operator decision point with no safe
	// default, so no sweep (autoResumeInfra, sweepPermissionTimeouts,
	// or any future one) ever inspects this reason. It waits forever
	// until a human acts.
	PauseApproval PauseReason = "approval"
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
	// InputPlanInfeasible fires when the planner reports the goal cannot
	// be achieved as stated (D-077); valid only in PhasePlan, fails the
	// mission instead of letting a rewritten goal reach generate.
	InputPlanInfeasible Input = "plan_infeasible"
	// InputResultComplete/InputResultFailed report the result phase's
	// own deterministic step outcome (driver-run, zero LLM turns): valid
	// only in PhaseResult. Complete advances to done; Failed parks the
	// mission IN result with PauseInfra so it stays resumable and the
	// work product isn't lost.
	InputResultComplete Input = "result_complete"
	InputResultFailed   Input = "result_failed"
	// InputPlanApprove/InputPlanReplan/InputPlanRediscover are the three
	// operator verbs that resolve a PauseApproval park (D-087): approve
	// advances straight to generate on the plan as landed; replan
	// re-enters PhasePlan with optional feedback folded into the next
	// planning prompt; rediscover re-enters PhaseDiscover. Valid only
	// when the mission is paused for PauseApproval; a no-op transition
	// otherwise, same "unrecognized/out-of-state input" convention as
	// InputPlanInfeasible's own phase check.
	InputPlanApprove    Input = "plan_approve"
	InputPlanReplan     Input = "plan_replan"
	InputPlanRediscover Input = "plan_rediscover"
	// InputAskUser fires when a phase turn's ask_user tool call parks the
	// mission (D-088, issue #457): the store has already recorded
	// pending_input and incremented asks_used by the time this input
	// reaches Step (mirrors InputWorkerBlocked's shape, both just move
	// Status to waiting_for_input); the difference is ask_user's answer
	// resolves through Store.AnswerPendingInput, not a plain resume.
	InputAskUser Input = "ask_user"
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
	// last unit: PhaseProve's approve transition needs this to decide
	// between advancing to generate (more units left) or result.
	LastUnit bool
	// ReplanUsed reports whether this mission already spent its one
	// automatic replan-on-stall attempt (stepWorkerRetry/
	// stepReviewRework), a second stall pauses for a human same as
	// before this feature existed.
	ReplanUsed bool
	// Light marks a mission that skips discover/plan/prove (D-069), a
	// stall can never replan into PhasePlan for one, since it never
	// visits that phase.
	Light bool
	// AutoApprovePlan gates the plan phase's success transition
	// (D-087, issue #456): true (the default) advances straight to
	// generate, byte-identical to pre-#456 behavior. false parks the
	// mission on PauseApproval instead, waiting for one of the three
	// operator verbs. Light missions never visit PhasePlan, so this
	// flag has no effect on them regardless of its value.
	AutoApprovePlan bool
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
	case InputAskUser:
		// The store already appended mission.input_requested (SetPendingInput)
		// before this input reaches Step: no separate event here, same as
		// pending_permission's own park (Store writes it, Step only moves
		// Status).
		return Transition{Next: withStatus(s, StatusWaitingForInput)}
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
	case InputResultComplete:
		return stepResultComplete(s)
	case InputResultFailed:
		return stepResultFailed(s, in)
	case InputPlanInfeasible:
		// Valid only in PhasePlan (D-077): a mission that reaches plan and
		// gets told the goal itself cannot be done fails outright rather
		// than falling through to a rewritten (fabricated) plan.
		if s.Phase != PhasePlan {
			return Transition{Next: s}
		}
		return Transition{
			Next:   withPhaseFailed(s),
			Events: []EventDraft{{Kind: "mission.failed", Payload: map[string]any{"reason": "goal_infeasible", "detail": in.Reason}}},
		}
	case InputPlanApprove:
		return stepPlanApprove(s)
	case InputPlanReplan:
		return stepPlanReplan(s, in)
	case InputPlanRediscover:
		return stepPlanRediscover(s)
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

// stepPhaseComplete advances discover/plan to the next fixed phase.
// generate's completion goes through prove (a worker verdict of DONE
// requests review, it doesn't itself complete the phase: the driver
// routes DONE into a review round, whose outcome arrives as
// InputReviewApprove/InputReviewRework, not InputPhaseComplete).
//
// The plan phase's own completion forks on AutoApprovePlan (D-087,
// issue #456): true is the byte-identical default, straight through to
// nextPhase like every other phase. false parks the mission instead:
// the plan already landed (runPlan's own SetSpec ran before this
// input arrives), so the park shows the real plan, not a stale one.
func stepPhaseComplete(s StepState) Transition {
	if s.Phase == PhasePlan && !s.AutoApprovePlan {
		return Transition{
			Next:   withPause(s, PauseApproval),
			Events: []EventDraft{{Kind: "mission.plan_awaiting_approval", Payload: map[string]any{}}},
		}
	}
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

// stepPlanApprove is the approve verb (D-087): valid only while parked
// on PauseApproval, advances straight to generate on the plan as it
// landed: the same nextPhase(PhasePlan) step stepPhaseComplete would
// have taken had AutoApprovePlan been true. Any other state is a no-op
// (the API layer rejects the request with 409 before Step ever sees
// it; this is the state machine's own belt).
func stepPlanApprove(s StepState) Transition {
	if s.Phase != PhasePlan || s.PauseReason != PauseApproval {
		return Transition{Next: s}
	}
	s.Status = StatusIdle
	s.PauseReason = ""
	s.Phase = PhaseGenerate
	s.Iteration = 0
	s.ConsecutiveFailures = 0
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.plan_approved", Payload: map[string]any{}}}}
}

// stepPlanReplan is the replan verb (D-087): re-enters PhasePlan for
// another planning turn with the operator's feedback (in.Reason)
// folded into the next prompt by the driver (mirrors replanNotes'
// existing stall-replan folding). Deliberately does NOT set
// ReplanUsed: only an AUTOMATIC stall replan spends that budget
// (stepWorkerRetry/stepReviewRework); an operator-requested replan is
// a free, unlimited iteration, per #456's "unlimited iterations" scope.
func stepPlanReplan(s StepState, in StepInput) Transition {
	if s.Phase != PhasePlan || s.PauseReason != PauseApproval {
		return Transition{Next: s}
	}
	s.Status = StatusIdle
	s.PauseReason = ""
	s.Iteration = 0
	s.ConsecutiveFailures = 0
	payload := map[string]any{"feedback": in.Reason != ""}
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.plan_replan_requested", Payload: payload}}}
}

// stepPlanRediscover is the rediscover verb (D-087): back to
// PhaseDiscover for a fresh exploration pass, same free-iteration
// reasoning as stepPlanReplan (no ReplanUsed, no iteration spend).
func stepPlanRediscover(s StepState) Transition {
	if s.Phase != PhasePlan || s.PauseReason != PauseApproval {
		return Transition{Next: s}
	}
	s.Status = StatusIdle
	s.PauseReason = ""
	s.Phase = PhaseDiscover
	s.Iteration = 0
	s.ConsecutiveFailures = 0
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.rediscover_requested", Payload: map[string]any{}}}}
}

// stepWorkerFailed counts consecutive failures toward the backoff
// brake; progress resets the counter elsewhere in this file
// (stepPhaseComplete, stepWorkerRetry, stepReviewApprove all zero it).
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
	// A replan is a fresh start, same as any other stepPhaseComplete
	// transition into a new phase: leaving ConsecutiveFailures set would
	// arrive at planning already one failure from a backoff pause despite
	// having made no failed attempt yet in the new phase.
	s.ConsecutiveFailures = 0
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.replan", Payload: map[string]any{"reason": in.Reason}}}}
}

// stepReviewApprove clears the stall counter (progress was made) and
// either moves to the next unit (more generate work) or advances to
// the result phase, depending on whether the reviewed unit was the
// plan's last.
func stepReviewApprove(s StepState) Transition {
	s.StallCount = 0
	s.LastGapFingerprint = ""
	if s.LastUnit {
		return reviewSkippedOrProvePassTransition(s)
	}
	s.Phase = PhaseGenerate
	s.Status = StatusIdle
	s.Iteration = 0
	s.ConsecutiveFailures = 0
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.unit_verified", Payload: map[string]any{"passed": true, "check": "review"}}}}
}

// reviewSkippedOrProvePassTransition advances a mission whose last
// unit is verified (either the review-skip fast path or an approved
// prove round) into the result phase, replacing the old direct
// transition to done: result now runs the delivery/copy/promote work
// that used to fire on the terminal done transition itself.
func reviewSkippedOrProvePassTransition(s StepState) Transition {
	s.Phase = PhaseResult
	s.Status = StatusIdle
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.phase_started", Payload: map[string]any{"phase": string(PhaseResult)}}}}
}

// stepReviewRework sends the mission back to generate for another
// attempt, tracking the stall brake: two consecutive rounds with an
// IDENTICAL gap fingerprint mean the reviewer keeps rejecting for the
// same reason, no real progress is happening.
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
	s.Phase = PhaseGenerate
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

// stepResultComplete is the result phase's own success transition,
// fired by the driver after every delivery/copy/promote/on_complete
// step succeeds (or has nothing to do). Zero LLM turns: this is pure
// bookkeeping, done stays the terminal state it always was.
func stepResultComplete(s StepState) Transition {
	s.Phase = PhaseDone
	s.Status = StatusDone
	// verified: false for light missions, which reach done with zero
	// harness verification (no spec units, no CheckArtifacts/RunVerify),
	// distinguishes that in the event log from a harness-verified done.
	return Transition{Next: s, Events: []EventDraft{{Kind: "mission.done", Payload: map[string]any{"verified": !s.Light}}}}
}

// stepResultFailed parks a mission whose result step hit a delivery,
// artifact-copy, kb-promotion, or on_complete failure: PauseInfra
// keeps it resumable in PhaseResult (not failed) so the retry only
// re-runs the failed pieces, and the work product (branch, artifacts)
// is never lost.
func stepResultFailed(s StepState, in StepInput) Transition {
	return Transition{
		Next:   withPause(s, PauseInfra),
		Events: []EventDraft{{Kind: "mission.paused", Payload: map[string]any{"reason": string(PauseInfra), "detail": in.Reason}}},
	}
}
