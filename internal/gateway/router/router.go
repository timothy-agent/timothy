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

// ModelPrices are per-million-token prices in the provider's billing
// currency (Currency; blank means USD). Absent prices mean cost is
// unknown and recorded as null — never guessed. No FX conversion
// happens anywhere in this codebase, so Currency must match what the
// provider actually bills in.
type ModelPrices struct {
	InputPerMTok      float64 `json:"input_per_mtok"`
	OutputPerMTok     float64 `json:"output_per_mtok"`
	CacheReadPerMTok  float64 `json:"cache_read_per_mtok"`
	CacheWritePerMTok float64 `json:"cache_write_per_mtok"`
	Currency          string  `json:"currency,omitempty"`
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
	// AnthropicBaseURL comes from options.anthropic_base_url (D-051) — the
	// URL a claude-cli harness entry injects into the spawned CLI's
	// environment when this row's own driver isn't already "anthropic"
	// (e.g. a subscription-auth row with no chat driver at all). Ignored
	// by every chat-serving driver.
	AnthropicBaseURL string
}

// ChainEntry is one step of a route chain. Harness selection moved to
// the mission itself (D-051 rework): a chain entry is pure
// {provider_id, model} again, model routing only.
type ChainEntry struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
}

// KnownHarnesses is the single source of truth for valid harness names —
// the resolve endpoint's executor gate (ResolveRoute) rejects anything
// else as "unknown harness" (D-051). Adapters that actually spawn these
// CLIs live in brain (internal/brain/missions/executor); the gateway
// only validates names and wire-format compatibility, never runs a
// subprocess itself.
var KnownHarnesses = map[string]bool{"claude-cli": true, "pi": true, "codex-cli": true, "opencode": true}

// harnessDrivers names the set of driver names each known harness
// accepts directly from its provider row — checked by both admin
// validation and the resolve endpoint's executor gate so the two can
// never disagree. claude-cli speaks anthropic only; pi speaks either
// anthropic or openaicompat natively (its whole point is dual-wire
// support); codex-cli and opencode speak openaicompat only (codex's own
// responses wire; opencode's config-file baseURL).
// Independent of this set, the anthropic_base_url override (D-051)
// always satisfies claude-cli/pi, since it exposes an anthropic-format
// endpoint regardless of the row's own driver — codex-cli/opencode have
// no such override, since neither speaks anthropic.
var harnessDrivers = map[string]map[string]bool{
	"claude-cli": {"anthropic": true},
	"pi":         {"anthropic": true, "openaicompat": true},
	"codex-cli":  {"openaicompat": true},
	"opencode":   {"openaicompat": true},
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
	healthy    map[string]bool   // by name: credential ref resolved and driver built
	unhealthy  map[string]string // by name: reason, set whenever healthy[name] is false
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

// BuildWarning reports one provider row that could not be built into a
// usable driver — a malformed config never takes down the whole
// snapshot; the row stays in Providers()/rows for admin visibility,
// just unhealthy and absent from the registry.
type BuildWarning struct {
	Provider string
	Err      error
}

// BuildSnapshot assembles a snapshot from table rows. lookup resolves
// credential references; a ref that resolves empty marks the provider
// unhealthy (skipped by routing) without failing the build. Rows whose
// driver fails to build are excluded from the registry; disabled rows
// stay in the registry (admin probes need them) but are marked
// unhealthy. Neither is removed from rows/byName — admin views still
// list them. providers.name is DB-unique, so
// duplicate rows can't reach BuildSnapshot; a bad or disabled provider
// is reported via the returned warnings — BuildSnapshot itself never
// fails.
func BuildSnapshot(provRows []ProviderRow, routeRows []RouteRow, lookup func(string) string) (*Snapshot, []BuildWarning) {
	s := &Snapshot{
		rows:       make(map[string]ProviderRow, len(provRows)),
		byName:     make(map[string]ProviderRow, len(provRows)),
		routes:     map[string][]ChainEntry{},
		strategy:   map[string]string{},
		capability: map[string]string{},
		roleRoute:  map[string]string{},
		healthy:    make(map[string]bool, len(provRows)),
		unhealthy:  map[string]string{},
		registry:   provider.NewRegistry(),
	}

	var warnings []BuildWarning
	for _, row := range provRows {
		s.rows[row.ID] = row
		s.byName[row.Name] = row
		// Disabled rows are still built into the registry so the admin
		// connection probe can reach them (create → test → enable); they
		// stay unhealthy and routing rejects them on row.Enabled.
		switch {
		case !row.Enabled:
			s.unhealthy[row.Name] = "disabled"
		// credential_ref names an env var for API-key drivers; bedrock now
		// requires the same ref to resolve in the secret store as static
		// keys (D-047, profile/SSO mode removed) — so an unresolved ref
		// marks every driver unhealthy alike.
		case row.CredentialRef != "" && lookup(row.CredentialRef) == "":
			s.unhealthy[row.Name] = fmt.Sprintf("credential %s unresolved", row.CredentialRef)
		default:
			s.healthy[row.Name] = true
		}

		// kind='cli' rows (D-051) are mission-only executor providers —
		// e.g. a subscription-auth claude-cli row with no chat driver at
		// all. They never serve chat, so no chat provider is built and an
		// unbuildable/unknown driver name here must never degrade the
		// snapshot or emit a BuildWarning; executorUsable judges these
		// rows entirely from the row itself, never from the registry.
		if row.Kind == "cli" {
			continue
		}

		// Built one provider at a time: a single bad driver name or
		// invalid config must not take down every other route's
		// providers, so provider.Build's all-or-nothing error is
		// contained to this one row.
		reg, err := provider.Build([]provider.Config{{
			Name:            row.Name,
			Kind:            provider.Kind(row.Kind),
			Driver:          row.Driver,
			BaseURL:         row.BaseURL,
			CredentialRef:   row.CredentialRef,
			Headers:         row.Headers,
			Region:          row.Region,
			ReasoningEffort: row.ReasoningEffort,
			Timeout:         row.Timeout,
		}}, lookup)
		if err != nil {
			warnings = append(warnings, BuildWarning{Provider: row.Name, Err: err})
			s.healthy[row.Name] = false
			s.unhealthy[row.Name] = err.Error()
			continue
		}
		p, _ := reg.Get(row.Name)
		s.registry.Add(row.Name, p)
	}

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
	return s, warnings
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
		return nil, row.Name, fmt.Sprintf("unhealthy: %s", s.unhealthy[row.Name])
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
	ProviderKind  string // "api" or "cli"; empty when the provider id is unknown
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
		d.ProviderKind = row.Kind
		if d.Model == "" {
			d.Model = row.DefaultModel
		}
		// Stats and prices are keyed by the resolved model (d.Model,
		// defaulted above) — an entry with no model of its own serves
		// row.DefaultModel, and that's what the ledger and price rows
		// are keyed by.
		if p := s.Prices(row.Name, d.Model); p != nil && p.OutputPerMTok > 0 {
			d.OutputPerMTok = p.OutputPerMTok
			if minPrice == 0 || d.OutputPerMTok < minPrice {
				minPrice = d.OutputPerMTok
			}
		}
		if st, ok := s.stats[row.Name+"/"+d.Model]; ok {
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

// ResolvedRouteEntry is one chain entry as reported by the resolve
// endpoint (D-051). CredentialRef is always a NAME, never resolved to a
// secret value.
type ResolvedRouteEntry struct {
	ProviderID    string
	ProviderName  string
	Driver        string
	Kind          string
	Model         string
	CredentialRef string
	BaseURL       string
	Usable        bool
	SkipReason    string
	// Prices is the entry's configured per-Mtok prices, when the
	// provider has a price row for Model — nil when unpriced (D-013:
	// never guessed). Lets a delegated executor caller price its own
	// reported tokens for a non-anthropic provider (D-05x) without a
	// second round trip.
	Prices *ModelPrices
	// Wire is the wire format this entry exposes on the executor axis —
	// "anthropic" or "openai" — set only when harness != "" and
	// row.Kind != "cli" (a kind='cli' row has no wire format at all).
	// A dual-wire harness (pi) uses this to pick its provider config;
	// a single-wire harness (claude-cli) ignores it.
	Wire string
}

// ResolveRoute returns route's chain in stored order, annotated with
// the gate appropriate to the requested axis (D-051 rework — harness is
// now a caller-supplied param, not a per-entry chain field): harness ==
// "" evaluates every entry with the chat entryGate (the same verdict
// ResolveDetail computes); harness != "" evaluates every entry with
// executorUsable instead, since the chat gate would reject a
// mission-only executor dispatch unconditionally, and applies the
// anthropic_base_url override for a non-anthropic driver row. ok is
// false when the route doesn't exist (disabled or unknown name) OR
// harness is non-empty and unknown — an existing route with zero
// entries still returns ok true and an empty slice.
func (s *Snapshot) ResolveRoute(route, harness string) ([]ResolvedRouteEntry, bool) {
	chain, ok := s.routes[route]
	if !ok {
		return nil, false
	}
	if harness != "" && !KnownHarnesses[harness] {
		return nil, false
	}
	required := []provider.Capability{s.requiredCapability(route)}
	out := make([]ResolvedRouteEntry, 0, len(chain))
	for _, e := range chain {
		re := ResolvedRouteEntry{ProviderID: e.ProviderID, Model: e.Model}
		row, found := s.rows[e.ProviderID]
		if !found {
			re.SkipReason = "unknown provider id"
			out = append(out, re)
			continue
		}
		re.ProviderName, re.Driver, re.Kind, re.CredentialRef = row.Name, row.Driver, row.Kind, row.CredentialRef
		if re.Model == "" {
			re.Model = row.DefaultModel
		}
		re.BaseURL = row.BaseURL
		re.Prices = s.Prices(row.Name, re.Model)
		if harness == "" {
			if _, _, reason := s.entryGate(row, re.Model, required); reason != "" {
				re.SkipReason = reason
			} else {
				re.Usable = true
			}
		} else {
			// The anthropic_base_url override only applies to a kind='api'
			// row pointed at a third-party anthropic-compatible endpoint,
			// for a harness that actually accepts the anthropic wire
			// (codex-cli never does — it speaks openai only) — a kind='cli'
			// row (subscription/oauth-auth) talks to the vendor's own
			// default endpoint and must keep BaseURL empty, or
			// BuildInvocation's AuthSubscription/AuthOAuthToken checks
			// reject the spawn outright (they require no BaseURL at all).
			overrideApplied := row.Kind != "cli" && row.Driver != "anthropic" &&
				row.AnthropicBaseURL != "" && harnessDrivers[harness]["anthropic"]
			if overrideApplied {
				re.BaseURL = row.AnthropicBaseURL
			}
			re.Usable, re.SkipReason = executorUsable(row, harness)
			if row.Kind != "cli" {
				re.Wire = harnessWire(row.Driver, overrideApplied)
			}
		}
		out = append(out, re)
	}
	return out, true
}

// executorUsable applies the harness-entry rule (D-051), deliberately
// separate from entryGate: a harness entry is dispatched by brain's
// missions harness as a CLI subprocess, never streamed through this
// gateway's chat path, so none of entryGate's chat-serving checks
// (registry membership, chat capability) apply. Usable requires the
// provider row enabled, a recognized harness name, wire-format
// compatibility for that harness, and a non-empty credential_ref name
// (the value itself is never resolved here — ref only, D-051). A
// kind='cli' row is inherently wire-compatible: it spawns the harness's
// own CLI talking to the vendor's own default endpoint under
// subscription/oauth credentials, never a third-party anthropic-
// compatible one, so the wire check only applies to kind='api' rows
// (a chat-serving row repurposed as an executor entry). A kind='cli'
// row exists specifically to serve the harness named by its own
// driver (e.g. driver="claude-cli" serves only the claude-cli harness)
// — usable by any other harness it would resolve wire-compatible
// (Wire == "") yet BuildInvocation rejects outright, a config that
// looks valid but always falls back to native.
func executorUsable(row ProviderRow, harness string) (bool, string) {
	if !row.Enabled {
		return false, "disabled"
	}
	if !KnownHarnesses[harness] {
		return false, "unknown harness"
	}
	if row.Kind == "cli" {
		if row.Driver != harness {
			return false, fmt.Sprintf("cli provider row serves the %s harness", row.Driver)
		}
	} else {
		wireOK := harnessDrivers[harness][row.Driver] ||
			(row.AnthropicBaseURL != "" && harnessDrivers[harness]["anthropic"])
		if !wireOK {
			return false, fmt.Sprintf("wire-incompatible: %s requires one of %s, or options.anthropic_base_url", harness, sortedKeys(harnessDrivers[harness]))
		}
	}
	if row.CredentialRef == "" {
		return false, "credential_ref is required"
	}
	return true, ""
}

// sortedKeys returns m's keys, sorted — used only to keep an operator-
// facing skip-reason string deterministic.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// harnessWire computes the wire format a resolved executor entry
// exposes: the anthropic_base_url override (in play whenever BaseURL
// was swapped to it) always means anthropic; otherwise it follows the
// row's own driver. Empty when neither applies (row.Kind == "cli",
// which has no wire format at all — the CLI talks to its vendor's own
// default endpoint).
func harnessWire(driver string, overrideApplied bool) string {
	if overrideApplied {
		return "anthropic"
	}
	switch driver {
	case "anthropic":
		return "anthropic"
	case "openaicompat":
		return "openai"
	default:
		return ""
	}
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
