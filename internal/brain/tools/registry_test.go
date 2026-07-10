package tools

import (
	"testing"
)

func reg(t *testing.T, names ...string) *Registry {
	t.Helper()
	r := NewRegistry()
	for _, n := range names {
		if err := r.Register(&Tool{Name: n, Description: n}); err != nil {
			t.Fatalf("register %s: %v", n, err)
		}
	}
	return r
}

func TestRegistryRegisterAndGet(t *testing.T) {
	t.Parallel()
	r := reg(t, "alpha", "beta")

	got, ok := r.Get("alpha")
	if !ok || got.Name != "alpha" {
		t.Fatalf("Get(alpha) = %v, %v", got, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("Get(missing) found a tool")
	}
}

func TestRegistryRejectsDuplicatesAndEmptyNames(t *testing.T) {
	t.Parallel()
	r := reg(t, "alpha")
	if err := r.Register(&Tool{Name: "alpha"}); err == nil {
		t.Fatal("duplicate register succeeded")
	}
	if err := r.Register(&Tool{}); err == nil {
		t.Fatal("empty-name register succeeded")
	}
}

func TestRegistryListKeepsRegistrationOrder(t *testing.T) {
	t.Parallel()
	r := reg(t, "charlie", "alpha", "beta")
	var names []string
	for _, tool := range r.List() {
		names = append(names, tool.Name)
	}
	want := []string{"charlie", "alpha", "beta"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("List order = %v, want %v", names, want)
		}
	}
}

func TestRegistryWithout(t *testing.T) {
	t.Parallel()
	r := reg(t, "alpha", "beta", "gamma")
	filtered := r.Without("beta")

	if _, ok := filtered.Get("beta"); ok {
		t.Fatal("Without(beta) still has beta")
	}
	if got := len(filtered.List()); got != 2 {
		t.Fatalf("filtered list has %d tools, want 2", got)
	}
	// The original registry is untouched.
	if got := len(r.List()); got != 3 {
		t.Fatalf("original list has %d tools, want 3", got)
	}
}
