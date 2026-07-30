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
	Driver        string // "anthropic" | "openaicompat" | "bedrock"
	BaseURL       string
	CredentialRef string
	Headers       map[string]string
	Timeout       time.Duration
	// ReasoningEffort overrides per-request reasoning effort for the
	// openaicompat driver only (D-040); anthropic and bedrock ignore it.
	ReasoningEffort string
}

// Registry holds the built providers by name. It is immutable after
// Build: config reloads construct a whole new Registry inside a fresh
// snapshot and swap atomically — nothing mutates a live registry, so
// reads need no synchronization.
type Registry struct {
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
				ReasoningEffort: c.ReasoningEffort,
			})
		case "bedrock":
			// base_url holds the AWS region. D-047: if credential_ref
			// resolves in the secret store (key is non-empty), it MUST be
			// static-credentials JSON — parse failure fails provider
			// construction (config honesty), never a silent profile
			// fallback. If it does not resolve (no secrets row), the
			// unchanged prior behavior applies: credential_ref names a
			// local AWS profile (SSO dev), and must be EMPTY when an IAM
			// role supplies credentials — a missing profile fails client
			// setup.
			bedrockCfg := BedrockConfig{
				Name:    c.Name,
				Region:  c.BaseURL,
				Profile: c.CredentialRef,
				Timeout: c.Timeout,
			}
			if key != "" {
				sc, err := ParseStaticCredentials(key)
				if err != nil {
					return nil, fmt.Errorf("registry: provider %q: %w", c.Name, err)
				}
				bedrockCfg.StaticCredentials = sc
				bedrockCfg.Profile = ""
			}
			p = NewBedrock(bedrockCfg)
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

// Names returns all provider names (unordered).
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.providers))
	for n := range r.providers {
		names = append(names, n)
	}
	return names
}
