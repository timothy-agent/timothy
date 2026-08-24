package missions

import (
	"reflect"
	"testing"
)

func TestStep(t *testing.T) {
	budget := func(v float64) *float64 { return &v }

	cases := []struct {
		name  string
		state StepState
		input StepInput
		cfg   Config
		want  StepState
	}{
		{
			name:  "cancel from working fails the mission",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking},
			input: StepInput{Input: InputCancel},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseFailed, Status: StatusError},
		},
		{
			name:  "cancel from waiting_for_input fails the mission",
			state: StepState{Phase: PhaseExecute, Status: StatusWaitingForInput},
			input: StepInput{Input: InputCancel},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseFailed, Status: StatusError},
		},
		{
			name:  "cancel from paused fails the mission and clears the stale pause reason",
			state: StepState{Phase: PhaseExecute, Status: StatusPaused, PauseReason: PauseBackoff},
			input: StepInput{Input: InputCancel},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseFailed, Status: StatusError},
		},
		{
			name:  "cancel from done is a no-op (already terminal)",
			state: StepState{Phase: PhaseDone, Status: StatusDone},
			input: StepInput{Input: InputCancel},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseDone, Status: StatusDone},
		},
		{
			name:  "cancel from failed is a no-op (already terminal)",
			state: StepState{Phase: PhaseFailed, Status: StatusError},
			input: StepInput{Input: InputCancel},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseFailed, Status: StatusError},
		},
		{
			name:  "budget exhausted pauses regardless of input, pre-empting review_approve",
			state: StepState{Phase: PhaseReview, Status: StatusWorking, Spent: 10, Budget: budget(10)},
			input: StepInput{Input: InputReviewApprove},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseReview, Status: StatusPaused, PauseReason: PauseBudget, Spent: 10, Budget: budget(10)},
		},
		{
			name:  "spend under budget does not pause",
			state: StepState{Phase: PhaseReview, Status: StatusWorking, Spent: 5, Budget: budget(10), LastUnit: true},
			input: StepInput{Input: InputReviewApprove},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseDone, Status: StatusDone, Spent: 5, Budget: budget(10), LastUnit: true},
		},
		{
			name:  "nil budget never pauses on spend",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, Spent: 1_000_000},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: ""},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, Spent: 1_000_000, Iteration: 1},
		},
		{
			name:  "budget check does not apply to a terminal mission",
			state: StepState{Phase: PhaseDone, Status: StatusDone, Spent: 999, Budget: budget(1)},
			input: StepInput{Input: InputResume},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseDone, Status: StatusDone, Spent: 999, Budget: budget(1)},
		},
		{
			name:  "mixed-currency spend pauses even when same-currency spend is well under budget",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, Spent: 0, Budget: budget(100), MixedCurrencySpend: true},
			input: StepInput{Input: InputWorkerRetry},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusPaused, PauseReason: PauseMixedCurrency, Spent: 0, Budget: budget(100), MixedCurrencySpend: true},
		},
		{
			name:  "phase_complete advances explore to plan",
			state: StepState{Phase: PhaseExplore, Status: StatusWorking, Iteration: 2, ConsecutiveFailures: 1},
			input: StepInput{Input: InputPhaseComplete},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhasePlan, Status: StatusIdle},
		},
		{
			name:  "phase_complete advances plan to execute",
			state: StepState{Phase: PhasePlan, Status: StatusWorking},
			input: StepInput{Input: InputPhaseComplete},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusIdle},
		},
		{
			name:  "phase_complete advances execute to review",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking},
			input: StepInput{Input: InputPhaseComplete},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseReview, Status: StatusIdle},
		},
		{
			name:  "phase_complete on review is a no-op (review resolves via approve/rework, not phase_complete)",
			state: StepState{Phase: PhaseReview, Status: StatusWorking, Iteration: 3},
			input: StepInput{Input: InputPhaseComplete},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseReview, Status: StatusWorking, Iteration: 3},
		},
		{
			name:  "worker_blocked parks the mission waiting for input",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking},
			input: StepInput{Input: InputWorkerBlocked, Message: "which endpoint?"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusWaitingForInput},
		},
		{
			name:  "worker_failed below backoff threshold just retries",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, ConsecutiveFailures: 1},
			input: StepInput{Input: InputWorkerFailed},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, ConsecutiveFailures: 2, Iteration: 1},
		},
		{
			name:  "worker_failed 3rd consecutive time pauses with backoff",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, ConsecutiveFailures: 2},
			input: StepInput{Input: InputWorkerFailed},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusPaused, PauseReason: PauseBackoff, MaxIterations: 8, ConsecutiveFailures: 3, Iteration: 1},
		},
		{
			name:  "worker_failed hitting max_iterations hard-fails instead of pausing",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 1, ConsecutiveFailures: 0},
			input: StepInput{Input: InputWorkerFailed},
			cfg:   Config{BackoffFailures: 10, StallRounds: 10},
			want:  StepState{Phase: PhaseFailed, Status: StatusError, MaxIterations: 1, ConsecutiveFailures: 1, Iteration: 1},
		},
		{
			// Ceiling is a hard stop checked before backoff: reaching
			// MaxIterations and BackoffFailures on the same failure must
			// fail terminally, never pause for backoff.
			name:  "worker_failed hitting both ceiling and backoff threshold hard-fails, not pauses",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 3, Iteration: 2, ConsecutiveFailures: 2},
			input: StepInput{Input: InputWorkerFailed},
			cfg:   Config{BackoffFailures: 3, StallRounds: 10},
			want:  StepState{Phase: PhaseFailed, Status: StatusError, MaxIterations: 3, Iteration: 3, ConsecutiveFailures: 3},
		},
		{
			name:  "worker_retry costs an iteration and resets consecutive failures",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, ConsecutiveFailures: 2, Iteration: 1},
			input: StepInput{Input: InputWorkerRetry},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, ConsecutiveFailures: 0, Iteration: 2},
		},
		{
			name:  "worker_retry hitting max_iterations hard-fails",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 1, Iteration: 0},
			input: StepInput{Input: InputWorkerRetry},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseFailed, Status: StatusError, MaxIterations: 1, Iteration: 1, ConsecutiveFailures: 0},
		},
		{
			name:  "worker_retry with no fingerprint never accumulates stall",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "abc"},
			input: StepInput{Input: InputWorkerRetry},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, Iteration: 1, StallCount: 1, LastGapFingerprint: "abc"},
		},
		{
			name:  "worker_retry with a new fingerprint returns to execute, stall count resets to 1",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, StallCount: 0, LastGapFingerprint: ""},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: "verify_failed:unit_0"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, Iteration: 1, StallCount: 1, LastGapFingerprint: "verify_failed:unit_0"},
		},
		{
			name:  "worker_retry with the SAME fingerprint twice pauses no_progress once replan is already used",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "verify_failed:unit_0", ReplanUsed: true},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: "verify_failed:unit_0"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusPaused, PauseReason: PauseNoProgress, MaxIterations: 8, StallCount: 2, LastGapFingerprint: "verify_failed:unit_0", ReplanUsed: true},
		},
		{
			name:  "worker_retry with a DIFFERENT fingerprint does not accumulate stall",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "verify_failed:unit_0"},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: "verify_failed:unit_1"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, Iteration: 1, StallCount: 1, LastGapFingerprint: "verify_failed:unit_1"},
		},
		{
			name:  "review_approve on a non-last unit returns to execute",
			state: StepState{Phase: PhaseReview, Status: StatusWorking, LastUnit: false, StallCount: 1, LastGapFingerprint: "x"},
			input: StepInput{Input: InputReviewApprove},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusIdle, LastUnit: false},
		},
		{
			name:  "review_approve on the last unit completes the mission",
			state: StepState{Phase: PhaseReview, Status: StatusWorking, LastUnit: true, StallCount: 1, LastGapFingerprint: "x"},
			input: StepInput{Input: InputReviewApprove},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseDone, Status: StatusDone, LastUnit: true},
		},
		{
			name:  "review_rework with a new fingerprint returns to execute, stall count resets to 1",
			state: StepState{Phase: PhaseReview, Status: StatusWorking, MaxIterations: 8, StallCount: 0, LastGapFingerprint: ""},
			input: StepInput{Input: InputReviewRework, GapFingerprint: "abc"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusIdle, MaxIterations: 8, Iteration: 1, StallCount: 1, LastGapFingerprint: "abc"},
		},
		{
			name:  "review_rework with the SAME fingerprint twice pauses no_progress once replan is already used",
			state: StepState{Phase: PhaseReview, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "abc", ReplanUsed: true},
			input: StepInput{Input: InputReviewRework, GapFingerprint: "abc"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseReview, Status: StatusPaused, PauseReason: PauseNoProgress, MaxIterations: 8, StallCount: 2, LastGapFingerprint: "abc", ReplanUsed: true},
		},
		{
			name:  "review_rework with a DIFFERENT fingerprint does not accumulate stall",
			state: StepState{Phase: PhaseReview, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "abc"},
			input: StepInput{Input: InputReviewRework, GapFingerprint: "def"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusIdle, MaxIterations: 8, Iteration: 1, StallCount: 1, LastGapFingerprint: "def"},
		},
		{
			name:  "review_rework hitting max_iterations hard-fails instead of pausing",
			state: StepState{Phase: PhaseReview, Status: StatusWorking, MaxIterations: 1, Iteration: 0, StallCount: 0},
			input: StepInput{Input: InputReviewRework, GapFingerprint: "abc"},
			cfg:   Config{BackoffFailures: 10, StallRounds: 10},
			want:  StepState{Phase: PhaseFailed, Status: StatusError, MaxIterations: 1, Iteration: 1, StallCount: 1, LastGapFingerprint: "abc"},
		},
		{
			name:  "worker_retry stall with replan unused replans instead of pausing",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "verify_failed:unit_0"},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: "verify_failed:unit_0", Reason: "same failure again"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhasePlan, Status: StatusIdle, MaxIterations: 8, ReplanUsed: true},
		},
		{
			name:  "review_rework stall with replan unused replans instead of pausing",
			state: StepState{Phase: PhaseReview, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "abc"},
			input: StepInput{Input: InputReviewRework, GapFingerprint: "abc", Reason: "reviewer keeps rejecting"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhasePlan, Status: StatusIdle, MaxIterations: 8, ReplanUsed: true},
		},
		{
			// A mission can carry ConsecutiveFailures into a stall (e.g. one
			// worker_failed round before the stall's own worker_retry
			// rounds) — replanning must clear it same as any other fresh
			// phase start (stepPhaseComplete), or the replanned mission
			// arrives at planning one failure from a backoff pause despite
			// having made no failed attempt yet in the new phase.
			name:  "worker_retry stall replan clears ConsecutiveFailures",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "verify_failed:unit_0", ConsecutiveFailures: 2},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: "verify_failed:unit_0", Reason: "same failure again"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhasePlan, Status: StatusIdle, MaxIterations: 8, ReplanUsed: true},
		},
		{
			name:  "review_infra_failure pauses with infra reason",
			state: StepState{Phase: PhaseReview, Status: StatusWorking},
			input: StepInput{Input: InputReviewInfraFailure},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseReview, Status: StatusPaused, PauseReason: PauseInfra},
		},
		{
			name:  "resume from paused clears the pause reason and goes idle",
			state: StepState{Phase: PhaseExecute, Status: StatusPaused, PauseReason: PauseBackoff, Iteration: 3, ConsecutiveFailures: 2},
			input: StepInput{Input: InputResume},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusIdle, Iteration: 3, ConsecutiveFailures: 2},
		},
		{
			name:  "resume from waiting_for_input clears and goes idle",
			state: StepState{Phase: PhaseExecute, Status: StatusWaitingForInput},
			input: StepInput{Input: InputResume},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusIdle},
		},
		{
			name:  "resume on a terminal mission is a no-op",
			state: StepState{Phase: PhaseDone, Status: StatusDone},
			input: StepInput{Input: InputResume},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseDone, Status: StatusDone},
		},
		{
			// D-077: a planner-reported infeasible goal fails the mission
			// outright instead of falling through to a rewritten plan.
			name:  "plan_infeasible in plan phase fails the mission",
			state: StepState{Phase: PhasePlan, Status: StatusWorking},
			input: StepInput{Input: InputPlanInfeasible, Reason: "goal forbids the only possible action"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseFailed, Status: StatusError},
		},
		{
			// InputPlanInfeasible is valid only in PhasePlan; arriving in
			// any other phase is a no-op, same convention as the default
			// unrecognized-input case.
			name:  "plan_infeasible outside plan phase is a no-op",
			state: StepState{Phase: PhaseExecute, Status: StatusWorking},
			input: StepInput{Input: InputPlanInfeasible, Reason: "goal forbids the only possible action"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseExecute, Status: StatusWorking},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Step(tc.state, tc.input, tc.cfg)
			if !reflect.DeepEqual(got.Next, tc.want) {
				t.Fatalf("Step(%+v, %+v) = %+v, want %+v", tc.state, tc.input, got.Next, tc.want)
			}
		})
	}
}

// TestStepReviewApproveEmitsPassedTrue guards against the web
// timeline rendering an approved unit as "failed": stepReviewApprove
// only ever fires on approval, so its mission.unit_verified event must
// carry passed=true rather than an empty payload (which the renderer
// treats as false).
func TestStepReviewApproveEmitsPassedTrue(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseReview, Status: StatusWorking, LastUnit: false},
		StepInput{Input: InputReviewApprove},
		DefaultConfig,
	)
	if len(got.Events) != 1 || got.Events[0].Kind != "mission.unit_verified" {
		t.Fatalf("Events = %+v, want exactly one mission.unit_verified event", got.Events)
	}
	passed, ok := got.Events[0].Payload["passed"].(bool)
	if !ok || !passed {
		t.Fatalf("Events[0].Payload = %+v, want passed=true", got.Events[0].Payload)
	}
}

// TestStepPlanInfeasibleEmitsGoalInfeasibleEvent confirms the plan
// phase's infeasible transition emits mission.failed with
// reason=goal_infeasible and the planner's own detail, distinguishable
// in the event log from a max_iterations or cancelled failure.
func TestStepPlanInfeasibleEmitsGoalInfeasibleEvent(t *testing.T) {
	got := Step(
		StepState{Phase: PhasePlan, Status: StatusWorking},
		StepInput{Input: InputPlanInfeasible, Reason: "goal forbids the only possible action"},
		DefaultConfig,
	)
	if len(got.Events) != 1 || got.Events[0].Kind != "mission.failed" {
		t.Fatalf("Events = %+v, want exactly one mission.failed event", got.Events)
	}
	if reason, _ := got.Events[0].Payload["reason"].(string); reason != "goal_infeasible" {
		t.Fatalf("mission.failed payload reason = %q, want %q", reason, "goal_infeasible")
	}
	if detail, _ := got.Events[0].Payload["detail"].(string); detail != "goal forbids the only possible action" {
		t.Fatalf("mission.failed payload detail = %q, want the planner's reason", detail)
	}
}

// TestStepMixedCurrencyPauseDetail confirms the mixed-currency pause
// event's detail is distinguishable from an ordinary budget-reached
// pause purely from the event log — the two have different root
// causes and a human reading the log later needs to tell them apart.
func TestStepMixedCurrencyPauseDetail(t *testing.T) {
	budget := 100.0
	got := Step(
		StepState{Phase: PhaseExecute, Status: StatusWorking, Budget: &budget, MixedCurrencySpend: true},
		StepInput{Input: InputWorkerRetry},
		DefaultConfig,
	)
	if len(got.Events) != 1 || got.Events[0].Kind != "mission.paused" {
		t.Fatalf("Events = %+v, want exactly one mission.paused event", got.Events)
	}
	detail, _ := got.Events[0].Payload["detail"].(string)
	if detail == "" || detail == "budget reached" {
		t.Fatalf("Events[0].Payload[detail] = %q, want a mixed-currency-specific message", detail)
	}
}

// TestStepReplanEmitsReasonAndUsesReplanOnlyOnce confirms the stall
// branch's replan path emits mission.replan carrying the stall reason,
// and that a second identical stall (ReplanUsed already true) pauses
// no_progress exactly as before this feature existed.
func TestStepReplanEmitsReasonAndUsesReplanOnlyOnce(t *testing.T) {
	first := Step(
		StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "fp"},
		StepInput{Input: InputWorkerRetry, GapFingerprint: "fp", Reason: "stuck"},
		DefaultConfig,
	)
	if len(first.Events) != 1 || first.Events[0].Kind != "mission.replan" {
		t.Fatalf("Events = %+v, want exactly one mission.replan event", first.Events)
	}
	if reason, _ := first.Events[0].Payload["reason"].(string); reason != "stuck" {
		t.Fatalf("mission.replan payload reason = %q, want %q", reason, "stuck")
	}
	if !first.Next.ReplanUsed {
		t.Fatal("ReplanUsed = false after the first stall, want true")
	}

	second := Step(
		StepState{Phase: PhaseExecute, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "fp", ReplanUsed: true},
		StepInput{Input: InputWorkerRetry, GapFingerprint: "fp"},
		DefaultConfig,
	)
	if second.Next.Status != StatusPaused || second.Next.PauseReason != PauseNoProgress {
		t.Fatalf("second identical stall = %+v, want paused/no_progress", second.Next)
	}
}

// TestStepLightApproveGoesDone confirms a light mission (born in
// PhaseExecute, empty spec so LastUnit is always true) reaches done on
// review_approve exactly like a coding/general mission's last unit,
// and that its mission.done event carries verified=false — a light
// mission has no spec units, so it reaches done with zero harness
// verification and the event log must say so.
func TestStepLightApproveGoesDone(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseExecute, Status: StatusWorking, Light: true, LastUnit: true},
		StepInput{Input: InputReviewApprove},
		DefaultConfig,
	)
	if got.Next.Phase != PhaseDone || got.Next.Status != StatusDone {
		t.Fatalf("Step(light approve) = %+v, want phase=done status=done", got.Next)
	}
	if len(got.Events) != 1 || got.Events[0].Kind != "mission.done" {
		t.Fatalf("Events = %+v, want exactly one mission.done event", got.Events)
	}
	if verified, ok := got.Events[0].Payload["verified"].(bool); !ok || verified {
		t.Fatalf("light mission.done payload = %+v, want verified=false", got.Events[0].Payload)
	}
}

// TestStepNonLightApproveDoneIsVerified confirms a mission that reaches
// its last unit via harness/review checks (not light) gets
// verified=true on mission.done.
func TestStepNonLightApproveDoneIsVerified(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseReview, Status: StatusWorking, Light: false, LastUnit: true},
		StepInput{Input: InputReviewApprove},
		DefaultConfig,
	)
	if len(got.Events) != 1 || got.Events[0].Kind != "mission.done" {
		t.Fatalf("Events = %+v, want exactly one mission.done event", got.Events)
	}
	if verified, ok := got.Events[0].Payload["verified"].(bool); !ok || !verified {
		t.Fatalf("non-light mission.done payload = %+v, want verified=true", got.Events[0].Payload)
	}
}

// TestStepLightStallNeverReplans confirms a light mission's stall
// brake (D-069) skips replanTransition — which would otherwise send it
// to PhasePlan, a phase a light mission never visits — and instead
// keeps retrying until max_iterations fails it, the same ceiling
// stepWorkerFailed's backoff path respects.
func TestStepLightStallNeverReplans(t *testing.T) {
	// A stall that would trigger replanTransition for an ordinary
	// mission (StallCount already at the threshold, ReplanUsed false)
	// must instead fall through to the plain retry path for a light one.
	got := Step(
		StepState{Phase: PhaseExecute, Status: StatusWorking, Light: true, MaxIterations: 8, Iteration: 0, StallCount: 1, LastGapFingerprint: "fp"},
		StepInput{Input: InputWorkerRetry, GapFingerprint: "fp", Reason: "stuck"},
		DefaultConfig,
	)
	if got.Next.Phase == PhasePlan {
		t.Fatalf("Step(light stall) = %+v, must never route to PhasePlan", got.Next)
	}
	if got.Next.Status == StatusPaused {
		t.Fatalf("Step(light stall) = %+v, must not pause no_progress either — retry to max_iterations instead", got.Next)
	}
	if len(got.Events) != 1 || got.Events[0].Kind != "mission.retry" {
		t.Fatalf("Events = %+v, want exactly one mission.retry event", got.Events)
	}

	// Repeated identical-fingerprint stalls still respect the hard
	// max_iterations ceiling, exactly like worker_failed.
	final := Step(
		StepState{Phase: PhaseExecute, Status: StatusWorking, Light: true, MaxIterations: 1, Iteration: 0, StallCount: 1, LastGapFingerprint: "fp"},
		StepInput{Input: InputWorkerRetry, GapFingerprint: "fp", Reason: "stuck"},
		DefaultConfig,
	)
	if final.Next.Phase != PhaseFailed {
		t.Fatalf("Step(light stall at max_iterations) = %+v, want phase=failed", final.Next)
	}
}

func TestParsePhase(t *testing.T) {
	valid := []Phase{PhaseExplore, PhasePlan, PhaseExecute, PhaseReview, PhaseDone, PhaseFailed}
	for _, p := range valid {
		if got, ok := parsePhase(string(p)); !ok || got != p {
			t.Fatalf("parsePhase(%q) = %q, %v; want %q, true", p, got, ok, p)
		}
	}
	if _, ok := parsePhase("nonsense"); ok {
		t.Fatal("parsePhase accepted an unrecognized value")
	}
	if _, ok := parsePhase(""); ok {
		t.Fatal("parsePhase accepted empty string")
	}
}

func TestParseStatus(t *testing.T) {
	valid := []Status{StatusIdle, StatusWorking, StatusWaitingForInput, StatusPaused, StatusDone, StatusError}
	for _, s := range valid {
		if got, ok := parseStatus(string(s)); !ok || got != s {
			t.Fatalf("parseStatus(%q) = %q, %v; want %q, true", s, got, ok, s)
		}
	}
	if _, ok := parseStatus("nonsense"); ok {
		t.Fatal("parseStatus accepted an unrecognized value")
	}
}

func TestPhaseTerminal(t *testing.T) {
	terminal := []Phase{PhaseDone, PhaseFailed}
	for _, p := range terminal {
		if !p.Terminal() {
			t.Fatalf("%q.Terminal() = false, want true", p)
		}
	}
	nonTerminal := []Phase{PhaseExplore, PhasePlan, PhaseExecute, PhaseReview}
	for _, p := range nonTerminal {
		if p.Terminal() {
			t.Fatalf("%q.Terminal() = true, want false", p)
		}
	}
}
