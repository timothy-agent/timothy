package missions

import "testing"

// TestPausePathsCarryCause pins D-136 (issue #1013): every Step path
// that parks a mission names its cause in the event payload.
func TestPausePathsCarryCause(t *testing.T) {
	budget := 1.0
	stale := []Finding{{ID: "F1", Title: "t", File: "x.go", Severity: SeverityBlocking, Status: FindingOpen, UntouchedRounds: 2}}
	build := StepState{Phase: PhaseBuild, Status: StatusWorking, MaxIterations: 8}
	cases := []struct {
		name      string
		state     StepState
		input     StepInput
		kind      string
		wantCause string
	}{
		{"mixed currency", StepState{Phase: PhaseBuild, Status: StatusWorking, Budget: &budget, MixedCurrencySpend: true}, StepInput{Input: InputWorkerRetry}, "mission.paused", CauseMixedCurrency},
		{"budget", StepState{Phase: PhaseBuild, Status: StatusWorking, Budget: &budget, Spent: 2}, StepInput{Input: InputWorkerRetry}, "mission.paused", CauseBudget},
		{"review infra", build, StepInput{Input: InputReviewInfraFailure, Reason: "gateway down"}, "mission.paused", CauseReviewInfra},
		{"model floor", build, StepInput{Input: InputReviewInfraFailure, Cause: CauseModelFloor, Model: "qwen2.5:7b", Reason: "below floor"}, "mission.paused", CauseModelFloor},
		{"review budget", build, StepInput{Input: InputReviewBudget, Reason: "cap"}, "mission.paused", CauseReviewBudget},
		{"harness retries exhausted on failure", StepState{Phase: PhaseBuild, Status: StatusWorking, MaxIterations: 8, HarnessRetries: 2}, StepInput{Input: InputWorkerFailed, HarnessCaused: true}, "mission.paused", CauseHarnessRetriesExhausted},
		{"harness retries exhausted on retry", StepState{Phase: PhasePlan, Status: StatusWorking, MaxIterations: 8, HarnessRetries: 2}, StepInput{Input: InputWorkerRetry, HarnessCaused: true}, "mission.paused", CauseHarnessRetriesExhausted},
		{"consecutive failures", StepState{Phase: PhaseBuild, Status: StatusWorking, MaxIterations: 8, ConsecutiveFailures: 2}, StepInput{Input: InputWorkerFailed}, "mission.paused", CauseConsecutiveFailures},
		{"stalled retries", StepState{Phase: PhaseBuild, Status: StatusWorking, MaxIterations: 8, Flow: FlowLight, StallCount: 1, LastGapFingerprint: "fp"}, StepInput{Input: InputWorkerRetry, HarnessCaused: true, GapFingerprint: "fp"}, "mission.paused", CauseStalledRetries},
		{"findings untouched", StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 8, ReviewFindings: stale}, StepInput{Input: InputReviewRework}, "mission.paused", CauseFindingsUntouched},
		{"review rounds exhausted", StepState{Phase: PhaseProve, Status: StatusWorking, MaxIterations: 1}, StepInput{Input: InputReviewRework}, "mission.paused", CauseReviewRoundsExhausted},
		{"result failed", StepState{Phase: PhaseResult, Status: StatusWorking}, StepInput{Input: InputResultFailed, Reason: "push failed"}, "mission.paused", CauseResultFailed},
		{"plan approval", StepState{Phase: PhasePlan, Status: StatusWorking}, StepInput{Input: InputPhaseComplete}, "mission.plan_awaiting_approval", CausePlanApproval},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := Step(tc.state, tc.input, DefaultConfig)
			ev := eventOfKind(tr.Events, tc.kind)
			if ev == nil {
				t.Fatalf("events = %+v, want a %s", tr.Events, tc.kind)
			}
			if ev.Payload["cause"] != tc.wantCause {
				t.Fatalf("cause = %v, want %q (payload %+v)", ev.Payload["cause"], tc.wantCause, ev.Payload)
			}
		})
	}
}

// TestPlanHarnessRetryPausePayload is the be8a2860 regression: harness
// retries spent in the plan phase pause as no_progress with the cause,
// the phase and the last rejection reason, so the UI can name them.
func TestPlanHarnessRetryPausePayload(t *testing.T) {
	tr := Step(
		StepState{Phase: PhasePlan, Status: StatusWorking, MaxIterations: 8, HarnessRetries: 2},
		StepInput{Input: InputWorkerRetry, HarnessCaused: true, Reason: "plan_invalid: unit 2 has no criteria"},
		DefaultConfig,
	)
	p := eventOfKind(tr.Events, "mission.paused").Payload
	if p["cause"] != CauseHarnessRetriesExhausted || p["phase"] != "plan" || p["harness_retries"] != 3 || p["detail"] != "plan_invalid: unit 2 has no criteria" {
		t.Fatalf("payload = %+v", p)
	}
}

// TestModelFloorPausePayload (issue #1090): a floor-hit pause names the
// model so the banner can say which one is below the mission floor.
func TestModelFloorPausePayload(t *testing.T) {
	tr := Step(
		StepState{Phase: PhaseBuild, Status: StatusWorking, MaxIterations: 8},
		StepInput{Input: InputReviewInfraFailure, Cause: CauseModelFloor, Model: "qwen2.5:7b", Reason: "mission turn served by a below-floor model: qwen2.5:7b"},
		DefaultConfig,
	)
	if tr.Next.Status != StatusPaused || tr.Next.PauseReason != PauseInfra {
		t.Fatalf("next = %s/%s, want paused/infra", tr.Next.Status, tr.Next.PauseReason)
	}
	p := eventOfKind(tr.Events, "mission.paused").Payload
	if p["cause"] != CauseModelFloor || p["model"] != "qwen2.5:7b" {
		t.Fatalf("payload = %+v", p)
	}
}
