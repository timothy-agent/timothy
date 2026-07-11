// Package router owns the gateway's data-driven configuration: it
// loads providers and task routes from Postgres into an immutable
// snapshot, resolves a request to an ordered list of attempts, and
// hot-reloads on a poll or an explicit trigger.
package router

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

// ModelPrices are USD per million tokens. Absent prices mean cost is
// unknown and recorded as null — never guessed.
type ModelPrices struct {
	InputPerMTok      float64 `json:"input_per_mtok"`
	OutputPerMTok     float64 `json:"output_per_mtok"`
	CacheReadPerMTok  float64 `json:"cache_read_per_mtok"`
	CacheWritePerMTok float64 `json:"cache_write_per_mtok"`
}

// ModelInfo is one entry of a provider's models jsonb.
type ModelInfo struct {
	ID            string       `json:"id"`
	ContextWindow int          `json:"context_window,omitempty"`
	Capabilities  []string     `json:"capabilities,omitempty"`
	Prices        *ModelPrices `json:"prices,omitempty"`
}

// ProviderRow mirrors one providers table row.
type ProviderRow struct {
	ID            string
	Name          string
	Kind          string
	Driver        string
	BaseURL       string
	DefaultModel  string
	Models        []ModelInfo
	CredentialRef string
	Headers       map[string]string
	Enabled       bool
}

// ChainEntry is one step of a route chain.
type ChainEntry struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
}

// RouteRow mirrors one task_routes table row.
type RouteRow struct {
	TaskCategory string
	Chain        []ChainEntry
	Enabled      bool
}

// Snapshot is an immutable view of the routing configuration plus the
// providers built from it. Store swaps whole snapshots atomically.
type Snapshot struct {
	rows     map[string]ProviderRow // by id
	byName   map[string]ProviderRow
	routes   map[string][]ChainEntry // enabled routes only
	registry *provider.Registry
	healthy  map[string]bool // by name: credential ref resolved
}

// Attempt is one provider+model candidate, in try order.
type Attempt struct {
	Provider     provider.Provider
	ProviderName string
	Model        string
}

// NoRouteError reports that resolution produced zero usable attempts.
type NoRouteError struct {
	Category string
	Hint     string
	Skipped  []string // "name (reason)" for every candidate rejected
}

func (e *NoRouteError) Error() string {
	msg := fmt.Sprintf("no usable provider for category %q", e.Category)
	if e.Hint != "" {
		msg += fmt.Sprintf(" (hint %q)", e.Hint)
	}
	if len(e.Skipped) > 0 {
		msg += ": " + strings.Join(e.Skipped, "; ")
	}
	return msg
}

// BuildSnapshot assembles a snapshot from table rows. lookup resolves
// credential references; a ref that resolves empty marks the provider
// unhealthy (skipped by routing) without failing the build.
func BuildSnapshot(provRows []ProviderRow, routeRows []RouteRow, lookup func(string) string) (*Snapshot, error) {
	s := &Snapshot{
		rows:    make(map[string]ProviderRow, len(provRows)),
		byName:  make(map[string]ProviderRow, len(provRows)),
		routes:  map[string][]ChainEntry{},
		healthy: make(map[string]bool, len(provRows)),
	}

	cfgs := make([]provider.Config, 0, len(provRows))
	for _, row := range provRows {
		s.rows[row.ID] = row
		s.byName[row.Name] = row
		s.healthy[row.Name] = row.CredentialRef == "" || lookup(row.CredentialRef) != ""
		cfgs = append(cfgs, provider.Config{
			Name:          row.Name,
			Kind:          provider.Kind(row.Kind),
			Driver:        row.Driver,
			BaseURL:       row.BaseURL,
			CredentialRef: row.CredentialRef,
			Headers:       row.Headers,
		})
	}

	reg, err := provider.Build(cfgs, lookup)
	if err != nil {
		return nil, err
	}
	s.registry = reg

	for _, r := range routeRows {
		if r.Enabled {
			s.routes[r.TaskCategory] = r.Chain
		}
	}
	return s, nil
}

// requiredCapability maps a task category to the driver capability its
// attempts must declare (D-005: routing never assumes an undeclared
// capability).
func requiredCapability(category string) provider.Capability {
	if category == "embedding" {
		return provider.CapEmbeddings
	}
	return provider.CapChat
}

// Resolve returns the ordered attempts for a category and optional
// hint. Hint semantics: a provider name routes to that provider's
// default model; an exact model id routes to the first enabled healthy
// provider listing it. A failed hint falls through to the chain.
// Disabled, unhealthy, and capability-lacking providers are skipped;
// each chain entry is tried at most once.
func (s *Snapshot) Resolve(category, hint string) ([]Attempt, error) {
	required := requiredCapability(category)
	var attempts []Attempt
	var skipped []string
	seen := map[string]bool{} // "name/model" dedupe across hint + chain

	add := func(row ProviderRow, model, source string) {
		p, inRegistry := s.registry.Get(row.Name)
		switch {
		case !row.Enabled:
			skipped = append(skipped, fmt.Sprintf("%s (disabled, %s)", row.Name, source))
		case !s.healthy[row.Name]:
			skipped = append(skipped, fmt.Sprintf("%s (unhealthy: credential %s unresolved, %s)", row.Name, row.CredentialRef, source))
		case !inRegistry:
			skipped = append(skipped, fmt.Sprintf("%s (not in registry, %s)", row.Name, source))
		case !attemptCapable(p, row, model, required):
			skipped = append(skipped, fmt.Sprintf("%s/%s (lacks %s capability, %s)", row.Name, model, required, source))
		default:
			key := row.Name + "/" + model
			if !seen[key] {
				seen[key] = true
				attempts = append(attempts, Attempt{Provider: p, ProviderName: row.Name, Model: model})
			}
		}
	}

	if hint != "" {
		if row, ok := s.byName[hint]; ok && row.DefaultModel != "" {
			add(row, row.DefaultModel, "hint")
		} else if row, model, ok := s.findModel(hint); ok {
			add(row, model, "hint")
		} else {
			skipped = append(skipped, fmt.Sprintf("hint %q matched nothing", hint))
		}
	}

	for _, entry := range s.routes[category] {
		row, ok := s.rows[entry.ProviderID]
		if !ok {
			skipped = append(skipped, fmt.Sprintf("chain entry %s (unknown provider id)", entry.ProviderID))
			continue
		}
		model := entry.Model
		if model == "" {
			model = row.DefaultModel
		}
		add(row, model, "chain")
	}

	if len(attempts) == 0 {
		return nil, &NoRouteError{Category: category, Hint: hint, Skipped: skipped}
	}
	return attempts, nil
}

// attemptCapable gates one provider+model candidate on the required
// capability: the DRIVER must implement it (the integration code) AND
// the model's own declaration in the models jsonb, when present, must
// list it — so an embeddings-only model in a chat chain is skipped
// before any wire call even though its driver can chat. Models with
// no declaration (or unlisted models, e.g. a hint for something not
// yet in the table) are judged by the driver alone (D-005).
func attemptCapable(p provider.Provider, row ProviderRow, model string, want provider.Capability) bool {
	if !hasCapability(p, want) {
		return false
	}
	for _, m := range row.Models {
		if m.ID != model {
			continue
		}
		if len(m.Capabilities) == 0 {
			return true // listed but silent on capabilities: driver decides
		}
		for _, c := range m.Capabilities {
			if c == string(want) {
				return true
			}
		}
		return false
	}
	return true
}

func hasCapability(p provider.Provider, want provider.Capability) bool {
	for _, c := range p.Capabilities() {
		if c == want {
			return true
		}
	}
	return false
}

// findModel locates the first provider listing the exact model id.
func (s *Snapshot) findModel(model string) (ProviderRow, string, bool) {
	for _, row := range s.byName {
		for _, m := range row.Models {
			if m.ID == model {
				return row, model, true
			}
		}
	}
	return ProviderRow{}, "", false
}

// Prices returns the price table for a provider's model, nil when
// unpriced.
func (s *Snapshot) Prices(providerName, model string) *ModelPrices {
	row, ok := s.byName[providerName]
	if !ok {
		return nil
	}
	for _, m := range row.Models {
		if m.ID == model {
			return m.Prices
		}
	}
	return nil
}

// Providers returns every row sorted by name (deterministic listing
// output) along with health.
func (s *Snapshot) Providers() ([]ProviderRow, map[string]bool) {
	rows := make([]ProviderRow, 0, len(s.byName))
	for _, row := range s.byName {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, s.healthy
}

// Routes returns the enabled route chains with provider names resolved
// (for the /v1/providers listing).
func (s *Snapshot) Routes() map[string][]map[string]string {
	out := make(map[string][]map[string]string, len(s.routes))
	for category, chain := range s.routes {
		entries := make([]map[string]string, 0, len(chain))
		for _, e := range chain {
			name := e.ProviderID
			if row, ok := s.rows[e.ProviderID]; ok {
				name = row.Name
			}
			entries = append(entries, map[string]string{"provider": name, "model": e.Model})
		}
		out[category] = entries
	}
	return out
}
