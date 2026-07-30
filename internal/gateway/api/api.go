// Package api is the gateway's internal HTTP surface: normalized
// streaming with chain failover, embeddings, provider listing, and
// config reload. No auth — it never leaves the compose network.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
	"github.com/SumonMSelim/timothy/internal/platform/metrics"
)

// ConfigSource supplies routing snapshots and reloads; router.Store
// implements it, tests fake it.
type ConfigSource interface {
	Snapshot() *router.Snapshot
	Load(ctx context.Context) error
}

// API serves the gateway routes.
type API struct {
	store         ConfigSource
	ledger        ledger.Recorder
	log           *slog.Logger
	providerCalls *prometheus.CounterVec
}

// Register mounts the gateway API on the shared server.
func Register(srv *httpserver.Server, store ConfigSource, rec ledger.Recorder, log *slog.Logger, m *metrics.Metrics) {
	a := &API{
		store:  store,
		ledger: rec,
		log:    log,
		providerCalls: m.NewCounterVec("provider_calls_total",
			"Provider attempts by provider, route, and outcome.", "provider", "route", "status"),
	}
	srv.Handle("POST /v1/stream", http.HandlerFunc(a.handleStream))
	srv.Handle("POST /v1/embed", http.HandlerFunc(a.handleEmbed))
	srv.Handle("GET /v1/providers", http.HandlerFunc(a.handleProviders))
	srv.Handle("POST /internal/reload", http.HandlerFunc(a.handleReload))
}

// recordAttempt writes the ledger row and the matching provider-call
// counter in one place so the two accountings never drift apart.
func (a *API) recordAttempt(ctx context.Context, entry ledger.Entry) {
	a.ledger.Record(ctx, entry)
	a.providerCalls.WithLabelValues(entry.Provider, entry.Route, entry.Status).Inc()
}

type streamRequest struct {
	Route     string             `json:"route"`
	Agent     string             `json:"agent,omitempty"`   // serving agent, for the ledger
	Purpose   string             `json:"purpose,omitempty"` // why: chat|distill|title|compaction|...
	ModelHint string             `json:"model_hint,omitempty"`
	System    string             `json:"system,omitempty"`
	Messages  []provider.Message `json:"messages"`
	Tools     []provider.ToolDef `json:"tools,omitempty"`
	MaxTokens int                `json:"max_tokens,omitempty"`
	Effort    string             `json:"effort,omitempty"` // D-020: "low" | "" (normal)
	SessionID string             `json:"session_id,omitempty"`
	MissionID string             `json:"mission_id,omitempty"`
}

// requiredVisionCapability derives whether this request needs a
// vision-capable chain entry from the message content itself — no
// explicit flag on streamRequest (D-045). A sensitive-pinned session
// routed to a local non-vision model correctly gets NoRouteError's
// "lacks vision capability" here; that's the intended v1 behavior, not
// a bug to route around.
func requiredVisionCapability(msgs []provider.Message) []provider.Capability {
	for _, m := range msgs {
		if len(m.Images) > 0 {
			return []provider.Capability{provider.CapVision}
		}
	}
	return nil
}

func jsonError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
}

// handleStream resolves the route and relays the first successful
// provider's normalized events as SSE. Failover happens only when an
// attempt fails before producing any content — a stream that already
// reached the client is never silently restarted on another provider.
func (a *API) handleStream(w http.ResponseWriter, r *http.Request) {
	var req streamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Route == "" || len(req.Messages) == 0 {
		jsonError(w, http.StatusBadRequest, "bad_request", "route and messages are required")
		return
	}

	snap := a.store.Snapshot()
	if snap == nil {
		jsonError(w, http.StatusServiceUnavailable, "config_unavailable", "routing configuration not loaded yet")
		return
	}
	// Stickiness: staying on the provider that served this session's
	// last successful turn keeps its prompt cache warm (D-018).
	var sticky router.Sticky
	if req.SessionID != "" {
		if src, ok := a.ledger.(stickySource); ok {
			if p, m, ok := src.LastSuccess(r.Context(), req.SessionID, req.Route); ok {
				sticky = router.Sticky{ProviderName: p, Model: m}
			}
		}
	}
	attempts, err := snap.Resolve(req.Route, req.ModelHint, sticky, requiredVisionCapability(req.Messages)...)
	if err != nil {
		var nre *router.NoRouteError
		if errors.As(err, &nre) {
			// D-046 safety net: brain flips an image message to "vision"
			// unconditionally (it has no cheap way to know whether the
			// route exists on this install). When "vision" is missing,
			// disabled, or has an empty chain, Resolve returns a
			// NoRouteError with no Skipped entries — nothing was even
			// tried. That's distinct from the chain existing but every
			// entry lacking the vision capability (Skipped is non-empty
			// there): THAT case must still error, since falling back
			// would silently serve an image turn on a model that can't
			// see it. Any other unknown/misconfigured route keeps
			// erroring — this fallback is vision-only, not generic
			// route-aliasing.
			if req.Route == "vision" && len(nre.Skipped) == 0 {
				a.log.Warn("vision route unresolvable, falling back to default", "reason", nre.Error())
				req.Route = "default"
				attempts, err = snap.Resolve(req.Route, req.ModelHint, sticky, requiredVisionCapability(req.Messages)...)
			}
		}
		if err != nil {
			if errors.As(err, &nre) {
				jsonError(w, http.StatusBadGateway, "no_route", nre.Error())
				return
			}
			jsonError(w, http.StatusInternalServerError, "resolve_failed", err.Error())
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer cannot flush")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// Flush headers now, before the first provider attempt starts: the
	// first token can be arbitrarily slow (e.g. a local model cold-loading),
	// and the caller's response-header timeout must never trip on that —
	// every later failure already streams as an SSE error event
	// (chain_exhausted), so nothing downstream relies on a non-200 here.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	send := func(ev stream.StreamEvent) {
		data, err := json.Marshal(ev)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	completion := provider.CompletionRequest{
		System:    req.System,
		Messages:  req.Messages,
		Tools:     req.Tools,
		MaxTokens: req.MaxTokens,
		Effort:    req.Effort,
	}

	var failed []string
	for i, att := range attempts {
		completion.Model = att.Model
		// Last chain entry gets the full in-provider retry budget;
		// earlier entries fail fast so the chain can advance.
		completion.FinalAttempt = i == len(attempts)-1
		res := streamAttempt(r.Context(), att, completion, ledger.Entry{
			ID:       ledger.NewID(),
			Provider: att.ProviderName, Model: att.Model,
			Route: req.Route, Agent: req.Agent, Purpose: req.Purpose,
			SessionID: req.SessionID, MissionID: req.MissionID,
		}, send)

		if res.failedOver() {
			failed = append(failed, fmt.Sprintf("%s/%s: %s", att.ProviderName, att.Model, res.reason))
			a.recordAttempt(r.Context(), res.entry)
			continue
		}

		res.entry.CostUSD = ledger.Cost(snap.Prices(att.ProviderName, att.Model), res.entry.Usage)
		a.recordAttempt(r.Context(), res.entry)
		return
	}

	// Chain exhausted with nothing streamed: report every attempt. The
	// per-attempt error rows recorded above ARE this request's
	// accounting (one ledger row per provider call); no synthetic
	// summary row is written.
	send(stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{
		Code:    "chain_exhausted",
		Message: "every provider attempt failed: " + strings.Join(failed, "; "),
	}})
}

// stickySource is the optional ledger capability behind session
// stickiness; the in-memory test recorders simply don't implement it.
type stickySource interface {
	LastSuccess(ctx context.Context, sessionID, route string) (providerName, model string, ok bool)
}

// attemptResult is one provider attempt's outcome.
type attemptResult struct {
	streamed bool   // any content event reached the client
	failed   bool   // the attempt errored before any content
	reason   string // failure detail for the exhaustion report
	entry    ledger.Entry
}

// noFailoverCodes are request-shape failures: the request itself is
// bad, so the next provider would reject it identically. Everything
// else — 5xx, timeouts, connection errors, 401/403 (bad key for THIS
// provider), 429 — advances the chain.
var noFailoverCodes = map[string]bool{
	"invalid_request": true,
	"http_400":        true,
	"http_404":        true,
	"http_413":        true,
	"http_422":        true,
}

// failedOver reports whether the chain may move to the next entry:
// only when the failure happened before anything reached the client,
// and only for failures a different provider could actually fix.
func (r attemptResult) failedOver() bool {
	return r.failed && !r.streamed && !noFailoverCodes[r.entry.ErrorCode]
}

// streamAttempt runs one provider attempt, relaying events to send
// until the provider channel closes, and derives the ledger status.
// Failover contract: an error before any content is a clean failure
// (nothing emitted to the client); once content has been sent, the
// stream is never restarted on another provider — errors after content
// are relayed honestly instead.
// The named return is load-bearing: the deferred latency stamp must
// mutate the value the caller receives, not a dead local copy.
func streamAttempt(ctx context.Context, att router.Attempt, completion provider.CompletionRequest, entry ledger.Entry, send func(stream.StreamEvent)) (res attemptResult) {
	res = attemptResult{entry: entry}
	start := time.Now()
	defer func() { res.entry.LatencyMS = time.Since(start).Milliseconds() }()

	ch, err := att.Provider.Stream(ctx, completion)
	if err != nil {
		res.failed, res.reason = true, err.Error()
		res.entry.Status, res.entry.ErrorCode = "error", "invalid_request"
		return res
	}

	for ev := range ch {
		switch ev.Type {
		case stream.EventError:
			res.entry.ErrorCode = ev.Err.Code
			if !res.streamed {
				res.failed, res.reason = true, ev.Err.Message
				continue // drain quietly; the client saw nothing
			}
			send(ev)
		case stream.EventUsage:
			res.entry.Usage = ev.Usage
			send(ev)
		case stream.EventIncomplete:
			if res.entry.Status == "" {
				res.entry.Status = "incomplete"
			}
			send(ev)
		case stream.EventChunk, stream.EventReasoningChunk, stream.EventToolStart, stream.EventToolEnd:
			res.streamed = true
			send(ev)
		case stream.EventDone:
			// A stream that closes clean but produced no content (e.g.
			// [DONE] with zero deltas) is not a success (D-044): tell the
			// client before the terminal done, the same incomplete-then-
			// done shape a cut-off stream already uses (see httpx.go).
			if !res.streamed {
				send(stream.StreamEvent{Type: stream.EventIncomplete, Text: "provider produced no output"})
			}
			// Attribute the serving provider on the terminal event so
			// callers need no second lookup.
			ev.Meta = &stream.Meta{
				Provider: att.ProviderName,
				Model:    att.Model,
				LedgerID: entry.ID,
			}
			send(ev)
		default:
			send(ev)
		}
	}

	res.entry.Status = finalStatus(res)
	return res
}

// finalStatus derives the ledger status from an attempt's outcome.
func finalStatus(res attemptResult) string {
	switch {
	case res.entry.Status != "": // incomplete already decided
		return res.entry.Status
	case res.failed, res.entry.ErrorCode != "":
		return "error"
	// A drained stream that produced no content is not a success (D-044):
	// booking it "ok" would also poison LastSuccess stickiness (ledger.go
	// filters status='ok'), sticking a session onto a provider that just
	// returned nothing. res.streamed is set only by content events
	// (chunk/reasoning/tool); EventUsage deliberately does not set it, so
	// a usage-only stream still lands here.
	case !res.streamed:
		return "incomplete"
	default:
		return "ok"
	}
}

type embedRequest struct {
	Texts     []string `json:"texts"`
	ModelHint string   `json:"model_hint,omitempty"`
	Purpose   string   `json:"purpose,omitempty"` // ledger tag: why this call happened
	SessionID string   `json:"session_id,omitempty"`
}

func (a *API) handleEmbed(w http.ResponseWriter, r *http.Request) {
	var req embedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if len(req.Texts) == 0 {
		jsonError(w, http.StatusBadRequest, "bad_request", "texts are required")
		return
	}

	snap := a.store.Snapshot()
	if snap == nil {
		jsonError(w, http.StatusServiceUnavailable, "config_unavailable", "routing configuration not loaded yet")
		return
	}
	attempts, err := snap.Resolve("embedding", req.ModelHint, router.Sticky{})
	if err != nil {
		jsonError(w, http.StatusBadGateway, "no_route", err.Error())
		return
	}

	var failed []string
	for _, att := range attempts {
		// Defensive only: Resolve already filters on the embeddings
		// capability, so this branch fires only for a driver that
		// declares the capability without implementing Embedder.
		emb, ok := att.Provider.(provider.Embedder)
		if !ok {
			failed = append(failed, fmt.Sprintf("%s: driver declares embeddings but cannot embed", att.ProviderName))
			continue
		}
		start := time.Now()
		entry := ledger.Entry{
			Provider: att.ProviderName, Model: att.Model,
			Route: "embedding", Purpose: req.Purpose, SessionID: req.SessionID,
		}

		vecs, usage, err := emb.Embed(r.Context(), att.Model, req.Texts)
		entry.LatencyMS = time.Since(start).Milliseconds()
		entry.Usage = usage
		if err != nil {
			entry.Status, entry.ErrorCode = "error", "provider_error"
			a.recordAttempt(r.Context(), entry)
			failed = append(failed, fmt.Sprintf("%s/%s: %v", att.ProviderName, att.Model, err))
			continue
		}

		entry.Status = "ok"
		entry.CostUSD = ledger.Cost(snap.Prices(att.ProviderName, att.Model), usage)
		a.recordAttempt(r.Context(), entry)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"provider": att.ProviderName, "model": att.Model, "embeddings": vecs,
		})
		return
	}

	jsonError(w, http.StatusBadGateway, "chain_exhausted",
		"every embedding attempt failed: "+strings.Join(failed, "; "))
}

func (a *API) handleProviders(w http.ResponseWriter, r *http.Request) {
	snap := a.store.Snapshot()
	if snap == nil {
		jsonError(w, http.StatusServiceUnavailable, "config_unavailable", "routing configuration not loaded yet")
		return
	}

	rows, healthy := snap.Providers()
	list := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		models := make([]map[string]any, 0, len(row.Models))
		for _, m := range row.Models {
			models = append(models, map[string]any{
				"id": m.ID, "context_window": m.ContextWindow,
			})
		}
		list = append(list, map[string]any{
			"name":           row.Name,
			"kind":           row.Kind,
			"driver":         row.Driver,
			"default_model":  row.DefaultModel,
			"models":         models,
			"credential_ref": row.CredentialRef, // the NAME of the ref, never a value
			"enabled":        row.Enabled,
			"healthy":        healthy[row.Name],
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"providers": list,
		"routes":    snap.Routes(),
	})
}

func (a *API) handleReload(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Load(r.Context()); err != nil {
		jsonError(w, http.StatusInternalServerError, "reload_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
