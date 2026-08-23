package provider

import (
	"fmt"
	"time"
)

// Config mirrors one providers row; a later unit loads these from
// Postgres and hot-reloads on change. CredentialRef names an
// environment variable — never a raw secret (resolved at build time by
// the lookup function).
type Config struct {
	Name          string
	Kind          Kind
	Driver        string // "anthropic" | "openaicompat" | "openai-responses" | "bedrock"
	BaseURL       string
	CredentialRef string
	Headers       map[string]string
	Timeout       time.Duration
	// ReasoningEffort overrides per-request reasoning effort for the
	// openaicompat driver only (D-040); anthropic and bedrock ignore it.
	ReasoningEffort string
	// Region comes from options.region (D-048) — the bedrock driver's AWS
	// region; ignored by every other driver.
	Region string
}

// Registry holds the built providers by name. It is immutable after
// Build: config reloads construct a whole new Registry inside a fresh
// snapshot and swap atomically — nothing mutates a live registry, so
// reads need no synchronization.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry returns an empty registry — router.BuildSnapshot builds
// providers one at a time (per-provider degradation) and adds each
// survivor via Add.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
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
				ReasoningEffort: c.ReasoningEffort,
			})
		case "openai-responses":
			// D-067: the Responses API driver for reasoning-class OpenAI
			// models that return empty streams over chat/completions on
			// tool-mandatory turns.
			if c.BaseURL == "" {
				return nil, fmt.Errorf("registry: provider %q: openai-responses requires base_url", c.Name)
			}
			p = NewOpenAIResponses(OpenAIResponsesConfig{
				Name: c.Name, BaseURL: c.BaseURL, APIKey: key,
				Headers: c.Headers, Timeout: c.Timeout,
				ReasoningEffort: c.ReasoningEffort,
			})
		case "bedrock":
			// D-047/D-048: static keys in the secret store are the only
			// supported auth — AWS profile/SSO mode was removed (a
			// headless server has no ~/.aws). credential_ref MUST resolve
			// and parse as static-credentials JSON; either failure is a
			// Build error, never a silent fallback. Region comes from
			// options.region (c.Region), with the secret JSON's own
			// "region" field taking precedence when set (BedrockConfig
			// handles that precedence).
			if key == "" {
				return nil, fmt.Errorf("registry: provider %q: bedrock requires static keys in the secret store; AWS profile mode was removed", c.Name)
			}
			sc, err := ParseStaticCredentials(key)
			if err != nil {
				return nil, fmt.Errorf("registry: provider %q: %w", c.Name, err)
			}
			p = NewBedrock(BedrockConfig{
				Name:              c.Name,
				Region:            c.Region,
				StaticCredentials: sc,
				Timeout:           c.Timeout,
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
	p, ok := r.providers[name]
	return p, ok
}

// Add inserts a single built provider, overwriting any existing entry
// of the same name — router.BuildSnapshot builds providers one at a
// time for per-provider degradation and merges the survivors here.
func (r *Registry) Add(name string, p Provider) {
	r.providers[name] = p
}

// Names returns all provider names (unordered).
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.providers))
	for n := range r.providers {
		names = append(names, n)
	}
	return names
}
