package provider

import (
	"fmt"
	"sync"
	"time"
)

// Config mirrors one providers row; a later unit loads these from
// Postgres and hot-reloads on change. CredentialRef names an
// environment variable — never a raw secret (resolved at build time by
// the lookup function).
type Config struct {
	Name          string
	Kind          Kind
	Driver        string // "anthropic" | "openaicompat"
	BaseURL       string
	CredentialRef string
	Headers       map[string]string
	Timeout       time.Duration
}

// Registry holds the built providers by name. The mutex guards the
// map for the hot-reload writer that arrives with the DB-backed config
// store (Replace on poll/reload); today Build is the only writer and
// runs before any reader.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// Build constructs providers from configs. lookup resolves a
// credential reference to its secret value (os.Getenv in production,
// a fake in tests). A ref that resolves empty still builds the
// provider — health/routing layers decide whether to use it.
func Build(cfgs []Config, lookup func(string) string) (*Registry, error) {
	r := &Registry{providers: make(map[string]Provider, len(cfgs))}
	for _, c := range cfgs {
		if c.Name == "" {
			return nil, fmt.Errorf("registry: provider with empty name")
		}
		if _, dup := r.providers[c.Name]; dup {
			return nil, fmt.Errorf("registry: duplicate provider %q", c.Name)
		}

		key := ""
		if c.CredentialRef != "" {
			key = lookup(c.CredentialRef)
		}

		var p Provider
		switch c.Driver {
		case "anthropic":
			p = NewAnthropic(AnthropicConfig{
				Name: c.Name, BaseURL: c.BaseURL, APIKey: key,
				Headers: c.Headers, Timeout: c.Timeout,
			})
		case "openaicompat":
			if c.BaseURL == "" {
				return nil, fmt.Errorf("registry: provider %q: openaicompat requires base_url", c.Name)
			}
			p = NewOpenAICompat(OpenAICompatConfig{
				Name: c.Name, BaseURL: c.BaseURL, APIKey: key,
				Headers: c.Headers, Timeout: c.Timeout,
			})
		default:
			return nil, fmt.Errorf("registry: provider %q: unknown driver %q", c.Name, c.Driver)
		}
		r.providers[c.Name] = p
	}
	return r, nil
}

// Get returns the named provider.
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// Names returns all provider names (unordered).
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for n := range r.providers {
		names = append(names, n)
	}
	return names
}
