package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeAutomationStore records calls and serves fixed records.
type fakeAutomationStore struct {
	list    []AutomationRecord
	records map[string]AutomationRecord
	created []AutomationCreateInput
	patched map[string]AutomationPatchInput
	deleted []string
	getErr  error
}

func (f *fakeAutomationStore) ListAutomations(ctx context.Context) ([]AutomationRecord, error) {
	return f.list, nil
}

func (f *fakeAutomationStore) GetAutomation(ctx context.Context, idOrName string) (AutomationRecord, error) {
	if f.getErr != nil {
		return AutomationRecord{}, f.getErr
	}
	if a, ok := f.records[idOrName]; ok {
		return a, nil
	}
	for _, a := range f.records {
		if a.Name == idOrName {
			return a, nil
		}
	}
	return AutomationRecord{}, errors.New("not found")
}

func (f *fakeAutomationStore) CreateAutomation(ctx context.Context, in AutomationCreateInput) (string, error) {
	f.created = append(f.created, in)
	return "new-id", nil
}

func (f *fakeAutomationStore) PatchAutomation(ctx context.Context, idOrName string, p AutomationPatchInput) error {
	if f.patched == nil {
		f.patched = map[string]AutomationPatchInput{}
	}
	f.patched[idOrName] = p
	return nil
}

func (f *fakeAutomationStore) DeleteAutomation(ctx context.Context, idOrName string) error {
	f.deleted = append(f.deleted, idOrName)
	return nil
}

func TestListAutomations(t *testing.T) {
	store := &fakeAutomationStore{list: []AutomationRecord{{
		ID: "a1", Name: "Daily digest", Enabled: true, ActionKind: "mission",
		Triggers: []AutomationTriggerView{{Kind: "cron", Enabled: true}},
	}}}
	out, err := ListAutomations(store).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{`"id":"a1"`, `"name":"Daily digest"`, `"action":"mission"`, `"triggers":"cron"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %s, want %s", out, want)
		}
	}
}

func TestReadAutomation(t *testing.T) {
	store := &fakeAutomationStore{records: map[string]AutomationRecord{
		"a1": {ID: "a1", Name: "Digest", MissionGoal: "summarize", Triggers: []AutomationTriggerView{{Kind: "cron", Config: json.RawMessage(`{"expr":"0 8 * * *"}`), Enabled: true}}},
	}}
	out, err := ReadAutomation(store).Execute(context.Background(), json.RawMessage(`{"automation":"Digest"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, `"mission_goal":"summarize"`) || !strings.Contains(out, `"cron"`) {
		t.Fatalf("output = %s", out)
	}
}

func TestCreateAutomation(t *testing.T) {
	store := &fakeAutomationStore{}
	args := `{"name":"Watch releases","agent":"general","mission_goal":"post each new release","triggers":[{"kind":"cron","config":{"expr":"0 * * * *"}}]}`
	out, err := CreateAutomation(store).Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "new-id") {
		t.Fatalf("output = %s", out)
	}
	if len(store.created) != 1 {
		t.Fatalf("created %d, want 1", len(store.created))
	}
	in := store.created[0]
	if in.Name != "Watch releases" || in.AgentID != "general" || in.MissionGoal != "post each new release" {
		t.Fatalf("created input = %+v", in)
	}
	if in.MaxRunsPerHour != automationDefaultMaxRunsPerHour {
		t.Fatalf("max_runs_per_hour = %d, want default %d", in.MaxRunsPerHour, automationDefaultMaxRunsPerHour)
	}
	if !in.AutoApproveTools {
		t.Fatal("AutoApproveTools = false, want true (UI-created automations get standing approvals)")
	}
	if len(in.Triggers) != 1 || in.Triggers[0].Kind != "cron" {
		t.Fatalf("triggers = %+v", in.Triggers)
	}
}

func TestCreateAutomationRequiresFields(t *testing.T) {
	store := &fakeAutomationStore{}
	cases := []struct{ name, args, wantErr string }{
		{"no name", `{"agent":"general","mission_goal":"g","triggers":[{"kind":"manual","config":{}}]}`, "name is required"},
		{"no agent", `{"name":"n","mission_goal":"g","triggers":[{"kind":"manual","config":{}}]}`, "agent is required"},
		{"no goal", `{"name":"n","agent":"general","triggers":[{"kind":"manual","config":{}}]}`, "mission_goal is required"},
		{"no triggers", `{"name":"n","agent":"general","mission_goal":"g"}`, "at least one trigger"},
	}
	for _, tc := range cases {
		_, err := CreateAutomation(store).Execute(context.Background(), json.RawMessage(tc.args))
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s: err = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestUpdateAutomation(t *testing.T) {
	store := &fakeAutomationStore{}
	args := `{"automation":"Watch releases","enabled":false}`
	if _, err := UpdateAutomation(store).Execute(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	p, ok := store.patched["Watch releases"]
	if !ok {
		t.Fatal("no patch recorded")
	}
	if p.Enabled == nil || *p.Enabled {
		t.Fatalf("Enabled = %v, want false", p.Enabled)
	}
	if p.Name != nil || p.Triggers != nil {
		t.Fatalf("absent fields leaked into patch: %+v", p)
	}
}

func TestUpdateAutomationReplacesTriggers(t *testing.T) {
	store := &fakeAutomationStore{}
	args := `{"automation":"a1","triggers":[{"kind":"manual","config":{}}]}`
	if _, err := UpdateAutomation(store).Execute(context.Background(), json.RawMessage(args)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	p := store.patched["a1"]
	if p.Triggers == nil || len(*p.Triggers) != 1 || (*p.Triggers)[0].Kind != "manual" {
		t.Fatalf("Triggers = %v", p.Triggers)
	}
}

func TestDeleteAutomation(t *testing.T) {
	store := &fakeAutomationStore{}
	if _, err := DeleteAutomation(store).Execute(context.Background(), json.RawMessage(`{"automation":"Watch releases"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "Watch releases" {
		t.Fatalf("deleted = %v", store.deleted)
	}
	if _, err := DeleteAutomation(store).Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("empty automation accepted")
	}
}
