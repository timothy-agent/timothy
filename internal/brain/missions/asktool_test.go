package missions

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeAskUserParker scripts ParkAskUser for asktool tests, capturing
// every recorded park without a real Postgres pool.
type fakeAskUserParker struct {
	err    error
	parked []PendingInput
}

func (f *fakeAskUserParker) ParkAskUser(ctx context.Context, missionID string, input PendingInput) error {
	if f.err != nil {
		return f.err
	}
	f.parked = append(f.parked, input)
	return nil
}

// TestAskUserToolExecute table-tests every kind's valid path (parks and
// records) plus the invalid-argument and over-budget paths (plain tool
// error, never a park) for D-088, issue #457.
func TestAskUserToolExecute(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		asksUsed    int
		budget      int
		args        string
		wantErr     bool
		wantParked  bool
		wantDefault string
	}{
		{
			name: "mcq valid parks", asksUsed: 0, budget: 2,
			args:       `{"question":"which runtime?","kind":"mcq","options":["node","python"],"proposed_default":"node"}`,
			wantParked: true, wantDefault: "node",
		},
		{
			name: "yes_no valid parks", asksUsed: 0, budget: 2,
			args:       `{"question":"continue?","kind":"yes_no","proposed_default":"yes"}`,
			wantParked: true, wantDefault: "yes",
		},
		{
			name: "open valid parks", asksUsed: 0, budget: 2,
			args:       `{"question":"what should the title be?","kind":"open","proposed_default":"Untitled"}`,
			wantParked: true, wantDefault: "Untitled",
		},
		{
			name: "mcq default not in options errors without parking", asksUsed: 0, budget: 2,
			args:    `{"question":"which runtime?","kind":"mcq","options":["node","python"],"proposed_default":"rust"}`,
			wantErr: true,
		},
		{
			name: "mcq too few options errors without parking", asksUsed: 0, budget: 2,
			args:    `{"question":"which runtime?","kind":"mcq","options":["node"],"proposed_default":"node"}`,
			wantErr: true,
		},
		{
			name: "yes_no invalid default errors without parking", asksUsed: 0, budget: 2,
			args:    `{"question":"continue?","kind":"yes_no","proposed_default":"maybe"}`,
			wantErr: true,
		},
		{
			name: "missing question errors without parking", asksUsed: 0, budget: 2,
			args:    `{"kind":"open","proposed_default":"x"}`,
			wantErr: true,
		},
		{
			name: "unknown kind errors without parking", asksUsed: 0, budget: 2,
			args:    `{"question":"x?","kind":"multi_select","proposed_default":"x"}`,
			wantErr: true,
		},
		{
			name: "missing proposed_default errors without parking", asksUsed: 0, budget: 2,
			args:    `{"question":"x?","kind":"open"}`,
			wantErr: true,
		},
		{
			name: "over budget errors without parking", asksUsed: 2, budget: 2,
			args:    `{"question":"one more?","kind":"open","proposed_default":"no"}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parker := &fakeAskUserParker{}
			tool := AskUserTool("m1", PhaseGenerate, tc.asksUsed, tc.budget, parker)
			_, err := tool.Execute(context.Background(), json.RawMessage(tc.args))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Execute() error = %v, wantErr %v", err, tc.wantErr)
			}
			gotParked := len(parker.parked) == 1
			if gotParked != tc.wantParked {
				t.Fatalf("parked = %v, want %v", gotParked, tc.wantParked)
			}
			if tc.wantParked {
				if parker.parked[0].ProposedDefault != tc.wantDefault {
					t.Fatalf("ProposedDefault = %q, want %q", parker.parked[0].ProposedDefault, tc.wantDefault)
				}
				if parker.parked[0].Phase != PhaseGenerate {
					t.Fatalf("Phase = %q, want generate", parker.parked[0].Phase)
				}
			}
		})
	}
}

// TestAskUserToolExecuteParkerErrorNoPanic confirms a park failure
// (store write error) surfaces as a plain tool error, never a panic.
func TestAskUserToolExecuteParkerErrorNoPanic(t *testing.T) {
	t.Parallel()
	parker := &fakeAskUserParker{err: context.DeadlineExceeded}
	tool := AskUserTool("m1", PhasePlan, 0, 2, parker)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"x?","kind":"open","proposed_default":"y"}`))
	if err == nil {
		t.Fatal("Execute() error = nil, want an error when the parker fails")
	}
}

// TestAskBudgetFor confirms scheduler/workflow missions get zero budget
// (nobody is watching to answer), everyone else gets askBudget.
func TestAskBudgetFor(t *testing.T) {
	t.Parallel()
	if got := askBudgetFor(Mission{}); got != askBudget {
		t.Fatalf("askBudgetFor(plain mission) = %d, want %d", got, askBudget)
	}
	if got := askBudgetFor(Mission{ScheduleID: "sched1"}); got != 0 {
		t.Fatalf("askBudgetFor(scheduled) = %d, want 0", got)
	}
	if got := askBudgetFor(Mission{WorkflowRunID: "wf1"}); got != 0 {
		t.Fatalf("askBudgetFor(workflow) = %d, want 0", got)
	}
}
