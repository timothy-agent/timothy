// Package router owns the gateway's data-driven configuration: it
// loads providers and task routes from Postgres into an immutable
// snapshot, resolves a request to an ordered list of attempts, and
// hot-reloads on a poll or an explicit trigger.
package router

import (
	"fmt"
	"sort"
	"strings"
	"time"

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
	// ExcludeFromBootstrap opts this provider out of BootstrapChain's
	// auto-fallback fill — for a local/dev provider (e.g. Ollama) that
	// should never be silently appended as a fallback on the shared
	// default/summarize/embedding routes production mission traffic runs on.
	ExcludeFromBootstrap bool
	// ReasoningEffort comes from options.reasoning_effort (D-040) and
	// overrides per-request reasoning effort for the openaicompat driver
	// only — e.g. "none" to disable a local Ollama model's thinking.
	ReasoningEffort string
	// Timeout comes from options.request_timeout (D-041) — a per-request
	// hard timeout override for slow backends (e.g. a CPU-only remote
	// Ollama). Zero leaves the driver's own default in place.
	Timeout time.Duration
	// Region comes from options.region (D-048) — the bedrock driver's AWS
	// region, overridden per-key by the secret JSON's own "region" field
	// (D-047) when set. Ignored by every other driver.
	Region string
}

// ChainEntry is one step of a route chain.
type ChainEntry struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
}

// RouteRow mirrors one routes table row. Strategy picks the chain
// order at resolve time: "ordered" keeps the written priority; "auto",
// "price", and "latency" score entries from recent ledger stats and
// declared prices (empty means ordered).
type RouteRow struct {
	Name     string
	Chain    []ChainEntry
	Strategy string
	Enabled  bool
	// Capability is what this route can serve: "chat", "embeddings", or
	// "vision" — replaces matching on the route's name to infer this.
	Capability string
	// Role marks this route as the one serving a function Timothy
	// requires to work (D-049): "default", "embedding", "vision", or
	// "summarize". Empty for a plain, user-owned route. At most one
	// route holds a given role (enforced by a partial unique index).
	Role string
}

// Snapshot is an immutable view of the routing configuration plus the
// providers built from it. Store swaps whole snapshots atomically.
type Snapshot struct {
	rows       map[string]ProviderRow // by id
	byName     map[string]ProviderRow
	routes     map[string][]ChainEntry // enabled routes only
	strategy   map[string]string       // route name -> chain strategy
	capability map[string]string       // route name -> capability (all routes, not just enabled)
	roleRoute  map[string]string       // role -> route name (all routes, not just enabled)
	stats      map[string]ModelStats   // "provider/model" -> recent ledger stats
	registry   *provider.Registry
	healthy    map[string]bool // by name: credential ref resolved
}

// ModelStats are time-decayed aggregates from the recent cost ledger,
// keyed per provider+model, feeding scored chain strategies.
type ModelStats struct {
	Uptime     float64 // weighted success rate, 0..1
	LatencyMS  float64 // weighted mean latency
	TokensPerS float64 // weighted output tokens per second
}

// SetStats attaches ledger stats to the snapshot (Store.Load calls it
// after a successful stats query; a failed query just leaves scored
// strategies running on prices alone).
func (s *Snapshot) SetStats(stats map[string]ModelStats) { s.stats = stats }

// Attempt is one provider+model candidate, in try order.
type Attempt struct {
	Provider     provider.Provider
	ProviderName string
	Model        string
}

// NoRouteError reports that resolution produced zero usable attempts.
type NoRouteError struct {
	Route   string
	Hint    string
	Skipped []string // "name (reason)" for every candidate rejected
}

func (e *NoRouteError) Error() string {
	msg := fmt.Sprintf("no usable provider for route %q", e.Route)
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
		rows:       make(map[string]ProviderRow, len(provRows)),
		byName:     make(map[string]ProviderRow, len(provRows)),
		routes:     map[string][]ChainEntry{},
		strategy:   map[string]string{},
		capability: map[string]string{},
		roleRoute:  map[string]string{},
		healthy:    make(map[string]bool, len(provRows)),
	}

	cfgs := make([]provider.Config, 0, len(provRows))
	for _, row := range provRows {
		s.rows[row.ID] = row
		s.byName[row.Name] = row
		// credential_ref names an env var for API-key drivers; bedrock now
		// requires the same ref to resolve in the secret store as static
		// keys (D-047, profile/SSO mode removed) — so an unresolved ref
		// marks every driver unhealthy alike.
		s.healthy[row.Name] = row.CredentialRef == "" || lookup(row.CredentialRef) != ""
		cfgs = append(cfgs, provider.Config{
			Name:            row.Name,
			Kind:            provider.Kind(row.Kind),
			Driver:          row.Driver,
			BaseURL:         row.BaseURL,
			CredentialRef:   row.CredentialRef,
			Headers:         row.Headers,
			Region:          row.Region,
			ReasoningEffort: row.ReasoningEffort,
			Timeout:         row.Timeout,
		})
	}

	reg, err := provider.Build(cfgs, lookup)
	if err != nil {
		return nil, err
	}
	s.registry = reg

	for _, r := range routeRows {
		s.capability[r.Name] = r.Capability
		if r.Role != "" {
			s.roleRoute[r.Role] = r.Name
		}
		if r.Enabled {
			s.routes[r.Name] = r.Chain
			s.strategy[r.Name] = r.Strategy
		}
	}
	return s, nil
}

// RouteForRole returns the name of the route currently bound to role
// ("default", "embedding", "vision", or "summarize"), or false if no
// route holds that role yet (e.g. not bootstrapped on a fresh
// install).
func (s *Snapshot) RouteForRole(role string) (string, bool) {
	name, ok := s.roleRoute[role]
	return name, ok
}

// requiredCapability maps a route to the driver capability its
// attempts must declare (D-005: routing never assumes an undeclared
// capability), from the route's own declared capability rather than
// matching its name.
func (s *Snapshot) requiredCapability(route string) provider.Capability {
	switch s.capability[route] {
	case "embeddings":
		return provider.CapEmbeddings
	case "vision":
		return provider.CapVision
	default:
		return provider.CapChat
	}
}

// Sticky names the provider+model that served a session's last
// successful turn. When set and still usable it is tried first (after
// an explicit hint): staying on one provider keeps its prompt cache
// warm (D-018), which is usually worth more than a marginally better
// score elsewhere.
type Sticky struct {
	ProviderName string
	Model        string
}

// Resolve returns the ordered attempts for a route, an optional hint,
// and an optional sticky preference. Hint semantics: a provider name
// routes to that provider's default model; an exact model id routes to
// the first enabled healthy provider listing it. A failed hint falls
// through. Try order: hint, sticky, then the chain — written order for
// "ordered" routes, score order for "auto"/"price"/"latency" (recent
// ledger stats + declared prices). Disabled, unhealthy, and
// capability-lacking providers are skipped; each candidate is tried at
// most once. extra names additional capabilities every candidate must
// also declare beyond the route's own requiredCapability — e.g.
// CapVision when the request carries images (D-045); most callers pass
// none.
func (s *Snapshot) Resolve(route, hint string, sticky Sticky, extra ...provider.Capability) ([]Attempt, error) {
	required := append([]provider.Capability{s.requiredCapability(route)}, extra...)
	var attempts []Attempt
	var skipped []string
	seen := map[string]bool{} // "name/model" dedupe across hint + sticky + chain

	add := func(row ProviderRow, model, source string) {
		p, subject, reason := s.entryGate(row, model, required)
		if reason != "" {
			skipped = append(skipped, fmt.Sprintf("%s (%s, %s)", subject, reason, source))
			return
		}
		key := row.Name + "/" + model
		if !seen[key] {
			seen[key] = true
			attempts = append(attempts, Attempt{Provider: p, ProviderName: row.Name, Model: model})
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

	// Sticky is a preference, never an expansion: it must already be a
	// member of this route's chain, so a route change or chain edit
	// naturally breaks the pin.
	if sticky.ProviderName != "" {
		for _, entry := range s.routes[route] {
			row, ok := s.rows[entry.ProviderID]
			if ok && row.Name == sticky.ProviderName && entry.Model == sticky.Model {
				add(row, entry.Model, "sticky")
				break
			}
		}
	}

	for _, entry := range s.orderedChain(route) {
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
		return nil, &NoRouteError{Route: route, Hint: hint, Skipped: skipped}
	}
	return attempts, nil
}

// entryGate applies the usability checks Resolve runs on every
// candidate. It returns the built driver plus empty subject/reason when
// usable, or a skip subject ("name", or "name/model" for capability
// misses) and reason matching the NoRouteError wording.
func (s *Snapshot) entryGate(row ProviderRow, model string, required []provider.Capability) (provider.Provider, string, string) {
	p, inRegistry := s.registry.Get(row.Name)
	switch {
	case !row.Enabled:
		return nil, row.Name, "disabled"
	case !s.healthy[row.Name]:
		return nil, row.Name, fmt.Sprintf("unhealthy: credential %s unresolved", row.CredentialRef)
	case !inRegistry:
		return nil, row.Name, "not in registry"
	}
	for _, want := range required {
		if !attemptCapable(p, row, model, want) {
			return nil, row.Name + "/" + model, fmt.Sprintf("lacks %s capability", want)
		}
	}
	return p, "", ""
}

// Strategy weights: relative importance of each additive factor,
// normalized against the best candidate in the chain (the llmgateway
// scheme). price dominates "price", latency dominates "latency",
// "auto" balances. Uptime is not a weight — it multiplies the whole
// score, so an unreliable provider sinks under every strategy: no
// price advantage outruns failing most requests. Factors with no data
// score neutrally so a brand-new entry is neither favored nor starved.
var strategyWeights = map[string]struct{ price, latency, tps float64 }{
	"auto":    {price: 0.6, latency: 0.1, tps: 0.05},
	"price":   {price: 0.9, latency: 0.02, tps: 0.02},
	"latency": {price: 0.1, latency: 0.6, tps: 0.1},
}

// ResolvedEntry is one chain candidate in router try order, annotated
// with the usability gate Resolve applies and the factors the scored
// strategies use. Sentinels: Uptime and the Norm* fields are -1 when
// the ledger has no data (scoring treats the factor as neutral); raw
// LatencyMS, TokensPerS, and OutputPerMTok are 0 when unknown — an
// absent price stays absent, never guessed.
type ResolvedEntry struct {
	Entry         ChainEntry
	ProviderName  string // empty when the provider id is unknown
	Model         string // entry model, defaulted to the provider's default when empty
	Usable        bool
	SkipReason    string  // empty when usable; NoRouteError wording without the source suffix
	Scored        bool    // false for "ordered" (or unknown) strategies
	Score         float64 // uptime-multiplied total; 0 when not scored
	NormPrice     float64 // best-in-chain normalized, 0..1
	NormLatency   float64
	NormTPS       float64
	Uptime        float64 // raw weighted success rate, 0..1
	LatencyMS     float64 // raw weighted mean latency
	TokensPerS    float64 // raw weighted output tokens per second
	OutputPerMTok float64 // declared output price used for scoring
}

// ResolveDetail returns a route's chain in the exact try order
// orderedChain produces, annotated for observability. It is the single
// scoring path: written order for "ordered" (or unknown) strategies,
// descending score otherwise. Only enabled routes exist in the
// snapshot; unknown routes return an empty slice.
func (s *Snapshot) ResolveDetail(route string) []ResolvedEntry {
	chain := s.routes[route]
	w, scoredStrategy := strategyWeights[s.strategy[route]]
	required := []provider.Capability{s.requiredCapability(route)}

	// Gather each entry's raw factors first: normalization needs the
	// best value across the candidate set.
	out := make([]ResolvedEntry, 0, len(chain))
	minPrice, minLatency, maxTPS := 0.0, 0.0, 0.0
	for _, e := range chain {
		d := ResolvedEntry{
			Entry: e, Model: e.Model, Scored: scoredStrategy,
			Uptime: -1, NormPrice: -1, NormLatency: -1, NormTPS: -1,
		}
		row, ok := s.rows[e.ProviderID]
		if !ok {
			d.SkipReason = "unknown provider id"
			out = append(out, d)
			continue
		}
		d.ProviderName = row.Name
		if d.Model == "" {
			d.Model = row.DefaultModel
		}
		// Stats and prices are keyed by the raw entry model — same
		// lookup Resolve's scoring has always used.
		if p := s.Prices(row.Name, e.Model); p != nil && p.OutputPerMTok > 0 {
			d.OutputPerMTok = p.OutputPerMTok
			if minPrice == 0 || d.OutputPerMTok < minPrice {
				minPrice = d.OutputPerMTok
			}
		}
		if st, ok := s.stats[row.Name+"/"+e.Model]; ok {
			d.Uptime = st.Uptime
			d.LatencyMS = st.LatencyMS
			d.TokensPerS = st.TokensPerS
			if d.LatencyMS > 0 && (minLatency == 0 || d.LatencyMS < minLatency) {
				minLatency = d.LatencyMS
			}
			if d.TokensPerS > maxTPS {
				maxTPS = d.TokensPerS
			}
		}
		if _, _, reason := s.entryGate(row, d.Model, required); reason != "" {
			d.SkipReason = reason
		} else {
			d.Usable = true
		}
		out = append(out, d)
	}

	if !scoredStrategy {
		return out
	}

	const neutral = 0.5
	for i := range out {
		d := &out[i]
		total := 0.0
		if d.OutputPerMTok > 0 && minPrice > 0 {
			d.NormPrice = minPrice / d.OutputPerMTok
			total += w.price * d.NormPrice
		} else {
			total += w.price * neutral
		}
		if d.LatencyMS > 0 && minLatency > 0 {
			d.NormLatency = minLatency / d.LatencyMS
			total += w.latency * d.NormLatency
		} else {
			total += w.latency * neutral
		}
		if d.TokensPerS > 0 && maxTPS > 0 {
			d.NormTPS = d.TokensPerS / maxTPS
			total += w.tps * d.NormTPS
		} else {
			total += w.tps * neutral
		}
		if d.Uptime >= 0 {
			// Multiplicative, floored so one bad minute can't zero a
			// candidate out of consideration entirely.
			total *= max(d.Uptime, 0.05)
		}
		d.Score = total
	}
	if len(chain) >= 2 {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	}
	return out
}

// orderedChain returns a route's chain in try order: written order for
// "ordered" (or unknown) strategies, descending score otherwise.
func (s *Snapshot) orderedChain(route string) []ChainEntry {
	detail := s.ResolveDetail(route)
	out := make([]ChainEntry, len(detail))
	for i, d := range detail {
		out[i] = d.Entry
	}
	return out
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
// Provider returns the built driver for a provider name — the admin
// test-connection path needs the instance, not an Attempt.
func (s *Snapshot) Provider(name string) (provider.Provider, bool) {
	return s.registry.Get(name)
}

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
	for name, chain := range s.routes {
		entries := make([]map[string]string, 0, len(chain))
		for _, e := range chain {
			name := e.ProviderID
			if row, ok := s.rows[e.ProviderID]; ok {
				name = row.Name
			}
			entries = append(entries, map[string]string{"provider": name, "model": e.Model})
		}
		out[name] = entries
	}
	return out
}
