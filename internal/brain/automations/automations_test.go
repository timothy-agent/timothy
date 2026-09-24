package automations

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	webhook := func(config, credentialRef string) Trigger {
		return Trigger{Kind: TriggerWebhook, Config: json.RawMessage(config), CredentialRef: credentialRef}
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
		{"webhook generic", func(a *Automation) { a.Triggers = []Trigger{webhook(`{"scheme":"generic"}`, "HOOK_SECRET")} }, "", false},
		{"webhook github with filters", func(a *Automation) {
			a.Triggers = []Trigger{webhook(`{"scheme":"github","filters":[{"path":"$.action","equals":"opened"},{"path":"pull_request.user.login","equals":"me"}]}`, "hooks/gh.key")}
		}, "", false},
		{"webhook allowlist", func(a *Automation) {
			a.Triggers = []Trigger{webhook(`{"scheme":"generic"}`, "HOOK_SECRET")}
			a.Triggers[0].ToolAllowlist = []string{"search_web"}
		}, "", false},
		{"webhook without config", func(a *Automation) { a.Triggers = []Trigger{webhook(``, "HOOK_SECRET")} }, "webhook trigger config", false},
		{"webhook unknown key", func(a *Automation) {
			a.Triggers = []Trigger{webhook(`{"scheme":"generic","secret":"x"}`, "HOOK_SECRET")}
		}, "webhook trigger config", false},
		{"webhook no scheme", func(a *Automation) { a.Triggers = []Trigger{webhook(`{}`, "HOOK_SECRET")} }, "scheme", false},
		{"webhook unknown scheme", func(a *Automation) { a.Triggers = []Trigger{webhook(`{"scheme":"slack"}`, "HOOK_SECRET")} }, "scheme", false},
		{"webhook without credential_ref", func(a *Automation) { a.Triggers = []Trigger{webhook(`{"scheme":"generic"}`, "")} }, "credential_ref", false},
		{"webhook credential_ref with spaces", func(a *Automation) { a.Triggers = []Trigger{webhook(`{"scheme":"generic"}`, "my secret")} }, "credential_ref", false},
		{"webhook eleven filters", func(a *Automation) {
			a.Triggers = []Trigger{webhook(`{"scheme":"generic","filters":[`+strings.TrimSuffix(strings.Repeat(`{"path":"a","equals":"b"},`, 11), ",")+`]}`, "HOOK_SECRET")}
		}, "at most 10 filters", false},
		{"webhook ten filters", func(a *Automation) {
			a.Triggers = []Trigger{webhook(`{"scheme":"generic","filters":[`+strings.TrimSuffix(strings.Repeat(`{"path":"a","equals":"b"},`, 10), ",")+`]}`, "HOOK_SECRET")}
		}, "", false},
		{"webhook array path", func(a *Automation) {
			a.Triggers = []Trigger{webhook(`{"scheme":"generic","filters":[{"path":"$.labels[0]","equals":"x"}]}`, "HOOK_SECRET")}
		}, "filter path", false},
		{"webhook empty path", func(a *Automation) {
			a.Triggers = []Trigger{webhook(`{"scheme":"generic","filters":[{"path":"","equals":"x"}]}`, "HOOK_SECRET")}
		}, "filter path", false},
		{"connector_event without config", func(a *Automation) { a.Triggers = []Trigger{{Kind: TriggerConnectorEvent}} }, "connector_event trigger config", false},
		{"credential_ref on connector_event", func(a *Automation) {
			a.Triggers = []Trigger{{Kind: TriggerConnectorEvent, CredentialRef: "X", Config: json.RawMessage(`{"connector_id":"` + agentID + `","repo":"o/r","events":["pr.opened"]}`)}}
		}, "credential_ref", false},
		{"channel", func(a *Automation) {
			a.Triggers = []Trigger{{Kind: TriggerChannel, Config: json.RawMessage(`{"channel_id":"` + agentID + `","pattern":"^/run coverage$"}`)}}
		}, "", false},
		{"channel without config", func(a *Automation) { a.Triggers = []Trigger{{Kind: TriggerChannel}} }, "channel trigger config", false},
		{"credential_ref on channel", func(a *Automation) {
			a.Triggers = []Trigger{{Kind: TriggerChannel, CredentialRef: "X", Config: json.RawMessage(`{"channel_id":"` + agentID + `","pattern":"go"}`)}}
		}, "credential_ref", false},
		{"unknown trigger kind", func(a *Automation) { a.Triggers = []Trigger{{Kind: "email"}} }, "unknown trigger kind", false},
		{"empty allowlist entry", func(a *Automation) { a.Triggers[0].ToolAllowlist = []string{"search_web", " "} }, "non-empty", false},
		{"allowlist too long", func(a *Automation) { a.Triggers[0].ToolAllowlist = make([]string, 65) }, "at most 64", false},
		{"allowlist entry with inner whitespace", func(a *Automation) { a.Triggers[0].ToolAllowlist = []string{"search web"} }, "whitespace", false},
		{"allowlist at the cap", func(a *Automation) {
			a.Triggers[0].ToolAllowlist = make([]string, 64)
			for i := range a.Triggers[0].ToolAllowlist {
				a.Triggers[0].ToolAllowlist[i] = fmt.Sprintf("tool_%d", i)
			}
		}, "", false},
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

func TestValidateConnectorEventConfig(t *testing.T) {
	t.Parallel()
	const id = "0B8F2F4E-6F1C-4B8A-9D2E-3C4B5A6D7E8F"
	for _, tc := range []struct {
		name, config, want, wantErr string
	}{
		{"canonical", `{"connector_id":"` + id + `","repo":"Timothy-Agent/Timothy","events":["pr.opened","pr.labeled","pr.opened"],"labels":[" timothy "]}`,
			`{"connector_id":"` + strings.ToLower(id) + `","repo":"timothy-agent/timothy","events":["pr.opened","pr.labeled"],"labels":["timothy"]}`, ""},
		{"no labels", `{"connector_id":"` + id + `","repo":"o/r.go","events":["check.completed"]}`,
			`{"connector_id":"` + strings.ToLower(id) + `","repo":"o/r.go","events":["check.completed"],"labels":[]}`, ""},
		{"all kinds", `{"connector_id":"` + id + `","repo":"o/r","events":["pr.opened","pr.labeled","pr.review","pr.review_comment","issue.comment","check.completed"]}`, "", ""},
		{"empty", ``, "", "config must be"},
		{"unknown field", `{"connector_id":"` + id + `","repo":"o/r","events":["pr.opened"],"branch":"main"}`, "", "config must be"},
		{"non-uuid connector", `{"connector_id":"github","repo":"o/r","events":["pr.opened"]}`, "", "UUID"},
		{"bad repo", `{"connector_id":"` + id + `","repo":"o/r/x","events":["pr.opened"]}`, "", "owner/name"},
		{"repo without owner", `{"connector_id":"` + id + `","repo":"r","events":["pr.opened"]}`, "", "owner/name"},
		{"no events", `{"connector_id":"` + id + `","repo":"o/r","events":[]}`, "", "must not be empty"},
		{"unknown event", `{"connector_id":"` + id + `","repo":"o/r","events":["pr.closed"]}`, "", "must be one of"},
		{"empty label", `{"connector_id":"` + id + `","repo":"o/r","events":["pr.opened"],"labels":["a"," "]}`, "", "non-empty"},
		{"eleven labels", `{"connector_id":"` + id + `","repo":"o/r","events":["pr.opened"],"labels":["1","2","3","4","5","6","7","8","9","10","11"]}`, "", "at most 10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := Trigger{Kind: TriggerConnectorEvent, Config: json.RawMessage(tc.config)}
			err := validateTrigger(&tr)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("validateTrigger = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateTrigger: %v", err)
			}
			if tc.want != "" && string(tr.Config) != tc.want {
				t.Fatalf("config = %s, want %s", tr.Config, tc.want)
			}
		})
	}
}

func TestConnectorEventIDs(t *testing.T) {
	t.Parallel()
	ts := []Trigger{
		{Kind: TriggerCron, Config: json.RawMessage(`{"expr":"0 9 * * *"}`)},
		{Kind: TriggerConnectorEvent, Config: json.RawMessage(`{"connector_id":"AA","repo":"o/r"}`)},
		{Kind: TriggerConnectorEvent, Config: json.RawMessage(`{"connector_id":"aa","repo":"o/s"}`)},
		{Kind: TriggerConnectorEvent, Config: json.RawMessage(`[`)},
		{Kind: TriggerConnectorEvent, Config: json.RawMessage(`{"connector_id":"bb"}`)},
	}
	if got := ConnectorEventIDs(ts); strings.Join(got, ",") != "aa,bb" {
		t.Fatalf("ConnectorEventIDs = %v, want [aa bb]", got)
	}
}

func TestValidateNormalizes(t *testing.T) {
	t.Parallel()
	a := validAutomation()
	a.Name = "  padded  "
	a.Triggers = []Trigger{{Kind: TriggerCron, Config: json.RawMessage(` { "expr" : "0 9 * * *" } `), ToolAllowlist: []string{" shell ", "read_note", "shell"}}, {Kind: TriggerManual, ToolAllowlist: []string{}}}
	if err := Validate(&a); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if a.Name != "padded" || string(a.Triggers[0].Config) != `{"expr":"0 9 * * *"}` || string(a.Triggers[1].Config) != `{}` {
		t.Fatalf("normalized name=%q configs=%s %s", a.Name, a.Triggers[0].Config, a.Triggers[1].Config)
	}
	if got := a.Triggers[0].ToolAllowlist; !slices.Equal(got, []string{"shell", "read_note"}) {
		t.Fatalf("normalized tool_allowlist = %v, want [shell read_note]", got)
	}
	if got := a.Triggers[1].ToolAllowlist; got == nil || len(got) != 0 {
		t.Fatalf("empty tool_allowlist = %#v, want an empty non-nil list", got)
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

func TestValidateWebhookConfigCanonical(t *testing.T) {
	t.Parallel()
	a := validAutomation()
	a.Triggers = []Trigger{{Kind: TriggerWebhook, CredentialRef: "HOOK_SECRET",
		Config: json.RawMessage(`{"scheme":"github","filters":[{"path":"action","equals":"opened"},{"path":"$.a.b","equals":""}]}`)}}
	if err := Validate(&a); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	want := `{"scheme":"github","filters":[{"path":"$.action","equals":"opened"},{"path":"$.a.b","equals":""}]}`
	if got := string(a.Triggers[0].Config); got != want {
		t.Fatalf("canonical config = %s, want %s", got, want)
	}
	a.Triggers[0].Config = json.RawMessage(`{"scheme":"generic"}`)
	if err := Validate(&a); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := string(a.Triggers[0].Config); got != `{"scheme":"generic","filters":[]}` {
		t.Fatalf("canonical config without filters = %s", got)
	}
}

func TestMatchWebhookFilters(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"opened","number":42,"draft":false,"pull_request":{"user":{"login":"octo"}},"labels":["x"],"empty":"","none":null}`)
	f := func(path, equals string) WebhookFilter { return WebhookFilter{Path: path, Equals: equals} }
	cases := []struct {
		name    string
		filters []WebhookFilter
		body    []byte
		want    bool
	}{
		{"no filters", nil, body, true},
		{"no filters non-JSON", nil, []byte("hello"), true},
		{"equals hit", []WebhookFilter{f("$.action", "opened")}, body, true},
		{"equals miss", []WebhookFilter{f("$.action", "closed")}, body, false},
		{"nested path", []WebhookFilter{f("$.pull_request.user.login", "octo")}, body, true},
		{"nested miss", []WebhookFilter{f("$.pull_request.user.login", "other")}, body, false},
		{"number as written", []WebhookFilter{f("$.number", "42")}, body, true},
		{"bool", []WebhookFilter{f("$.draft", "false")}, body, true},
		{"empty string value", []WebhookFilter{f("$.empty", "")}, body, true},
		{"missing path", []WebhookFilter{f("$.nope", "")}, body, false},
		{"null value", []WebhookFilter{f("$.none", "")}, body, false},
		{"object value", []WebhookFilter{f("$.pull_request", "")}, body, false},
		{"array value", []WebhookFilter{f("$.labels", `["x"]`)}, body, false},
		{"through a scalar", []WebhookFilter{f("$.action.x", "")}, body, false},
		{"all must hit", []WebhookFilter{f("$.action", "opened"), f("$.number", "7")}, body, false},
		{"both hit", []WebhookFilter{f("$.action", "opened"), f("$.number", "42")}, body, true},
		{"non-JSON body", []WebhookFilter{f("$.action", "opened")}, []byte("action=opened"), false},
		{"JSON array body", []WebhookFilter{f("$.action", "opened")}, []byte(`[{"action":"opened"}]`), false},
		{"empty body", []WebhookFilter{f("$.action", "opened")}, nil, false},
	}
	for _, tc := range cases {
		if got := MatchWebhookFilters(tc.filters, tc.body); got != tc.want {
			t.Errorf("%s: MatchWebhookFilters = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRecordAuthFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	var failures []time.Time
	for i := range HookAuthFailureLimit - 1 {
		var trip bool
		failures, trip = recordAuthFailure(failures, now.Add(time.Duration(i)*time.Second))
		if trip {
			t.Fatalf("failure %d tripped, want 20 to trip", i+1)
		}
	}
	if len(failures) != 19 {
		t.Fatalf("kept %d, want 19", len(failures))
	}
	kept, trip := recordAuthFailure(failures, now.Add(30*time.Second))
	if !trip || len(kept) != 20 {
		t.Fatalf("20th failure in the window: trip %v kept %d, want trip with 20", trip, len(kept))
	}
	// Past the window the first 19 fall out.
	kept, trip = recordAuthFailure(failures, now.Add(HookAuthFailureWindow+20*time.Second))
	if trip || len(kept) != 1 {
		t.Fatalf("after the window: trip %v kept %d, want 1 without trip", trip, len(kept))
	}
	// The list never grows past the limit.
	many := make([]time.Time, 40)
	for i := range many {
		many[i] = now
	}
	if kept, _ = recordAuthFailure(many, now); len(kept) != HookAuthFailureLimit {
		t.Fatalf("kept %d, want capped at %d", len(kept), HookAuthFailureLimit)
	}
}

func TestValidateChannelConfig(t *testing.T) {
	t.Parallel()
	const id = "0B8F2F4E-6F1C-4B8A-9D2E-3C4B5A6D7E8F"
	for _, tc := range []struct {
		name, config, want, wantErr string
	}{
		{"canonical", `{"channel_id":"` + id + `","pattern":"^/run (\\w+)$","chat_id":" -100 "}`,
			`{"channel_id":"` + strings.ToLower(id) + `","pattern":"^/run (\\w+)$","chat_id":"-100"}`, ""},
		{"no chat id", `{"channel_id":"` + id + `","pattern":"deploy"}`, `{"channel_id":"` + strings.ToLower(id) + `","pattern":"deploy"}`, ""},
		{"pattern at the cap", `{"channel_id":"` + id + `","pattern":"` + strings.Repeat("a", 200) + `"}`, "", ""},
		{"empty", ``, "", "config must be"},
		{"unknown field", `{"channel_id":"` + id + `","pattern":"x","agent":"y"}`, "", "config must be"},
		{"missing channel id", `{"pattern":"x"}`, "", "UUID"},
		{"non-uuid channel", `{"channel_id":"telegram","pattern":"x"}`, "", "UUID"},
		{"empty pattern", `{"channel_id":"` + id + `","pattern":""}`, "", "1 to 200"},
		{"pattern too long", `{"channel_id":"` + id + `","pattern":"` + strings.Repeat("a", 201) + `"}`, "", "1 to 200"},
		{"bad regex", `{"channel_id":"` + id + `","pattern":"(unclosed"}`, "", "pattern"},
		{"backreference is not RE2", `{"channel_id":"` + id + `","pattern":"(a)\\1"}`, "", "pattern"},
		{"chat id too long", `{"channel_id":"` + id + `","pattern":"x","chat_id":"` + strings.Repeat("1", 129) + `"}`, "", "chat_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := Trigger{Kind: TriggerChannel, Config: json.RawMessage(tc.config)}
			err := validateTrigger(&tr)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("validateTrigger = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateTrigger: %v", err)
			}
			if tc.want != "" && string(tr.Config) != tc.want {
				t.Fatalf("config = %s, want %s", tr.Config, tc.want)
			}
		})
	}
}

func TestMatchChannel(t *testing.T) {
	t.Parallel()
	re, err := CompileChannelPattern(`^/run coverage$`)
	if err != nil {
		t.Fatal(err)
	}
	already, err := CompileChannelPattern(`(?i)^deploy`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                    string
		re                      *regexp.Regexp
		triggerChat, chat, text string
		want                    bool
	}{
		{"exact", re, "", "1", "/run coverage", true},
		{"case-insensitive", re, "", "1", "/RUN Coverage", true},
		{"no match", re, "", "1", "/run tests", false},
		{"substring anchored", re, "", "1", "please /run coverage", false},
		{"chat filter hit", re, "-100", "-100", "/run coverage", true},
		{"chat filter miss", re, "-100", "42", "/run coverage", false},
		{"explicit (?i) kept", already, "", "1", "DEPLOY now", true},
		{"nil regexp", nil, "", "1", "/run coverage", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MatchChannel(tc.re, tc.triggerChat, tc.chat, tc.text); got != tc.want {
				t.Fatalf("MatchChannel = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestChannelIDs(t *testing.T) {
	t.Parallel()
	const id = "0B8F2F4E-6F1C-4B8A-9D2E-3C4B5A6D7E8F"
	ts := []Trigger{
		{Kind: TriggerChannel, Config: json.RawMessage(`{"channel_id":"` + id + `","pattern":"a"}`)},
		{Kind: TriggerChannel, Config: json.RawMessage(`{"channel_id":"` + strings.ToLower(id) + `","pattern":"b"}`)},
		{Kind: TriggerChannel, Config: json.RawMessage(`not json`)},
		{Kind: TriggerManual},
	}
	if got := ChannelIDs(ts); len(got) != 1 || got[0] != strings.ToLower(id) {
		t.Fatalf("ChannelIDs = %v", got)
	}
}
