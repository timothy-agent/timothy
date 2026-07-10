package tools

import (
	"fmt"
	"slices"
)

// Registry holds the compiled-in tool set. Registration happens once
// at startup; lookups after that are read-only, so no locking.
type Registry struct {
	byName map[string]*Tool
	order  []string
}

func NewRegistry() *Registry {
	return &Registry{byName: map[string]*Tool{}}
}

// Register adds a tool. Duplicate names are a programming error and
// fail loudly at startup.
func (r *Registry) Register(t *Tool) error {
	if t.Name == "" {
		return fmt.Errorf("tools: register: empty name")
	}
	if _, ok := r.byName[t.Name]; ok {
		return fmt.Errorf("tools: register: duplicate tool %q", t.Name)
	}
	r.byName[t.Name] = t
	r.order = append(r.order, t.Name)
	return nil
}

func (r *Registry) Get(name string) (*Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// List returns tools in registration order.
func (r *Registry) List() []*Tool {
	out := make([]*Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.byName[name])
	}
	return out
}

// Without returns a filtered view for turns that must not see certain
// tools (e.g. dropping schemas at the step ceiling). The underlying
// tools are shared, not copied.
func (r *Registry) Without(names ...string) *Registry {
	out := NewRegistry()
	for _, name := range r.order {
		if slices.Contains(names, name) {
			continue
		}
		out.byName[name] = r.byName[name]
		out.order = append(out.order, name)
	}
	return out
}
