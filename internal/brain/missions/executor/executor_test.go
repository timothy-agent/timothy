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
