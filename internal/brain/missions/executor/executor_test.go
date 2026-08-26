package executor

import "testing"

// fakeAdapter is a minimal Adapter for registry tests, distinct from
// claudeAdapter so registering it never collides with the real adapter's
// init() registration.
type fakeAdapter struct{ name string }

func (f fakeAdapter) Harness() string          { return f.name }
func (fakeAdapter) Capabilities() Capabilities { return Capabilities{} }
func (fakeAdapter) BuildInvocation(InvocationSpec) (Invocation, error) {
	return Invocation{}, nil
}
func (fakeAdapter) NewParser() StreamParser          { return nil }
func (fakeAdapter) ParseResult(Event) (Result, bool) { return Result{}, false }

func TestRegister_Duplicate_Panics(t *testing.T) {
	Register(fakeAdapter{name: "fake-dup-test"})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate Register, got none")
		}
	}()
	Register(fakeAdapter{name: "fake-dup-test"})
}

func TestLookup(t *testing.T) {
	if _, ok := Lookup("claude-cli"); !ok {
		t.Fatal("claude-cli adapter not registered")
	}
	if _, ok := Lookup("does-not-exist"); ok {
		t.Fatal("expected ok=false for unregistered harness")
	}
}

// TestKindSystemModelOnlyWhereReported proves Event.Model is populated
// only by harnesses whose init line actually names a model. The other
// three carry a different identifier in KindSystem's Text (codex a
// thread id, opencode a session id, pi a cwd), and must leave Model
// empty so recordLedger keeps falling back to the route entry's model
// instead of booking usage against a thread id.
func TestKindSystemModelOnlyWhereReported(t *testing.T) {
	cases := []struct {
		name      string
		parser    StreamParser
		line      string
		wantModel string
	}{
		{"claude reports its model", claudeAdapter{}.NewParser(),
			`{"type":"system","subtype":"init","model":"claude-sonnet-5"}`, "claude-sonnet-5"},
		{"cursor reports its model", cursorAdapter{}.NewParser(),
			`{"type":"system","subtype":"init","model":"claude-4.5-sonnet"}`, "claude-4.5-sonnet"},
		{"codex reports a thread id, not a model", codexAdapter{}.NewParser(),
			`{"type":"thread.started","thread_id":"th_123"}`, ""},
		{"opencode reports a session id, not a model", opencodeAdapter{}.NewParser(),
			`{"type":"step_start","sessionID":"ses_123"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := tc.parser.ParseLine([]byte(tc.line))
			if !ok {
				t.Fatalf("line failed to parse: %s", tc.line)
			}
			if ev.Kind != KindSystem {
				t.Fatalf("Kind = %q, want %q", ev.Kind, KindSystem)
			}
			if ev.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", ev.Model, tc.wantModel)
			}
		})
	}
}
