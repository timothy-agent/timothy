package missions

import (
	"reflect"
	"strings"
	"testing"
	"time"
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
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking},
			input: StepInput{Input: InputCancel},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseFailed, Status: StatusError},
		},
		{
			name:  "cancel from waiting_for_input fails the mission",
			state: StepState{Phase: PhaseGenerate, Status: StatusWaitingForInput},
			input: StepInput{Input: InputCancel},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseFailed, Status: StatusError},
		},
		{
			name:  "cancel from paused fails the mission and clears the stale pause reason",
			state: StepState{Phase: PhaseGenerate, Status: StatusPaused, PauseReason: PauseBackoff},
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
			state: StepState{Phase: PhaseProve, Status: StatusWorking, Spent: 10, Budget: budget(10)},
			input: StepInput{Input: InputReviewApprove},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseProve, Status: StatusPaused, PauseReason: PauseBudget, Spent: 10, Budget: budget(10)},
		},
		{
			name:  "spend under budget does not pause",
			state: StepState{Phase: PhaseProve, Status: StatusWorking, Spent: 5, Budget: budget(10)},
			input: StepInput{Input: InputReviewApprove},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseResult, Status: StatusIdle, Spent: 5, Budget: budget(10)},
		},
		{
			name:  "nil budget never pauses on spend",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, Spent: 1_000_000},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: ""},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, Spent: 1_000_000, Iteration: 1},
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
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, Spent: 0, Budget: budget(100), MixedCurrencySpend: true},
			input: StepInput{Input: InputWorkerRetry},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusPaused, PauseReason: PauseMixedCurrency, Spent: 0, Budget: budget(100), MixedCurrencySpend: true},
		},
		{
			name:  "phase_complete advances discover to plan",
			state: StepState{Phase: PhaseDiscover, Status: StatusWorking, Iteration: 2, ConsecutiveFailures: 1},
			input: StepInput{Input: InputPhaseComplete},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhasePlan, Status: StatusIdle},
		},
		{
			// D-090, issue #459: flow=discover_generate routes discover's
			// completion straight to generate, never plan.
			name:  "flow=discover_generate: phase_complete advances discover straight to generate, skipping plan",
			state: StepState{Phase: PhaseDiscover, Status: StatusWorking, Flow: FlowDiscoverGenerate, Iteration: 2, ConsecutiveFailures: 1},
			input: StepInput{Input: InputPhaseComplete},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusIdle, Flow: FlowDiscoverGenerate},
		},
		{
			name:  "phase_complete advances plan to generate when auto_approve_plan is true",
			state: StepState{Phase: PhasePlan, Status: StatusWorking, AutoApprovePlan: true},
			input: StepInput{Input: InputPhaseComplete},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusIdle, AutoApprovePlan: true},
		},
		{
			name:  "phase_complete parks on approval when auto_approve_plan is false",
			state: StepState{Phase: PhasePlan, Status: StatusWorking},
			input: StepInput{Input: InputPhaseComplete},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval},
		},
		{
			name:  "phase_complete advances generate to prove",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking},
			input: StepInput{Input: InputPhaseComplete},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseProve, Status: StatusIdle},
		},
		{
			name:  "phase_complete on prove is a no-op (prove resolves via approve/rework, not phase_complete)",
			state: StepState{Phase: PhaseProve, Status: StatusWorking, Iteration: 3},
			input: StepInput{Input: InputPhaseComplete},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseProve, Status: StatusWorking, Iteration: 3},
		},
		{
			name:  "worker_blocked parks the mission waiting for input",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking},
			input: StepInput{Input: InputWorkerBlocked, Message: "which endpoint?"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusWaitingForInput},
		},
		{
			name:  "worker_failed below backoff threshold just retries",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, ConsecutiveFailures: 1},
			input: StepInput{Input: InputWorkerFailed},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, ConsecutiveFailures: 2, Iteration: 1},
		},
		{
			name:  "worker_failed 3rd consecutive time pauses with backoff",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, ConsecutiveFailures: 2},
			input: StepInput{Input: InputWorkerFailed},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusPaused, PauseReason: PauseBackoff, MaxIterations: 8, ConsecutiveFailures: 3, Iteration: 1},
		},
		{
			name:  "worker_failed hitting max_iterations hard-fails instead of pausing",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 1, ConsecutiveFailures: 0},
			input: StepInput{Input: InputWorkerFailed},
			cfg:   Config{BackoffFailures: 10, StallRounds: 10},
			want:  StepState{Phase: PhaseFailed, Status: StatusError, MaxIterations: 1, ConsecutiveFailures: 1, Iteration: 1},
		},
		{
			// Ceiling is a hard stop checked before backoff: reaching
			// MaxIterations and BackoffFailures on the same failure must
			// fail terminally, never pause for backoff.
			name:  "worker_failed hitting both ceiling and backoff threshold hard-fails, not pauses",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 3, Iteration: 2, ConsecutiveFailures: 2},
			input: StepInput{Input: InputWorkerFailed},
			cfg:   Config{BackoffFailures: 3, StallRounds: 10},
			want:  StepState{Phase: PhaseFailed, Status: StatusError, MaxIterations: 3, Iteration: 3, ConsecutiveFailures: 3},
		},
		{
			name:  "worker_retry costs an iteration and resets consecutive failures",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, ConsecutiveFailures: 2, Iteration: 1},
			input: StepInput{Input: InputWorkerRetry},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, ConsecutiveFailures: 0, Iteration: 2},
		},
		{
			name:  "worker_retry hitting max_iterations hard-fails",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 1, Iteration: 0},
			input: StepInput{Input: InputWorkerRetry},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseFailed, Status: StatusError, MaxIterations: 1, Iteration: 1, ConsecutiveFailures: 0},
		},
		{
			name:  "worker_retry with no fingerprint never accumulates stall",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "abc"},
			input: StepInput{Input: InputWorkerRetry},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, Iteration: 1, StallCount: 1, LastGapFingerprint: "abc"},
		},
		{
			name:  "worker_retry with a new fingerprint returns to generate, stall count resets to 1",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, StallCount: 0, LastGapFingerprint: ""},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: "verify_failed:unit_0"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, Iteration: 1, StallCount: 1, LastGapFingerprint: "verify_failed:unit_0"},
		},
		{
			name:  "worker_retry with the SAME fingerprint twice pauses no_progress once replan is already used",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "verify_failed:unit_0", ReplanUsed: true},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: "verify_failed:unit_0"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusPaused, PauseReason: PauseNoProgress, MaxIterations: 8, StallCount: 2, LastGapFingerprint: "verify_failed:unit_0", ReplanUsed: true},
		},
		{
			name:  "worker_retry with a DIFFERENT fingerprint does not accumulate stall",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "verify_failed:unit_0"},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: "verify_failed:unit_1"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, Iteration: 1, StallCount: 1, LastGapFingerprint: "verify_failed:unit_1"},
		},
		{
			name:  "review_approve on a non-last unit returns to generate",
			state: StepState{Phase: PhaseProve, Status: StatusWorking, StallCount: 1, LastGapFingerprint: "x", Units: []PlanUnit{{}}},
			input: StepInput{Input: InputReviewApprove},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusIdle, Units: []PlanUnit{{}}},
		},
		{
			name:  "review_approve on the last unit advances to result, not done",
			state: StepState{Phase: PhaseProve, Status: StatusWorking, StallCount: 1, LastGapFingerprint: "x"},
			input: StepInput{Input: InputReviewApprove},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseResult, Status: StatusIdle},
		},
		{
			name:  "review_rework with no findings returns to generate and counts the round",
			state: StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8},
			input: StepInput{Input: InputReviewRework},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusIdle, MaxIterations: 8, Iteration: 1, ReworkRounds: 1},
		},
		{
			name:  "review_rework leaves the worker-retry stall fingerprint alone",
			state: StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "abc"},
			input: StepInput{Input: InputReviewRework},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusIdle, MaxIterations: 8, Iteration: 1, StallCount: 1, LastGapFingerprint: "abc", ReworkRounds: 1},
		},
		{
			name:  "review_rework reaching max_iterations parks review_exhausted in generate instead of failing",
			state: StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 1},
			input: StepInput{Input: InputReviewRework},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusPaused, PauseReason: PauseReviewExhausted, MaxIterations: 1, ReworkRounds: 1},
		},
		{
			name:  "worker_retry stall with replan unused replans instead of pausing",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "verify_failed:unit_0"},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: "verify_failed:unit_0", Reason: "same failure again"},
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
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "verify_failed:unit_0", ConsecutiveFailures: 2},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: "verify_failed:unit_0", Reason: "same failure again"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhasePlan, Status: StatusIdle, MaxIterations: 8, ReplanUsed: true},
		},
		{
			name:  "review_infra_failure pauses with infra reason",
			state: StepState{Phase: PhaseProve, Status: StatusWorking},
			input: StepInput{Input: InputReviewInfraFailure},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseProve, Status: StatusPaused, PauseReason: PauseInfra},
		},
		{
			name:  "review_budget pauses with budget reason (D-097)",
			state: StepState{Phase: PhaseProve, Status: StatusWorking, Iteration: 2},
			input: StepInput{Input: InputReviewBudget, Reason: "review input tokens 1500100 reached the ceiling 1500000"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseProve, Status: StatusPaused, PauseReason: PauseBudget, Iteration: 2},
		},
		{
			name:  "resume from paused clears the pause reason and goes idle",
			state: StepState{Phase: PhaseGenerate, Status: StatusPaused, PauseReason: PauseBackoff, Iteration: 3, ConsecutiveFailures: 2},
			input: StepInput{Input: InputResume},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusIdle, Iteration: 3, ConsecutiveFailures: 2},
		},
		{
			name:  "resume from waiting_for_input clears and goes idle",
			state: StepState{Phase: PhaseGenerate, Status: StatusWaitingForInput},
			input: StepInput{Input: InputResume},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusIdle},
		},
		{
			name:  "resume on a terminal mission is a no-op",
			state: StepState{Phase: PhaseDone, Status: StatusDone},
			input: StepInput{Input: InputResume},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseDone, Status: StatusDone},
		},
		{
			name:  "resume from a result-phase pause clears the pause reason and goes idle",
			state: StepState{Phase: PhaseResult, Status: StatusPaused, PauseReason: PauseInfra},
			input: StepInput{Input: InputResume},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseResult, Status: StatusIdle},
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
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking},
			input: StepInput{Input: InputPlanInfeasible, Reason: "goal forbids the only possible action"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusWorking},
		},
		{
			name:  "result_complete advances result to done",
			state: StepState{Phase: PhaseResult, Status: StatusWorking},
			input: StepInput{Input: InputResultComplete},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseDone, Status: StatusDone},
		},
		{
			name:  "result_failed parks the mission in result with an infra pause",
			state: StepState{Phase: PhaseResult, Status: StatusWorking},
			input: StepInput{Input: InputResultFailed, Reason: "delivery: destination unreachable"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseResult, Status: StatusPaused, PauseReason: PauseInfra},
		},
		{
			name:  "plan_approve from an approval park advances to generate",
			state: StepState{Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval},
			input: StepInput{Input: InputPlanApprove},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusIdle},
		},
		{
			// Operator iterations are free: replan must NOT set ReplanUsed
			// (that budget is reserved for the automatic stall-replan path).
			name:  "plan_replan from an approval park re-enters plan without spending ReplanUsed",
			state: StepState{Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval},
			input: StepInput{Input: InputPlanReplan, Reason: "use Go 1.23 not 1.21"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhasePlan, Status: StatusIdle},
		},
		{
			name:  "plan_rediscover from an approval park re-enters discover",
			state: StepState{Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval},
			input: StepInput{Input: InputPlanRediscover},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseDiscover, Status: StatusIdle},
		},
		{
			name:  "plan_approve outside an approval park is a no-op",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking},
			input: StepInput{Input: InputPlanApprove},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseGenerate, Status: StatusWorking},
		},
		{
			name:  "plan_replan outside an approval park is a no-op",
			state: StepState{Phase: PhasePlan, Status: StatusWorking},
			input: StepInput{Input: InputPlanReplan, Reason: "feedback"},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhasePlan, Status: StatusWorking},
		},
		{
			name:  "plan_rediscover outside an approval park is a no-op",
			state: StepState{Phase: PhaseResult, Status: StatusPaused, PauseReason: PauseInfra},
			input: StepInput{Input: InputPlanRediscover},
			cfg:   DefaultConfig,
			want:  StepState{Phase: PhaseResult, Status: StatusPaused, PauseReason: PauseInfra},
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
		StepState{Phase: PhaseProve, Status: StatusWorking, Units: []PlanUnit{{}}},
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
		StepState{Phase: PhaseGenerate, Status: StatusWorking, Budget: &budget, MixedCurrencySpend: true},
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
		StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "fp"},
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
		StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, StallCount: 1, LastGapFingerprint: "fp", ReplanUsed: true},
		StepInput{Input: InputWorkerRetry, GapFingerprint: "fp"},
		DefaultConfig,
	)
	if second.Next.Status != StatusPaused || second.Next.PauseReason != PauseNoProgress {
		t.Fatalf("second identical stall = %+v, want paused/no_progress", second.Next)
	}
}

// TestStepLightApproveGoesResult confirms a light mission (born in
// PhaseGenerate, empty plan so every unit counts as passed) advances to
// the result phase on review_approve exactly like a coding/general
// mission's last unit: not straight to done, since D-086's result
// phase now sits between the last unit's approval and done.
func TestStepLightApproveGoesResult(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseGenerate, Status: StatusWorking, Flow: FlowLight},
		StepInput{Input: InputReviewApprove},
		DefaultConfig,
	)
	if got.Next.Phase != PhaseResult || got.Next.Status != StatusIdle {
		t.Fatalf("Step(light approve) = %+v, want phase=result status=idle", got.Next)
	}
}

// TestStepDiscoverGenerateApproveGoesResult confirms a discover_generate
// mission (generate turn takes the same planless short-circuit as
// light, empty plan so every unit counts as passed) advances to the
// result phase on review_approve exactly like light, D-090.
func TestStepDiscoverGenerateApproveGoesResult(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseGenerate, Status: StatusWorking, Flow: FlowDiscoverGenerate},
		StepInput{Input: InputReviewApprove},
		DefaultConfig,
	)
	if got.Next.Phase != PhaseResult || got.Next.Status != StatusIdle {
		t.Fatalf("Step(discover_generate approve) = %+v, want phase=result status=idle", got.Next)
	}
}

// TestStepResultCompleteEmitsVerifiedFlag confirms mission.done's
// verified flag still distinguishes a light mission (zero harness
// verification) from one whose units passed CheckArtifacts/RunVerify,
// now emitted by stepResultComplete rather than stepReviewApprove.
func TestStepResultCompleteEmitsVerifiedFlag(t *testing.T) {
	light := Step(
		StepState{Phase: PhaseResult, Status: StatusWorking, Flow: FlowLight},
		StepInput{Input: InputResultComplete},
		DefaultConfig,
	)
	if len(light.Events) != 1 || light.Events[0].Kind != "mission.done" {
		t.Fatalf("Events = %+v, want exactly one mission.done event", light.Events)
	}
	if verified, ok := light.Events[0].Payload["verified"].(bool); !ok || verified {
		t.Fatalf("light mission.done payload = %+v, want verified=false", light.Events[0].Payload)
	}

	nonLight := Step(
		StepState{Phase: PhaseResult, Status: StatusWorking, Flow: FlowFull},
		StepInput{Input: InputResultComplete},
		DefaultConfig,
	)
	if len(nonLight.Events) != 1 || nonLight.Events[0].Kind != "mission.done" {
		t.Fatalf("Events = %+v, want exactly one mission.done event", nonLight.Events)
	}
	if verified, ok := nonLight.Events[0].Payload["verified"].(bool); !ok || !verified {
		t.Fatalf("non-light mission.done payload = %+v, want verified=true", nonLight.Events[0].Payload)
	}

	// D-090, issue #459: flow=discover_generate reaches done with zero
	// harness verification too (no spec units, planless), same as light.
	discoverGenerate := Step(
		StepState{Phase: PhaseResult, Status: StatusWorking, Flow: FlowDiscoverGenerate},
		StepInput{Input: InputResultComplete},
		DefaultConfig,
	)
	if verified, ok := discoverGenerate.Events[0].Payload["verified"].(bool); !ok || verified {
		t.Fatalf("discover_generate mission.done payload = %+v, want verified=false", discoverGenerate.Events[0].Payload)
	}
}

// TestStepResultFailedParksInResult confirms a result-step failure
// parks the mission IN PhaseResult (not failed), so a retry re-runs
// only the failed pieces and the work product is never lost.
func TestStepResultFailedParksInResult(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseResult, Status: StatusWorking},
		StepInput{Input: InputResultFailed, Reason: "delivery: destination unreachable"},
		DefaultConfig,
	)
	if got.Next.Phase != PhaseResult {
		t.Fatalf("Step(result_failed).Phase = %q, want PhaseResult (never lose the work product)", got.Next.Phase)
	}
	if got.Next.Status != StatusPaused || got.Next.PauseReason != PauseInfra {
		t.Fatalf("Step(result_failed) = %+v, want paused/infra", got.Next)
	}
	if len(got.Events) != 1 || got.Events[0].Kind != "mission.paused" {
		t.Fatalf("Events = %+v, want exactly one mission.paused event", got.Events)
	}
	if detail, _ := got.Events[0].Payload["detail"].(string); detail != "delivery: destination unreachable" {
		t.Fatalf("mission.paused payload detail = %q, want the result step's reason", detail)
	}
}

// TestStepAppliesVerification pins D-094's plan bookkeeping: a batch
// pass carried by any input lands on Units before the input itself is
// handled, Passes only ever flips on approval over harness evidence, and
// a passed unit failing again flips back to pending with a
// mission.unit_regressed event.
func TestStepAppliesVerification(t *testing.T) {
	long := strings.Repeat("x", verifyExcerptCap+100)
	cases := []struct {
		name       string
		state      StepState
		input      StepInput
		wantUnits  []PlanUnit
		wantPhase  Phase
		wantEvents []string
	}{
		{
			name:  "worker_retry records the failing excerpt without flipping anything",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, Units: []PlanUnit{{Title: "a"}}},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: "verify_failed:unit_0", Verified: []UnitVerification{{Unit: 0, Check: "verify_cmd", Excerpt: long}}},
			wantUnits: []PlanUnit{{Title: "a", VerifyCheck: "verify_cmd", VerifyExcerpt: long[:verifyExcerptCap] + "…"}},
			wantPhase: PhaseGenerate, wantEvents: []string{"mission.retry"},
		},
		{
			name:  "phase_complete marks a passing unit harness-passed and stays in generate while a unit is pending (D-096)",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, Units: []PlanUnit{{Title: "a"}, {Title: "b"}}},
			input: StepInput{Input: InputPhaseComplete, Verified: []UnitVerification{{Unit: 0, Passed: true, Check: "verify_cmd", Excerpt: "ok"}}},
			wantUnits: []PlanUnit{{Title: "a", HarnessPassed: true, VerifyCheck: "verify_cmd", VerifyExcerpt: "ok"}, {Title: "b"}},
			wantPhase: PhaseGenerate, wantEvents: []string{"mission.generate_continued"},
		},
		{
			name:  "phase_complete enters prove once every unit is harness-passed, Passes waits for approval",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, Units: []PlanUnit{{Title: "a", HarnessPassed: true}, {Title: "b"}}},
			input: StepInput{Input: InputPhaseComplete, Verified: []UnitVerification{{Unit: 1, Passed: true, Check: "verify_cmd", Excerpt: "ok"}}},
			wantUnits: []PlanUnit{{Title: "a", HarnessPassed: true}, {Title: "b", HarnessPassed: true, VerifyCheck: "verify_cmd", VerifyExcerpt: "ok"}},
			wantPhase: PhaseProve, wantEvents: []string{"mission.phase_started"},
		},
		{
			name: "review_approve flips every harness-passed unit and advances to result when all passed",
			state: StepState{Phase: PhaseProve, Status: StatusWorking, Units: []PlanUnit{
				{Title: "a", Passes: true, HarnessPassed: true}, {Title: "b", HarnessPassed: true},
			}},
			input:     StepInput{Input: InputReviewApprove, Verified: []UnitVerification{{Unit: 0, Passed: true, Check: "artifacts"}, {Unit: 1, Passed: true, Check: "artifacts"}}},
			wantUnits: []PlanUnit{{Title: "a", Passes: true, HarnessPassed: true, VerifyCheck: "artifacts"}, {Title: "b", Passes: true, HarnessPassed: true, VerifyCheck: "artifacts"}},
			wantPhase: PhaseResult, wantEvents: []string{"mission.phase_started"},
		},
		{
			name:      "review_approve never flips a unit without harness evidence",
			state:     StepState{Phase: PhaseProve, Status: StatusWorking, Units: []PlanUnit{{Title: "a", HarnessPassed: true}, {Title: "b"}}},
			input:     StepInput{Input: InputReviewApprove},
			wantUnits: []PlanUnit{{Title: "a", Passes: true, HarnessPassed: true}, {Title: "b"}},
			wantPhase: PhaseGenerate, wantEvents: []string{"mission.unit_verified"},
		},
		{
			name: "a passed unit failing again regresses to pending with the excerpt and an event",
			state: StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, Units: []PlanUnit{
				{Title: "a", Passes: true, HarnessPassed: true}, {Title: "b"},
			}},
			input: StepInput{Input: InputWorkerRetry, GapFingerprint: "regression:unit_0", Verified: []UnitVerification{
				{Unit: 0, Check: "artifacts", Excerpt: "a.md: not found"}, {Unit: 1, Passed: true, Check: "verify_cmd"},
			}},
			wantUnits: []PlanUnit{
				{Title: "a", Regressed: true, VerifyCheck: "artifacts", VerifyExcerpt: "a.md: not found"},
				{Title: "b", HarnessPassed: true, VerifyCheck: "verify_cmd"},
			},
			wantPhase: PhaseGenerate, wantEvents: []string{"mission.unit_regressed", "mission.retry"},
		},
		{
			name:      "a regressed unit passing again clears the regression marker",
			state:     StepState{Phase: PhaseGenerate, Status: StatusWorking, Units: []PlanUnit{{Title: "a", Regressed: true, VerifyCheck: "artifacts", VerifyExcerpt: "gone"}}},
			input:     StepInput{Input: InputPhaseComplete, Verified: []UnitVerification{{Unit: 0, Passed: true, Check: "verify_cmd", Excerpt: "ok"}}},
			wantUnits: []PlanUnit{{Title: "a", HarnessPassed: true, VerifyCheck: "verify_cmd", VerifyExcerpt: "ok"}},
			wantPhase: PhaseProve, wantEvents: []string{"mission.phase_started"},
		},
		{
			name:      "an out-of-range unit index is ignored",
			state:     StepState{Phase: PhaseGenerate, Status: StatusWorking, Units: []PlanUnit{{Title: "a"}}},
			input:     StepInput{Input: InputPhaseComplete, Verified: []UnitVerification{{Unit: 4, Passed: true}}},
			wantUnits: []PlanUnit{{Title: "a"}},
			wantPhase: PhaseGenerate, wantEvents: []string{"mission.generate_continued"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Step(tc.state, tc.input, DefaultConfig)
			if got.Next.Phase != tc.wantPhase {
				t.Fatalf("phase = %q, want %q", got.Next.Phase, tc.wantPhase)
			}
			if !reflect.DeepEqual(got.Next.Units, tc.wantUnits) {
				t.Fatalf("units = %+v, want %+v", got.Next.Units, tc.wantUnits)
			}
			kinds := make([]string, 0, len(got.Events))
			for _, ev := range got.Events {
				kinds = append(kinds, ev.Kind)
			}
			if !reflect.DeepEqual(kinds, tc.wantEvents) {
				t.Fatalf("events = %v, want %v", kinds, tc.wantEvents)
			}
		})
	}
}

// TestStepRegressionEventPayload pins what mission.unit_regressed
// carries: the unit index and title, the failing check, and a bounded
// excerpt, so the timeline names what broke without the full output.
func TestStepRegressionEventPayload(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, Units: []PlanUnit{{Title: "write a.md", Passes: true, HarnessPassed: true}}},
		StepInput{Input: InputWorkerRetry, Verified: []UnitVerification{{Unit: 0, Check: "verify_cmd", Excerpt: strings.Repeat("y", 600)}}},
		DefaultConfig,
	)
	if len(got.Events) == 0 || got.Events[0].Kind != "mission.unit_regressed" {
		t.Fatalf("events = %+v, want mission.unit_regressed first", got.Events)
	}
	p := got.Events[0].Payload
	if p["unit"] != 0 || p["title"] != "write a.md" || p["check"] != "verify_cmd" || len(p["excerpt"].(string)) > 510 {
		t.Fatalf("payload = %+v, want unit 0, the title, the check and a bounded excerpt", p)
	}
}

// TestStepReviewSkippedGoesResult confirms the non-coding review-skip
// fast path (routeVerified firing InputReviewApprove directly, driver.go)
// lands in result the same way an approved prove round does: the
// state machine can't distinguish the two, by design, so this is
// really the same case as review_approve on the last unit; kept
// separate as documentation of that fast path's contract.
func TestStepReviewSkippedGoesResult(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseGenerate, Status: StatusWorking},
		StepInput{Input: InputReviewApprove},
		DefaultConfig,
	)
	if got.Next.Phase != PhaseResult {
		t.Fatalf("Step(review-skip fast path) = %+v, want phase=result", got.Next)
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
		StepState{Phase: PhaseGenerate, Status: StatusWorking, Flow: FlowLight, MaxIterations: 8, Iteration: 0, StallCount: 1, LastGapFingerprint: "fp"},
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
		StepState{Phase: PhaseGenerate, Status: StatusWorking, Flow: FlowLight, MaxIterations: 1, Iteration: 0, StallCount: 1, LastGapFingerprint: "fp"},
		StepInput{Input: InputWorkerRetry, GapFingerprint: "fp", Reason: "stuck"},
		DefaultConfig,
	)
	if final.Next.Phase != PhaseFailed {
		t.Fatalf("Step(light stall at max_iterations) = %+v, want phase=failed", final.Next)
	}
}

// TestStepDiscoverGenerateStallNeverReplans mirrors
// TestStepLightStallNeverReplans for flow=discover_generate (D-090,
// issue #459): its generate turn never visits PhasePlan either, so the
// same replan-skip must apply.
func TestStepDiscoverGenerateStallNeverReplans(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseGenerate, Status: StatusWorking, Flow: FlowDiscoverGenerate, MaxIterations: 8, Iteration: 0, StallCount: 1, LastGapFingerprint: "fp"},
		StepInput{Input: InputWorkerRetry, GapFingerprint: "fp", Reason: "stuck"},
		DefaultConfig,
	)
	if got.Next.Phase == PhasePlan {
		t.Fatalf("Step(discover_generate stall) = %+v, must never route to PhasePlan", got.Next)
	}
	if got.Next.Status == StatusPaused {
		t.Fatalf("Step(discover_generate stall) = %+v, must not pause no_progress either", got.Next)
	}
}

func TestParsePhase(t *testing.T) {
	valid := []Phase{PhaseDiscover, PhasePlan, PhaseGenerate, PhaseProve, PhaseResult, PhaseDone, PhaseFailed}
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

// TestParsePhaseLegacyMapping is the table test for slice 1's legacy
// phase name tolerance: a row (or historical mission_events payload)
// still carrying the pre-rename explore/execute/review value must
// parse to its new-name equivalent, so a new binary reads old rows
// correctly before the data migration in scripts/pending-alters.md
// runs, and old event history keeps displaying after a rollback.
func TestParsePhaseLegacyMapping(t *testing.T) {
	cases := []struct {
		legacy string
		want   Phase
	}{
		{"explore", PhaseDiscover},
		{"execute", PhaseGenerate},
		{"review", PhaseProve},
	}
	for _, tc := range cases {
		got, ok := parsePhase(tc.legacy)
		if !ok {
			t.Fatalf("parsePhase(%q) ok = false, want true", tc.legacy)
		}
		if got != tc.want {
			t.Fatalf("parsePhase(%q) = %q, want %q", tc.legacy, got, tc.want)
		}
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
	nonTerminal := []Phase{PhaseDiscover, PhasePlan, PhaseGenerate, PhaseProve, PhaseResult}
	for _, p := range nonTerminal {
		if p.Terminal() {
			t.Fatalf("%q.Terminal() = true, want false", p)
		}
	}
}

// TestStepPlanAwaitingApprovalEmitsEvent confirms the park itself
// (D-087, issue #456) fires mission.plan_awaiting_approval. The
// notification inbox entry (Notifier.OnTransition) rides the generic
// idle/working -> paused transition this produces, no separate wiring
// needed.
func TestStepPlanAwaitingApprovalEmitsEvent(t *testing.T) {
	got := Step(
		StepState{Phase: PhasePlan, Status: StatusWorking},
		StepInput{Input: InputPhaseComplete},
		DefaultConfig,
	)
	if len(got.Events) != 1 || got.Events[0].Kind != "mission.plan_awaiting_approval" {
		t.Fatalf("Events = %+v, want exactly one mission.plan_awaiting_approval event", got.Events)
	}
}

// TestStepPlanApproveEmitsEvent confirms the approve verb's event.
func TestStepPlanApproveEmitsEvent(t *testing.T) {
	got := Step(
		StepState{Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval},
		StepInput{Input: InputPlanApprove},
		DefaultConfig,
	)
	if len(got.Events) != 1 || got.Events[0].Kind != "mission.plan_approved" {
		t.Fatalf("Events = %+v, want exactly one mission.plan_approved event", got.Events)
	}
}

// TestStepPlanReplanEventCarriesFeedbackPresence confirms the replan
// verb's event payload records only WHETHER feedback text was given
// (matching mission.blocked/mission.answered's own precedent for
// operator-authored text: full text does get stored, but the event's
// own "feedback" key is a presence bool the API layer decides, this
// tests both the with- and without-feedback shapes).
func TestStepPlanReplanEventCarriesFeedbackPresence(t *testing.T) {
	withFeedback := Step(
		StepState{Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval},
		StepInput{Input: InputPlanReplan, Reason: "use Go 1.23"},
		DefaultConfig,
	)
	if len(withFeedback.Events) != 1 || withFeedback.Events[0].Kind != "mission.plan_replan_requested" {
		t.Fatalf("Events = %+v, want exactly one mission.plan_replan_requested event", withFeedback.Events)
	}
	if got, _ := withFeedback.Events[0].Payload["feedback"].(bool); !got {
		t.Fatalf("payload[feedback] = %v, want true when Reason is set", withFeedback.Events[0].Payload)
	}
	if withFeedback.Next.ReplanUsed {
		t.Fatal("operator replan must not set ReplanUsed, that budget is only spent by an automatic stall replan")
	}

	noFeedback := Step(
		StepState{Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval},
		StepInput{Input: InputPlanReplan},
		DefaultConfig,
	)
	if got, _ := noFeedback.Events[0].Payload["feedback"].(bool); got {
		t.Fatalf("payload[feedback] = %v, want false when Reason is empty", noFeedback.Events[0].Payload)
	}
}

// TestStepPlanRediscoverEmitsEvent confirms the rediscover verb's event.
func TestStepPlanRediscoverEmitsEvent(t *testing.T) {
	got := Step(
		StepState{Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval},
		StepInput{Input: InputPlanRediscover},
		DefaultConfig,
	)
	if len(got.Events) != 1 || got.Events[0].Kind != "mission.rediscover_requested" {
		t.Fatalf("Events = %+v, want exactly one mission.rediscover_requested event", got.Events)
	}
}

// TestStepAskUserParksWithoutEvent confirms InputAskUser moves Status
// to waiting_for_input and leaves Phase untouched (D-088, issue #457):
// no event here since Store.SetPendingInput already appended
// mission.input_requested before this input reaches Step, same
// division of labor as pending_permission's own park.
func TestStepAskUserParksWithoutEvent(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseGenerate, Status: StatusWorking},
		StepInput{Input: InputAskUser},
		DefaultConfig,
	)
	if got.Next.Status != StatusWaitingForInput {
		t.Fatalf("Status = %s, want waiting_for_input", got.Next.Status)
	}
	if got.Next.Phase != PhaseGenerate {
		t.Fatalf("Phase = %s, want generate (unchanged)", got.Next.Phase)
	}
	if len(got.Events) != 0 {
		t.Fatalf("Events = %+v, want none: the store already recorded mission.input_requested", got.Events)
	}
}

// TestStepAskUserParksInEveryLLMPhase confirms the park applies
// uniformly regardless of which of the four LLM phases asked.
func TestStepAskUserParksInEveryLLMPhase(t *testing.T) {
	for _, phase := range []Phase{PhaseDiscover, PhasePlan, PhaseGenerate, PhaseProve} {
		got := Step(StepState{Phase: phase, Status: StatusWorking}, StepInput{Input: InputAskUser}, DefaultConfig)
		if got.Next.Status != StatusWaitingForInput || got.Next.Phase != phase {
			t.Fatalf("phase %s: Next = %+v, want same phase with status waiting_for_input", phase, got.Next)
		}
	}
}

// eventOfKind returns the first event of kind in events, or nil.
func eventOfKind(events []EventDraft, kind string) *EventDraft {
	for i := range events {
		if events[i].Kind == kind {
			return &events[i]
		}
	}
	return nil
}

// TestStepReworkAssignsFindingIDsInOrder pins the D-092 ledger's
// harness-owned fields: ids F1, F2 in order of appearance, the
// reviewed unit and round stamped on, status open, severity defaulting
// to blocking, and the model's own id/status ignored.
func TestStepReworkAssignsFindingIDsInOrder(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8},
		StepInput{Input: InputReviewRework, Unit: 2, Findings: []Finding{
			{ID: "model-made-up", Status: "resolved", Title: "missing validation", File: "x.go", Detail: "no input check"},
			{Title: "typo", Severity: SeverityMinor},
		}},
		DefaultConfig,
	)
	want := []Finding{
		{ID: "F1", Unit: 2, Title: "missing validation", File: "x.go", Detail: "no input check", Severity: SeverityBlocking, Status: FindingOpen, RoundOpened: 1},
		{ID: "F2", Unit: 2, Title: "typo", Severity: SeverityMinor, Status: FindingOpen, RoundOpened: 1},
	}
	if !reflect.DeepEqual(got.Next.ReviewFindings, want) {
		t.Fatalf("ReviewFindings = %+v, want %+v", got.Next.ReviewFindings, want)
	}
	if got.Next.ReworkRounds != 1 || got.Next.Phase != PhaseGenerate || got.Next.Status != StatusIdle {
		t.Fatalf("Next = %+v, want rework round 1 back in generate/idle", got.Next)
	}
	ev := eventOfKind(got.Events, "mission.review_verdict")
	if ev == nil || !reflect.DeepEqual(ev.Payload["open"], []string{"F1", "F2"}) || ev.Payload["round"] != 1 {
		t.Fatalf("review_verdict event = %+v, want open [F1 F2] round 1", got.Events)
	}
}

// TestStepReworkMatchesDuplicateFindings confirms a reworded repeat of
// an open finding (same file, same title words in another order and
// case) reuses its id and only refreshes the detail, while a genuinely
// new finding gets the next id; ids never recycle after a resolve.
func TestStepReworkMatchesDuplicateFindings(t *testing.T) {
	ledger := []Finding{
		{ID: "F1", Title: "missing validation", File: "x.go", Detail: "old detail", Severity: SeverityBlocking, Status: FindingOpen, RoundOpened: 1},
		{ID: "F2", Title: "typo", File: "y.go", Severity: SeverityMinor, Status: FindingResolved, RoundOpened: 1},
	}
	got := Step(
		StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8, ReworkRounds: 1, ReviewFindings: ledger},
		StepInput{Input: InputReviewRework, Findings: []Finding{
			{Title: "Validation, missing!", File: "./x.go", Detail: "new detail"},
			{Title: "typo", File: "y.go"},
			{Title: "missing validation", File: "z.go"},
		}},
		DefaultConfig,
	)
	fs := got.Next.ReviewFindings
	if len(fs) != 4 {
		t.Fatalf("ledger = %+v, want 4 findings (F1 matched, F2 stays resolved, F3/F4 new)", fs)
	}
	if fs[0].ID != "F1" || fs[0].Detail != "new detail" || fs[0].RoundOpened != 1 {
		t.Fatalf("F1 = %+v, want matched with refreshed detail and original round", fs[0])
	}
	if fs[1].Status != FindingResolved {
		t.Fatalf("resolved F2 was touched: %+v", fs[1])
	}
	if fs[2].ID != "F3" || fs[2].Title != "typo" || fs[2].RoundOpened != 2 {
		t.Fatalf("re-reported resolved finding must reopen under a new id: %+v", fs[2])
	}
	if fs[3].ID != "F4" || fs[3].File != "z.go" {
		t.Fatalf("different file must be a new finding: %+v", fs[3])
	}
	if ledger[0].Detail != "old detail" {
		t.Fatal("Step mutated the caller's ledger in place")
	}
}

// TestStepReworkResolvedFlipsStatus covers the reviewer's resolved
// list: named open ids flip to resolved, unknown ids are ignored, and
// a finding both re-reported and resolved in the same verdict ends
// resolved.
func TestStepReworkResolvedFlipsStatus(t *testing.T) {
	ledger := []Finding{
		{ID: "F1", Title: "a", File: "a.go", Severity: SeverityBlocking, Status: FindingOpen, RoundOpened: 1},
		{ID: "F2", Title: "b", File: "b.go", Severity: SeverityBlocking, Status: FindingOpen, RoundOpened: 1},
	}
	got := Step(
		StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8, ReworkRounds: 1, ReviewFindings: ledger},
		StepInput{Input: InputReviewRework, Resolved: []string{"F1", "F9"}, Findings: []Finding{{Title: "a", File: "a.go"}}},
		DefaultConfig,
	)
	fs := got.Next.ReviewFindings
	if len(fs) != 2 || fs[0].Status != FindingResolved || fs[1].Status != FindingOpen {
		t.Fatalf("ledger = %+v, want F1 resolved (resolve wins over re-report), F2 open", fs)
	}
	if ledger[0].Status != FindingOpen {
		t.Fatal("Step mutated the caller's ledger in place")
	}
}

// TestStepApproveResolvesAllAndResetsRounds: approval closes the cycle.
func TestStepApproveResolvesAllAndResetsRounds(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8, ReworkRounds: 2, ReviewFindings: []Finding{
			{ID: "F1", Title: "a", Status: FindingOpen},
			{ID: "F2", Title: "b", Status: FindingAccepted},
		}},
		StepInput{Input: InputReviewApprove},
		DefaultConfig,
	)
	if got.Next.ReworkRounds != 0 {
		t.Fatalf("ReworkRounds = %d, want 0", got.Next.ReworkRounds)
	}
	if fs := got.Next.ReviewFindings; fs[0].Status != FindingResolved || fs[1].Status != FindingAccepted {
		t.Fatalf("ledger after approve = %+v, want F1 resolved and F2 left accepted", fs)
	}
}

// TestStepPhaseCompleteKeepsReworkRounds is the regression test for the
// dead max_iterations brake: the worker's phase_complete used to reset
// Iteration, the only counter rework incremented, so a rework loop
// never reached the ceiling. ReworkRounds must survive it.
func TestStepPhaseCompleteKeepsReworkRounds(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 3, Iteration: 2, ReworkRounds: 2},
		StepInput{Input: InputPhaseComplete},
		DefaultConfig,
	)
	if got.Next.Phase != PhaseProve || got.Next.Iteration != 0 || got.Next.ReworkRounds != 2 {
		t.Fatalf("Next = %+v, want prove with Iteration 0 and ReworkRounds still 2", got.Next)
	}
	final := Step(got.Next, StepInput{Input: InputReviewRework}, DefaultConfig)
	if final.Next.PauseReason != PauseReviewExhausted {
		t.Fatalf("third rework after a phase_complete = %+v, want review_exhausted park", final.Next)
	}
}

// TestStepPhaseCompleteRoutesOnPendingUnits pins the D-096 routing: a
// generate completion stays in generate (counters reset, next unit
// named) while any unit lacks harness evidence and nothing is open,
// enters prove once every unit is harness-passed, and enters prove
// regardless when a finding is open (a rework must be re-reviewed).
// Legacy Passes-only rows count as verified.
func TestStepPhaseCompleteRoutesOnPendingUnits(t *testing.T) {
	pending := Step(
		StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, Iteration: 2, ConsecutiveFailures: 1, Units: []PlanUnit{
			{Title: "a", HarnessPassed: true}, {Title: "b"}, {Title: "c"},
		}},
		StepInput{Input: InputPhaseComplete}, DefaultConfig,
	)
	if pending.Next.Phase != PhaseGenerate || pending.Next.Status != StatusIdle || pending.Next.Iteration != 0 || pending.Next.ConsecutiveFailures != 0 {
		t.Fatalf("Next = %+v, want generate/idle with counters reset", pending.Next)
	}
	ev := eventOfKind(pending.Events, "mission.generate_continued")
	if ev == nil || ev.Payload["next_unit"] != 1 || ev.Payload["pending_units"] != 2 {
		t.Fatalf("events = %+v, want generate_continued naming unit 1 with 2 pending", pending.Events)
	}
	if eventOfKind(pending.Events, "mission.phase_started") != nil {
		t.Fatal("staying in generate must not emit phase_started")
	}

	allPassed := Step(
		StepState{Phase: PhaseGenerate, Status: StatusWorking, Units: []PlanUnit{{Title: "a", HarnessPassed: true}, {Title: "b", Passes: true}}},
		StepInput{Input: InputPhaseComplete}, DefaultConfig,
	)
	if allPassed.Next.Phase != PhaseProve || eventOfKind(allPassed.Events, "mission.phase_started") == nil {
		t.Fatalf("Next = %+v events %+v, want prove", allPassed.Next, allPassed.Events)
	}

	openFinding := Step(
		StepState{Phase: PhaseGenerate, Status: StatusWorking, Units: []PlanUnit{{Title: "a", HarnessPassed: true}, {Title: "b"}},
			ReviewFindings: []Finding{{ID: "F1", Title: "gap", Status: FindingOpen}}},
		StepInput{Input: InputPhaseComplete}, DefaultConfig,
	)
	if openFinding.Next.Phase != PhaseProve {
		t.Fatalf("Next = %+v, want prove: an open finding needs re-review even with a unit pending", openFinding.Next)
	}
}

// TestStepReworkRecordsReviewCommit pins D-096's anchor: a rework
// records the round's worktree HEAD as LastReviewCommit and its time as
// LastReviewAt (D-098), an input without them leaves the previous
// anchors alone, and approval keeps them (harmless: findings-only needs
// an open finding too).
func TestStepReworkRecordsReviewCommit(t *testing.T) {
	t1 := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	first := Step(
		StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8},
		StepInput{Input: InputReviewRework, ReviewCommit: "abc123", ReviewAt: t1, Findings: []Finding{{Title: "gap", File: "x.go"}}},
		DefaultConfig,
	)
	if first.Next.LastReviewCommit != "abc123" || !first.Next.LastReviewAt.Equal(t1) {
		t.Fatalf("LastReviewCommit = %q LastReviewAt = %v, want abc123 at %v", first.Next.LastReviewCommit, first.Next.LastReviewAt, t1)
	}
	second := Step(withStatus(first.Next, StatusWorking), StepInput{Input: InputReviewRework}, DefaultConfig)
	if second.Next.LastReviewCommit != "abc123" || !second.Next.LastReviewAt.Equal(t1) {
		t.Fatalf("anchors = %q %v after a rework without them, want abc123 at %v kept", second.Next.LastReviewCommit, second.Next.LastReviewAt, t1)
	}
	third := Step(withStatus(second.Next, StatusWorking), StepInput{Input: InputReviewRework, ReviewCommit: "def456", ReviewAt: t2}, DefaultConfig)
	if third.Next.LastReviewCommit != "def456" || !third.Next.LastReviewAt.Equal(t2) {
		t.Fatalf("anchors = %q %v, want def456 at %v", third.Next.LastReviewCommit, third.Next.LastReviewAt, t2)
	}
}

// TestStepPhaseCompleteScoresUntouchedFindings covers the worker-turn
// scoring: an open finding whose file the turn never touched counts a
// round and (if blocking) is named in mission.rework_untouched; a
// touched file resets the count; nil TouchedFiles (no worktree) leaves
// everything alone; a finding without a file is never scored.
func TestStepPhaseCompleteScoresUntouchedFindings(t *testing.T) {
	ledger := []Finding{
		{ID: "F1", Title: "a", File: "src/x.go", Severity: SeverityBlocking, Status: FindingOpen, UntouchedRounds: 1},
		{ID: "F2", Title: "b", File: "y.go", Severity: SeverityMinor, Status: FindingOpen},
		{ID: "F3", Title: "c", Severity: SeverityBlocking, Status: FindingOpen},
		{ID: "F4", Title: "d", File: "src/x.go", Severity: SeverityBlocking, Status: FindingResolved},
	}
	state := StepState{Phase: PhaseGenerate, Status: StatusWorking, MaxIterations: 8, ReviewFindings: ledger}

	untouched := Step(state, StepInput{Input: InputPhaseComplete, TouchedFiles: []string{"README.md"}}, DefaultConfig)
	fs := untouched.Next.ReviewFindings
	if fs[0].UntouchedRounds != 2 || fs[1].UntouchedRounds != 1 || fs[2].UntouchedRounds != 0 || fs[3].UntouchedRounds != 0 {
		t.Fatalf("untouched counts = %+v, want F1 2, F2 1, F3 0 (no file), F4 0 (resolved)", fs)
	}
	ev := eventOfKind(untouched.Events, "mission.rework_untouched")
	if ev == nil || !reflect.DeepEqual(ev.Payload["findings"], []string{"F1"}) {
		t.Fatalf("rework_untouched event = %+v, want only the blocking F1", untouched.Events)
	}
	if untouched.Events[len(untouched.Events)-1].Kind != "mission.phase_started" {
		t.Fatalf("phase_started must still be emitted: %+v", untouched.Events)
	}

	touched := Step(state, StepInput{Input: InputPhaseComplete, TouchedFiles: []string{"./src/x.go", "y.go"}}, DefaultConfig)
	if fs := touched.Next.ReviewFindings; fs[0].UntouchedRounds != 0 || fs[1].UntouchedRounds != 0 {
		t.Fatalf("touched files must reset the counts: %+v", fs)
	}
	if eventOfKind(touched.Events, "mission.rework_untouched") != nil {
		t.Fatal("no rework_untouched event when every finding's file was touched")
	}

	unknown := Step(state, StepInput{Input: InputPhaseComplete}, DefaultConfig)
	if !reflect.DeepEqual(unknown.Next.ReviewFindings, ledger) || eventOfKind(unknown.Events, "mission.rework_untouched") != nil {
		t.Fatalf("nil TouchedFiles must leave the ledger alone: %+v", unknown)
	}
	if ledger[0].UntouchedRounds != 1 {
		t.Fatal("Step mutated the caller's ledger in place")
	}
}

// TestStepReworkParksExhaustedWithIDsInDetail: the third rework at the
// default ceiling parks (never fails), in generate, naming every open
// finding by id and title.
func TestStepReworkParksExhaustedWithIDsInDetail(t *testing.T) {
	got := Step(
		StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 3, ReworkRounds: 2, ReviewFindings: []Finding{
			{ID: "F1", Title: "missing validation", File: "x.go", Severity: SeverityBlocking, Status: FindingOpen, RoundOpened: 1},
		}},
		StepInput{Input: InputReviewRework, Findings: []Finding{{Title: "no header toggle", File: "t.tsx"}}},
		DefaultConfig,
	)
	if got.Next.Phase != PhaseGenerate || got.Next.Status != StatusPaused || got.Next.PauseReason != PauseReviewExhausted {
		t.Fatalf("Next = %+v, want paused review_exhausted in generate", got.Next)
	}
	if got.Next.ReworkRounds != 3 {
		t.Fatalf("ReworkRounds = %d, want 3", got.Next.ReworkRounds)
	}
	ev := eventOfKind(got.Events, "mission.paused")
	if ev == nil || ev.Payload["reason"] != string(PauseReviewExhausted) {
		t.Fatalf("events = %+v, want mission.paused review_exhausted", got.Events)
	}
	if !reflect.DeepEqual(ev.Payload["findings"], []string{"F1", "F2"}) {
		t.Fatalf("paused findings = %v, want [F1 F2]", ev.Payload["findings"])
	}
	if detail, _ := ev.Payload["detail"].(string); !strings.Contains(detail, "F1 missing validation") || !strings.Contains(detail, "F2 no header toggle") {
		t.Fatalf("paused detail = %q, want both open findings named", detail)
	}
}

// TestStepReworkStallsOnUntouchedFile: a blocking finding untouched for
// StallRounds worker turns parks no_progress on the next rework, well
// below the rework ceiling; a finding resolved this round or a minor
// one never stalls the mission.
func TestStepReworkStallsOnUntouchedFile(t *testing.T) {
	stale := []Finding{
		{ID: "F1", Title: "missing validation", File: "x.go", Severity: SeverityBlocking, Status: FindingOpen, RoundOpened: 1, UntouchedRounds: 2},
	}
	stalled := Step(
		StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8, ReworkRounds: 2, ReviewFindings: stale},
		StepInput{Input: InputReviewRework},
		DefaultConfig,
	)
	if stalled.Next.Phase != PhaseGenerate || stalled.Next.PauseReason != PauseNoProgress {
		t.Fatalf("Next = %+v, want paused no_progress in generate", stalled.Next)
	}
	ev := eventOfKind(stalled.Events, "mission.paused")
	if ev == nil || !reflect.DeepEqual(ev.Payload["findings"], []string{"F1"}) {
		t.Fatalf("events = %+v, want mission.paused naming F1", stalled.Events)
	}
	if detail, _ := ev.Payload["detail"].(string); !strings.Contains(detail, "F1 missing validation") {
		t.Fatalf("paused detail = %q, want the stalled finding named", detail)
	}

	resolvedNow := Step(
		StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8, ReworkRounds: 2, ReviewFindings: stale},
		StepInput{Input: InputReviewRework, Resolved: []string{"F1"}},
		DefaultConfig,
	)
	if resolvedNow.Next.Status == StatusPaused {
		t.Fatalf("a finding resolved this round must not stall: %+v", resolvedNow.Next)
	}

	minor := Step(
		StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8, ReworkRounds: 1, ReviewFindings: []Finding{
			{ID: "F1", Title: "typo", File: "x.go", Severity: SeverityMinor, Status: FindingOpen, RoundOpened: 1, UntouchedRounds: 5},
		}},
		StepInput{Input: InputReviewRework},
		DefaultConfig,
	)
	if minor.Next.Status == StatusPaused {
		t.Fatalf("a minor finding must never stall the mission: %+v", minor.Next)
	}
}

// TestStepRouteChange covers the review-route rewrite (D-100, issue
// #536): legal only while paused, records old and new values, and
// leaves the mission paused for the operator's separate resume.
func TestStepRouteChange(t *testing.T) {
	paused := StepState{Phase: PhaseProve, Status: StatusPaused, PauseReason: PauseInfra, ReviewRoute: "old", ReviewRouteModel: "zai/glm-4.7"}
	got := Step(paused, StepInput{Input: InputRouteChange, ReviewRoute: "careful", ReviewRouteModel: "openai/gpt-5"}, DefaultConfig)
	want := paused
	want.ReviewRoute, want.ReviewRouteModel = "careful", "openai/gpt-5"
	if !reflect.DeepEqual(got.Next, want) {
		t.Fatalf("Next = %+v, want %+v", got.Next, want)
	}
	if len(got.Events) != 1 || got.Events[0].Kind != "mission.route_changed" {
		t.Fatalf("Events = %+v, want exactly one mission.route_changed", got.Events)
	}
	wantPayload := map[string]any{"from_route": "old", "to_route": "careful", "from_model": "zai/glm-4.7", "to_model": "openai/gpt-5"}
	if !reflect.DeepEqual(got.Events[0].Payload, wantPayload) {
		t.Fatalf("payload = %+v, want %+v", got.Events[0].Payload, wantPayload)
	}

	approval := StepState{Phase: PhasePlan, Status: StatusPaused, PauseReason: PauseApproval, ReviewRoute: "old"}
	if got := Step(approval, StepInput{Input: InputRouteChange, ReviewRoute: "careful"}, DefaultConfig); got.Next.ReviewRoute != "careful" || got.Next.PauseReason != PauseApproval {
		t.Fatalf("plan-approval park: Next = %+v, want route changed and still parked on approval", got.Next)
	}

	for _, tc := range []struct {
		name  string
		state StepState
		input StepInput
	}{
		{"working is a no-op", StepState{Phase: PhaseProve, Status: StatusWorking, ReviewRoute: "old"}, StepInput{Input: InputRouteChange, ReviewRoute: "careful"}},
		{"terminal is a no-op", StepState{Phase: PhaseDone, Status: StatusDone, ReviewRoute: "old"}, StepInput{Input: InputRouteChange, ReviewRoute: "careful"}},
		{"empty route is a no-op", paused, StepInput{Input: InputRouteChange}},
	} {
		got := Step(tc.state, tc.input, DefaultConfig)
		if !reflect.DeepEqual(got.Next, tc.state) || len(got.Events) != 0 {
			t.Fatalf("%s: got %+v / %+v, want unchanged state and no events", tc.name, got.Next, got.Events)
		}
	}
}

// TestStepReviewInfraFailureCarriesRoute confirms the pause payload
// names the route when the input carries one (D-100) and omits the key
// otherwise, so older readers see the same shape as before.
func TestStepReviewInfraFailureCarriesRoute(t *testing.T) {
	got := Step(StepState{Phase: PhaseProve, Status: StatusWorking}, StepInput{Input: InputReviewInfraFailure, Reason: "dead", Route: "careful"}, DefaultConfig)
	if r, _ := got.Events[0].Payload["route"].(string); r != "careful" {
		t.Fatalf("payload = %+v, want route=careful", got.Events[0].Payload)
	}
	got = Step(StepState{Phase: PhaseProve, Status: StatusWorking}, StepInput{Input: InputReviewInfraFailure, Reason: "dead"}, DefaultConfig)
	if _, ok := got.Events[0].Payload["route"]; ok {
		t.Fatalf("payload = %+v, want no route key", got.Events[0].Payload)
	}
}
