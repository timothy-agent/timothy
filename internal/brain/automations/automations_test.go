package automations

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

const agentID = "0b8f2f4e-6f1c-4b8a-9d2e-3c4b5a6d7e8f"

func validAutomation() Automation {
	return Automation{
		Name: "daily digest", AgentID: agentID,
		Action:      Action{Kind: ActionMission, Mission: &missions.MissionTemplate{Goal: "g", Kind: missions.KindGeneral}},
		Concurrency: ConcurrencySkip, MaxConcurrent: 1, MaxRunsPerHour: 6, Enabled: true,
		Triggers: []Trigger{{Kind: TriggerCron, Config: json.RawMessage(`{"expr":"0 9 * * *"}`), Enabled: true}},
	}
}

func TestValidateName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, in, want, wantErr string
	}{
		{"trimmed", "  Daily Digest  ", "Daily Digest", ""},
		{"unicode 64 runes", strings.Repeat("é", 64), strings.Repeat("é", 64), ""},
		{"empty", "   ", "", "required"},
		{"65 runes", strings.Repeat("a", 65), "", "at most 64"},
		{"control char", "daily\ndigest", "", "control characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := validateName(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("validateName(%q) = %v, want %q", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("validateName(%q) = %q, %v, want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	cron := func(expr string) Trigger {
		return Trigger{Kind: TriggerCron, Config: json.RawMessage(`{"expr":"` + expr + `"}`)}
	}
	cases := []struct {
		name    string
		mutate  func(*Automation)
		wantErr string
		badCron bool
	}{
		{"valid", func(*Automation) {}, "", false},
		{"description too long", func(a *Automation) { a.Description = strings.Repeat("x", 1025) }, "description", false},
		{"non-uuid agent", func(a *Automation) { a.AgentID = "researcher" }, "agent_id", false},
		{"missing mission", func(a *Automation) { a.Action.Mission = nil }, "action.mission is required", false},
		{"workflow action", func(a *Automation) { a.Action = Action{Kind: ActionWorkflow, WorkflowID: agentID} }, "workflow actions are not available yet", false},
		{"unknown action kind", func(a *Automation) { a.Action.Kind = "shell" }, "action.kind", false},
		{"workflow_id on mission", func(a *Automation) { a.Action.WorkflowID = agentID }, "workflow_id", false},
		{"invalid template", func(a *Automation) { a.Action.Mission.Goal = "" }, "goal is required", false},
		{"zero triggers", func(a *Automation) { a.Triggers = nil }, "at least one trigger", false},
		{"six triggers", func(a *Automation) {
			a.Triggers = []Trigger{cron("0 9 * * *"), cron("0 10 * * *"), cron("0 11 * * *"), cron("0 12 * * *"), cron("0 13 * * *"), cron("0 14 * * *")}
		}, "at most 5", false},
		{"five triggers", func(a *Automation) {
			a.Triggers = []Trigger{cron("0 9 * * *"), cron("0 10 * * *"), cron("0 11 * * *"), cron("0 12 * * *"), {Kind: TriggerManual}}
		}, "", false},
		{"bad expr", func(a *Automation) { a.Triggers = []Trigger{cron("61 * * * *")} }, "invalid cron", true},
		{"empty expr", func(a *Automation) { a.Triggers = []Trigger{cron("")} }, "invalid cron", true},
		{"unknown config key", func(a *Automation) {
			a.Triggers = []Trigger{{Kind: TriggerCron, Config: json.RawMessage(`{"expr":"0 9 * * *","tz":"UTC"}`)}}
		}, "cron trigger config", false},
		{"cron without config", func(a *Automation) { a.Triggers = []Trigger{{Kind: TriggerCron}} }, "cron trigger config", false},
		{"credential_ref on cron", func(a *Automation) { a.Triggers[0].CredentialRef = "HOOK_SECRET" }, "credential_ref", false},
		{"credential_ref on manual", func(a *Automation) {
			a.Triggers = []Trigger{{Kind: TriggerManual, CredentialRef: "HOOK_SECRET"}}
		}, "credential_ref", false},
		{"manual with config", func(a *Automation) {
			a.Triggers = []Trigger{{Kind: TriggerManual, Config: json.RawMessage(`{"x":1}`)}}
		}, "no config", false},
		{"manual null config", func(a *Automation) { a.Triggers = []Trigger{{Kind: TriggerManual, Config: json.RawMessage(`null`)}} }, "", false},
		{"webhook not yet", func(a *Automation) { a.Triggers = []Trigger{{Kind: TriggerWebhook}} }, "not available yet", false},
		{"connector_event not yet", func(a *Automation) { a.Triggers = []Trigger{{Kind: TriggerConnectorEvent}} }, "not available yet", false},
		{"channel not yet", func(a *Automation) { a.Triggers = []Trigger{{Kind: TriggerChannel}} }, "not available yet", false},
		{"unknown trigger kind", func(a *Automation) { a.Triggers = []Trigger{{Kind: "email"}} }, "unknown trigger kind", false},
		{"empty allowlist entry", func(a *Automation) { a.Triggers[0].ToolAllowlist = []string{"search_web", " "} }, "non-empty", false},
		{"allowlist too long", func(a *Automation) { a.Triggers[0].ToolAllowlist = make([]string, 65) }, "at most 64", false},
		{"unknown concurrency", func(a *Automation) { a.Concurrency = "burst" }, "concurrency", false},
		{"max_concurrent 2 on skip", func(a *Automation) { a.MaxConcurrent = 2 }, "parallel", false},
		{"max_concurrent 2 on queue", func(a *Automation) { a.Concurrency, a.MaxConcurrent = ConcurrencyQueue, 2 }, "parallel", false},
		{"max_concurrent 3 on parallel", func(a *Automation) { a.Concurrency, a.MaxConcurrent = ConcurrencyParallel, 3 }, "", false},
		{"max_concurrent 4", func(a *Automation) { a.Concurrency, a.MaxConcurrent = ConcurrencyParallel, 4 }, "between 1 and 3", false},
		{"max_concurrent 0", func(a *Automation) { a.MaxConcurrent = 0 }, "between 1 and 3", false},
		{"max_runs_per_hour 0", func(a *Automation) { a.MaxRunsPerHour = 0 }, "between 1 and 60", false},
		{"max_runs_per_hour 61", func(a *Automation) { a.MaxRunsPerHour = 61 }, "between 1 and 60", false},
		{"max_runs_per_hour 60", func(a *Automation) { a.MaxRunsPerHour = 60 }, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := validAutomation()
			tc.mutate(&a)
			err := Validate(&a)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			var ve *ValidationError
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !errors.As(err, &ve) {
				t.Fatalf("Validate = %v, want a ValidationError mentioning %q", err, tc.wantErr)
			}
			if errors.Is(err, ErrBadCron) != tc.badCron {
				t.Fatalf("errors.Is(ErrBadCron) = %v, want %v", !tc.badCron, tc.badCron)
			}
		})
	}
}

func TestValidateNormalizes(t *testing.T) {
	t.Parallel()
	a := validAutomation()
	a.Name = "  padded  "
	a.Triggers = []Trigger{{Kind: TriggerCron, Config: json.RawMessage(` { "expr" : "0 9 * * *" } `)}, {Kind: TriggerManual}}
	if err := Validate(&a); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if a.Name != "padded" || string(a.Triggers[0].Config) != `{"expr":"0 9 * * *"}` || string(a.Triggers[1].Config) != `{}` {
		t.Fatalf("normalized name=%q configs=%s %s", a.Name, a.Triggers[0].Config, a.Triggers[1].Config)
	}
}

func TestValidateNote(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, note, content string
		ok                  bool
	}{
		{"valid", "last_seen-1", "x", true},
		{"64 chars", strings.Repeat("a", 64), "", true},
		{"4096 bytes", "n", strings.Repeat("a", 4096), true},
		{"4097 bytes", "n", strings.Repeat("a", 4097), false},
		{"multibyte over", "n", strings.Repeat("é", 2049), false},
		{"uppercase", "Notes", "x", false},
		{"space", "my note", "x", false},
		{"empty", "", "x", false},
		{"65 chars", strings.Repeat("a", 65), "x", false},
	} {
		err := ValidateNote(tc.note, tc.content)
		if (err == nil) != tc.ok {
			t.Fatalf("%s: ValidateNote = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestValidateCron(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		expr string
		ok   bool
	}{
		{"0 9 * * *", true}, {"*/5 * * * *", true}, {"0 8 * * 1-5", true},
		{"", false}, {"not a cron expression", false}, {"* * *", false}, {"0 9 * * * *", false},
	} {
		err := ValidateCron(tc.expr)
		if (err == nil) != tc.ok || (err != nil && !errors.Is(err, ErrBadCron)) {
			t.Fatalf("ValidateCron(%q) = %v, want ok=%v", tc.expr, err, tc.ok)
		}
	}
	if !NextRun("garbage", time.Now()).IsZero() {
		t.Fatal("NextRun on an invalid expression must be zero")
	}
}

// TestNextRunDST is the DST regression: a local 09:00 cron keeps its
// wall-clock hour across both Europe/Amsterdam transitions, and a UTC
// anchor does not shift the local hour.
func TestNextRunDST(t *testing.T) {
	t.Parallel()
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	for _, tc := range []struct {
		name   string
		anchor time.Time
		want   string
	}{
		{"spring forward", time.Date(2026, 3, 28, 9, 0, 0, 0, ams), "2026-03-29T09:00:00+02:00"},
		{"fall back", time.Date(2026, 10, 24, 9, 0, 0, 0, ams), "2026-10-25T09:00:00+01:00"},
		{"utc anchor spring", time.Date(2026, 3, 28, 8, 0, 0, 0, time.UTC).In(ams), "2026-03-29T09:00:00+02:00"},
		{"utc anchor fall", time.Date(2026, 10, 24, 7, 0, 0, 0, time.UTC).In(ams), "2026-10-25T09:00:00+01:00"},
	} {
		got := NextRun("0 9 * * *", tc.anchor).Format(time.RFC3339)
		if got != tc.want {
			t.Fatalf("%s: NextRun = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestNextFire(t *testing.T) {
	t.Parallel()
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	created := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	base := func() Automation {
		a := validAutomation()
		a.CreatedAt = created
		a.Triggers = []Trigger{
			{Kind: TriggerCron, Config: json.RawMessage(`{"expr":"0 9 * * *"}`), State: json.RawMessage(`{"last_fired_at":"2026-09-23T07:00:00Z"}`), Enabled: true},
			{Kind: TriggerCron, Config: json.RawMessage(`{"expr":"30 8 * * *"}`), State: json.RawMessage(`{}`), Enabled: true},
			{Kind: TriggerManual, Config: json.RawMessage(`{}`), Enabled: true},
		}
		return a
	}
	got := NextFire(base(), now, ams)
	// The second trigger anchors on created_at: 08:30 Amsterdam on Sept 1.
	if got == nil || !got.Equal(time.Date(2026, 9, 1, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("NextFire = %v, want 2026-09-01T06:30Z", got)
	}
	a := base()
	a.Triggers[1].Enabled = false
	if got := NextFire(a, now, ams); got == nil || !got.Equal(time.Date(2026, 9, 24, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("NextFire with one cron trigger = %v, want 2026-09-24T07:00Z", got)
	}
	a = base()
	a.Enabled = false
	if NextFire(a, now, ams) != nil {
		t.Fatal("disabled automation must have no next run")
	}
	a = base()
	expired := now.Add(-time.Minute)
	a.ExpiresAt = &expired
	if NextFire(a, now, ams) != nil {
		t.Fatal("expired automation must have no next run")
	}
	a = base()
	a.Triggers = a.Triggers[2:]
	if NextFire(a, now, ams) != nil {
		t.Fatal("manual-only automation must have no next run")
	}
}

func TestPatchApply(t *testing.T) {
	t.Parallel()
	exp := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	a := validAutomation()
	a.ExpiresAt = &exp
	if got := (Patch{}).apply(a); got.ExpiresAt == nil || got.Name != a.Name {
		t.Fatal("empty patch changed the automation")
	}
	var cleared *time.Time
	if got := (Patch{ExpiresAt: &cleared}).apply(a); got.ExpiresAt != nil {
		t.Fatal("inner nil expires_at must clear")
	}
	name, enabled := "renamed", false
	triggers := []Trigger{{Kind: TriggerManual}}
	got := Patch{Name: &name, Enabled: &enabled, Triggers: &triggers}.apply(a)
	if got.Name != "renamed" || got.Enabled || len(got.Triggers) != 1 || got.Triggers[0].Kind != TriggerManual {
		t.Fatalf("apply = %+v", got)
	}
}
