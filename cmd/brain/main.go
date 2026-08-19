// Command brain is Timothy's public API service.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
	"github.com/SumonMSelim/timothy/internal/brain/api"
	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/destinations"
	"github.com/SumonMSelim/timothy/internal/brain/fxrates"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/loop"
	"github.com/SumonMSelim/timothy/internal/brain/memclient"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	"github.com/SumonMSelim/timothy/internal/brain/sandboxclient"
	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/brain/settings"
	"github.com/SumonMSelim/timothy/internal/brain/skills"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/internal/platform/service"
	"github.com/SumonMSelim/timothy/internal/secretstore"
	"github.com/SumonMSelim/timothy/migrations"
)

// defaultTokenBudget bounds the projected context; provider-window
// driven budgets arrive when model rows carry context windows.
const defaultTokenBudget = 60_000

// sensitiveToolSuffixes is the single source of truth for which tools'
// output must never leave the sensitive route floor once called: the
// in-turn pin (loop.Agent.SetForceRoute) and the side-call route
// (session.SensitiveTools, chat/memoryd/compactor) both key off this
// same list, so a tool added here is covered everywhere at once.
var sensitiveToolSuffixes = []string{"gmail_read", "gmail_read_attachment"}

// builtinToolSet guards the compiled-in tool slice buildAgent
// returns: the connector reload goroutine reads it (via
// conns.SetOnReload's closure) on its own timer, concurrently with
// main()'s later append of the mission tools once missionStore is
// built — a plain slice variable shared across those two goroutines
// would race on that append. add appends under lock and returns the
// current snapshot for the caller to pass straight to swapAgentTools.
type builtinToolSet struct {
	mu    sync.Mutex
	tools []*tools.Tool
}

func newBuiltinToolSet(initial []*tools.Tool) *builtinToolSet {
	return &builtinToolSet{tools: initial}
}

func (b *builtinToolSet) add(t ...*tools.Tool) []*tools.Tool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tools = append(b.tools, t...)
	return append([]*tools.Tool(nil), b.tools...)
}

func (b *builtinToolSet) snapshot() []*tools.Tool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*tools.Tool(nil), b.tools...)
}

const (
	serviceName = "brain"
	defaultPort = 8080
	// extractBudget bounds the fire-and-forget turn-end extraction:
	// one memoryd round trip (LLM + embed) plus slack.
	extractBudget = 90 * time.Second
	// retrieveBudget sits ON the turn's critical path — one embed +
	// three SQL legs, so tight.
	retrieveBudget = 10 * time.Second
	// preCompactExtractBudget bounds the extraction INSIDE the
	// compaction pass so a slow extraction degrades to empty facts
	// instead of starving the summarize.
	preCompactExtractBudget = 45 * time.Second
	// connectorReadyWait bounds how long a turn waits at entry for the
	// first connector load (D-043) — long enough to absorb the DB pool
	// warming at boot, short enough that a turn never meaningfully hangs
	// on it.
	connectorReadyWait = 15 * time.Second
)

func main() {
	healthcheck := flag.Bool("healthcheck", false,
		"probe /health and exit; used as the container health check")
	flag.Parse()
	if *healthcheck {
		os.Exit(service.ProbeHealth(defaultPort))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := service.New(ctx, serviceName, defaultPort, migrations.FS)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	token := os.Getenv("TIMOTHY_API_TOKEN")
	if token == "" {
		app.Log.Warn("TIMOTHY_API_TOKEN not set; API requests will be rejected")
	}
	app.AddCheck("auth", func() httpserver.Check {
		if token == "" {
			return httpserver.Check{Status: "degraded", Detail: "TIMOTHY_API_TOKEN not set"}
		}
		return httpserver.Check{Status: "ok"}
	})

	gatewayURL := os.Getenv("GATEWAY_URL")
	if gatewayURL == "" {
		gatewayURL = "http://gateway:8081"
	}
	gwc := gwclient.New(gatewayURL)
	store := session.NewStore(app.DB, app.Log)
	flags := settings.New(app.DB, app.Log)
	// Runtime settings, editable in the UI without a restart: the
	// projected-context budget fallback and the skill-pack allowlist.
	budgetFn := func(ctx context.Context) int { return flags.TokenBudget(ctx, defaultTokenBudget) }
	compactor := session.NewCompactor(store, gwc, gwc, budgetFn, app.Log,
		app.Metrics.NewCounter("session_compactions_total", "Sessions compacted to stay under the context budget."))
	distill := func(ctx context.Context, sessionID, turnText, route string) *session.TurnMemory {
		return loop.DistillTurn(ctx, gwc, sessionID, turnText, route)
	}

	workspace := os.Getenv("WORKSPACE_ROOT")
	if workspace == "" {
		workspace = "/workspace"
	}
	skillsDir := os.Getenv("SKILLS_DIR")
	if skillsDir == "" {
		skillsDir = "/skills"
	}
	// Broken packs degrade health rather than crash the service.
	// All loaded packs stay in memory; the skills_allowlist runtime
	// setting gates which ones the agent may reach per turn.
	packs, err := skills.Load(skillsDir)
	if err != nil {
		app.Log.Error("skills failed to load; continuing without them", "error", err)
	}
	app.AddCheck("skills", func() httpserver.Check {
		if err != nil {
			return httpserver.Check{Status: "degraded", Detail: err.Error()}
		}
		return httpserver.Check{Status: "ok"}
	})

	memorydURL := os.Getenv("MEMORYD_URL")
	if memorydURL == "" {
		memorydURL = "http://memoryd:8082"
	}
	mc := memclient.New(memorydURL)

	searxngURL := os.Getenv("SEARXNG_URL")
	markitdownURL := os.Getenv("MARKITDOWN_URL")
	if markitdownURL == "" {
		app.Log.Warn("MARKITDOWN_URL not set; PDF attachments in chat are unavailable")
	}
	whisperURL := os.Getenv("WHISPER_URL")
	if whisperURL == "" {
		app.Log.Warn("WHISPER_URL not set; the web mic button's /v1/transcribe endpoint is unavailable")
	}

	toolCalls := app.Metrics.NewCounterVec("tool_calls_total",
		"Tool executions by tool name and outcome.", "tool", "outcome")
	// sensitiveRoute resolves the route sensitive-tool turns and their
	// side-calls (extraction, compaction summarize) pin to: the runtime
	// setting, else "" (feature off). A func, not a value snapshotted at
	// startup, so a settings change from the web UI applies to the next
	// turn without a restart.
	sensitiveRoute := func(ctx context.Context) string {
		return flags.Value(ctx, settings.ValueSensitiveToolRoute)
	}
	// fxStore backs both the daily rate fetch (below) and
	// currency_convert's table-first lookup (buildAgent) — one fetch,
	// one table, shared by the live tool and by display conversion
	// (Analytics, mission usage) elsewhere in this file.
	fxStore := fxrates.NewStore(app.DB)
	go fxrates.NewFetcher(fxStore, settings.AllowedCurrencies(), app.Log).Run(ctx)

	agent, broker, outputs, builtins, chatPerms, buildErr := buildAgent(gwc, store, app.DB, workspace, searxngURL, markitdownURL, packs, flags.SkillAllowed, mc.Add, app.Log, toolCalls, sensitiveRoute, fxStore)
	if buildErr != nil {
		fmt.Fprintln(os.Stderr, buildErr)
		os.Exit(1)
	}
	builtinSet := newBuiltinToolSet(builtins)
	go runOutputGC(ctx, outputs, app.Log)

	secrets, err := buildSecretStore(app.DB, app.Log)
	if err != nil {
		app.Log.Warn("secret store disabled", "error", err)
	}
	var resolveSecret func(context.Context, string) (string, error)
	if secrets != nil {
		resolveSecret = secrets.Resolve
	}

	conns, goog := buildConnectors(app.DB, secrets, app.Log)
	if conns != nil {
		conns.RegisterBuilder("mcp", connectors.MCPBuilder(nil))
		conns.RegisterBuilder("github", connectors.GitHubBuilder(nil))
		if goog != nil {
			conns.RegisterBuilder("google", goog.Builder())
		}
		conns.SetOnReload(func(context.Context) {
			swapAgentTools(agent, builtinSet.snapshot(), conns, app.Log, toolCalls)
		})
		go runConnectorReload(ctx, conns, app.Log)
		app.AddCheck("connectors", func() httpserver.Check {
			select {
			case <-conns.Ready():
				return httpserver.Check{Status: "ok"}
			default:
				return httpserver.Check{Status: "degraded", Detail: "initial connector load not yet complete"}
			}
		})
		// Only the first turn after boot pays this: once conns.Ready()
		// closes, WaitReady returns instantly on every later call. Bounded
		// so a turn NEVER blocks indefinitely — a slow/never-ready load
		// degrades to builtins-only instead of hanging the turn.
		agent.SetWaitToolsReady(func(ctx context.Context) {
			wctx, cancel := context.WithTimeout(ctx, connectorReadyWait)
			defer cancel()
			if err := conns.WaitReady(wctx); err != nil {
				app.Log.Warn("connector tools not yet loaded; turn proceeds with builtin tools only")
			}
		})
	}

	// Mission shell/verify_cmd execution always runs sandboxed via
	// sandboxd (it holds the Docker socket, brain no longer touches it
	// directly). Brain doesn't fail closed if sandboxd itself is
	// unreachable at boot — that surfaces as a degraded health check
	// below, with missions failing as infra at exec time instead.
	sandboxdURL := os.Getenv("SANDBOXD_URL")
	if sandboxdURL == "" {
		sandboxdURL = "http://sandboxd:8083"
	}
	missionSandbox := sandboxclient.New(sandboxdURL)

	// Built here, above buildMissions, so both the scheduler (fire-time
	// route/review_route/budget/prompt_overlay resolution) and the
	// driver (ApprovalAllowlist grants at provisioning time) can close
	// over the same registry.
	agentReg := agents.NewStore(app.DB, app.Log)

	routeForRole := func(ctx context.Context, role string) string {
		name, ok, err := gwc.RouteForRole(ctx, role)
		if err != nil || !ok {
			return ""
		}
		return name
	}
	missionStore, missionDriver, missionNotifier, missionWorkspace, missionHub := buildMissions(ctx, app.DB, agent, store, workspace, flags, missionSandbox, agentReg, routeForRole, fxStore, gwc, secrets, conns, mc, app.Log)
	if missionDriver != nil {
		go missions.RecoverAndSweep(ctx, missionDriver, missionStore, missionWorkSlotMax, missionSandbox, missionSandbox, app.Log)
	}
	destinationStore, destinationDeliverer := buildDestinations(app.DB, conns, goog, flags, missionStore, app.Log)
	if missionDriver != nil && destinationDeliverer != nil {
		missionDriver.SetDestinationDeliver(destinationDeliverer.Deliver)
	}
	// Chat-facing mission tools (D-0xx: "is mission X done?" / "push
	// mission X"): registered here, not inside buildAgent, since both
	// need missionStore (built above, after buildAgent) and mission_push
	// additionally needs conns+secrets for its push token resolver.
	// missionStore nil (WORKSPACES unset) means neither tool is
	// registered at all, matching the nil-gating pattern buildMissions
	// itself already uses. A recompile+swap applies them to the live
	// agent immediately rather than waiting for the next connector
	// reload (which may never come if no connectors are configured).
	if missionStore != nil {
		missionAdapter := missionToolStore{missionStore}
		newTools := []*tools.Tool{builtin.Missions(missionAdapter, missionAdapter)}
		if conns != nil && secrets != nil {
			resolvePushToken := func(ctx context.Context, connectorID string) (string, error) {
				c, err := conns.Store().Get(ctx, connectorID)
				if err != nil {
					return "", fmt.Errorf("resolve connector %s: %w", connectorID, err)
				}
				return secrets.Resolve(ctx, c.CredentialRef)
			}
			completer := missionCompleterAdapter{missionStore, missions.NewCompleter(missionWorkspace, missionStore, nil, connsPRSource{conns})}
			newTools = append(newTools, builtin.MissionPush(missionAdapter, completer, resolvePushToken))
		}
		current := builtinSet.add(newTools...)
		if conns != nil {
			swapAgentTools(agent, current, conns, app.Log, toolCalls)
		} else if constrained, defs, err := compileToolset(current, nil, app.Log, toolCalls); err != nil {
			app.Log.Warn("mission tool registration failed; agent keeps its previous tool surface", "error", err)
		} else {
			agent.SwapTools(constrained, defs)
		}
	}
	app.AddCheck("sandbox", func() httpserver.Check {
		if err := missionSandbox.Health(ctx); err != nil {
			return httpserver.Check{Status: "degraded", Detail: err.Error()}
		}
		return httpserver.Check{Status: "ok"}
	})

	// sensitiveConnectorNames is the connector-level input to
	// SensitiveTools: every enabled connector marked sensitive in
	// settings, matched as a namespace PREFIX (Matches' ConnectorNames
	// rule) since "<connector-name>_<tool-name>" puts the connector's own
	// name in front of every tool it serves. Computed fresh each call,
	// not cached, so toggling a connector's sensitive flag takes effect
	// on the next side-call without a restart — same reasoning as
	// sensitiveRoute above. conns is nil when the connector surface
	// itself is disabled (no secret store).
	sensitiveConnectorNames := func(ctx context.Context) []string {
		if conns == nil {
			return nil
		}
		names, err := conns.SensitiveNames(ctx)
		if err != nil {
			app.Log.Warn("sensitive connector lookup failed; connector-level sensitivity off this call", "error", err)
			return nil
		}
		return names
	}

	// Single source of truth for "this turn/session executed a sensitive
	// tool": the loop's in-turn SetForceRoute pin above and this
	// SensitiveTools value share sensitiveToolSuffixes and the same
	// sensitiveRoute resolver, so side-calls (extraction, compaction
	// summarize) honor the same route floor the tool loop already
	// pinned the turn to. Wired unconditionally — sensitiveRoute
	// resolving to "" at call time means the feature is currently off,
	// same as before, but now editable at runtime from the settings UI.
	sensitiveTools := &session.SensitiveTools{
		Suffixes:       func(context.Context) []string { return sensitiveToolSuffixes },
		ConnectorNames: sensitiveConnectorNames,
		Route:          sensitiveRoute,
	}
	// Connector-level sensitivity's in-turn counterpart: a whole
	// connector flagged sensitive must pin the SAME turn that calls its
	// tool, not just every turn after (chat.pinSensitiveRoute) — without
	// this, a search/read tool's own results ride the turn's ORIGINAL
	// route (e.g. a cloud default) until the NEXT turn notices the
	// session is sensitive, one turn too late for whatever content that
	// tool just pulled into context. Wired here, after conns exists,
	// rather than inside buildAgent (which runs before conns is built).
	agent.SetForceRouteByConnector(sensitiveConnectorNames, sensitiveRoute)

	svc := chat.New(turnRouter{agent: agent, gw: gwc, flags: flags}, store, distill,
		gatedCompactor{inner: compactor, flags: flags}, budgetFn, packs, flags.SkillAllowed,
		agentReg.Resolve, app.Log)
	svc.SetAutoDispatch(agentReg.Enabled, chat.ClassifyOverGateway(gwc))
	svc.SetSensitiveTools(sensitiveTools)
	// TURN_TIMEOUT raises the detached-turn ceiling above the compiled
	// 30m default — needed when a route serves a slow CPU-only backend
	// whose provider request_timeout (D-041) would otherwise collide
	// with the brain ceiling and manufacture a deadline-vs-terminal
	// race at the exact same instant. Env-gated feature, default off.
	if v := os.Getenv("TURN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			app.Log.Warn("invalid TURN_TIMEOUT ignored", "value", v, "error", err)
		} else if err := svc.SetTurnTimeout(d); err != nil {
			app.Log.Warn("TURN_TIMEOUT rejected", "value", v, "error", err)
		} else {
			app.Log.Info("turn timeout overridden", "timeout", d)
		}
	}
	// Nil-safe: missionHub is nil when WORKSPACES is unset (see
	// buildMissions), in which case SetSessionHub's publish func is
	// never called (chat.go only invokes it when non-nil), leaving
	// today's behavior (no push) unchanged.
	if missionHub != nil {
		svc.SetSessionHub(func(sessionID string) {
			missionHub.Publish(missions.Signal{Kind: "session", ID: sessionID})
		})
		svc.SetPermissionHub(func(sessionID string) {
			missionHub.Publish(missions.Signal{Kind: "permission", ID: sessionID})
		})
	}
	// Same Permissions instance (and session_grants table) the chat
	// agent loop's own Resolve chain reads from — seeding here is
	// visible to that exact chain, not a parallel grant store.
	svc.SetApprovalGrants(chatPerms)
	compactor.SetSensitiveTools(sensitiveTools)
	svc.SetMemoryExtract(func(ctx context.Context, sessionID string, seq int64, text, route string) {
		if !flags.Enabled(ctx, settings.KeyMemoryExtraction) {
			return
		}
		ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), extractBudget)
		defer cancel()
		if _, err := mc.Extract(ectx, sessionID, seq, text, route); err != nil {
			app.Log.Warn("turn memory extraction failed", "session_id", sessionID, "error", err)
		}
	})
	if missionDriver != nil {
		// Same mc.Extract entry point chat's own MemoryExtract rides,
		// fed the mission's curated digest instead of a chat turn's
		// residue — missions/memory.go builds the digest, this closure
		// only owns the flag gate, timeout, and error logging, exactly
		// like chat's own wiring above.
		missionDriver.SetMemoryExtract(func(ctx context.Context, sessionID string, seq int64, text, route string) {
			if !flags.Enabled(ctx, settings.KeyMemoryExtraction) {
				return
			}
			ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), extractBudget)
			defer cancel()
			if _, err := mc.Extract(ectx, sessionID, seq, text, route); err != nil {
				app.Log.Warn("mission memory extraction failed", "session_id", sessionID, "error", err)
			}
		})
	}
	compactor.SetMemoryExtract(func(ctx context.Context, sessionID string, seq int64, text, route string) []string {
		if !flags.Enabled(ctx, settings.KeyMemoryExtraction) {
			return nil
		}
		// Own deadline WITHIN the compaction budget: extraction must
		// never starve the summarize that follows it.
		ectx, cancel := context.WithTimeout(ctx, preCompactExtractBudget)
		defer cancel()
		ids, err := mc.Extract(ectx, sessionID, seq, text, route)
		if err != nil {
			app.Log.Warn("pre-compaction extraction failed", "session_id", sessionID, "error", err)
			return nil
		}
		return ids
	})
	svc.SetMemoryRetrieve(func(ctx context.Context, sessionID, query string) string {
		rctx, cancel := context.WithTimeout(ctx, retrieveBudget)
		defer cancel()
		memories, err := mc.Retrieve(rctx, sessionID, query)
		if err != nil {
			app.Log.Warn("memory retrieval failed; turn continues without", "session_id", sessionID, "error", err)
			return ""
		}
		return memclient.RenderBlock(memories)
	})

	attachmentStore := buildAttachments(app.DB, app.Log)
	// Nil-safe: a nil *attachments.Store boxed into the AttachmentStore
	// interface would be a non-nil interface value, breaking chat.go's
	// `s.attachments == nil` gate — so this only wires when non-nil,
	// same guard shape as the missionHub check above.
	if attachmentStore != nil {
		svc.SetAttachments(attachmentStore)
	}
	if markitdownURL != "" {
		svc.SetMarkitdown(markitdownURL)
	}

	// kb_search: nil-safe wiring, same shape as memory retrieve/extract
	// above — mc satisfies IngestDocument/KBSearch unconditionally, so
	// this always wires (memoryd unreachable surfaces as a per-call
	// error, not a nil-gate, since MEMORYD_URL always resolves to a
	// default).
	kbStore := kb.New(app.DB)
	// Ingest goroutines die with the process; fail anything a previous
	// run left mid-ingest so the UI offers reingest instead of an
	// eternal spinner. On its own goroutine behind WaitHealthy: at this
	// point in boot the pool is still connecting (the connector load
	// and mission recovery sweep hit the same window and retry).
	go func() {
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := app.DB.WaitHealthy(wctx); err != nil {
			app.Log.Warn("kb stale-ingest sweep skipped: database not ready", "error", err)
			return
		}
		if n, err := kbStore.SweepStale(wctx); err != nil {
			app.Log.Warn("kb stale-ingest sweep failed", "error", err)
		} else if n > 0 {
			app.Log.Info("kb stale-ingest sweep", "documents_failed", n)
		}
	}()
	svc.SetKBSearch(func(ctx context.Context, query string, collectionNames []string, mode string, k int) ([]builtin.KBSearchHit, error) {
		hits, err := mc.KBSearch(ctx, query, collectionNames, mode, k)
		if err != nil {
			return nil, err
		}
		out := make([]builtin.KBSearchHit, len(hits))
		for i, h := range hits {
			out[i] = builtin.KBSearchHit{
				DocumentID: h.DocumentID, DocumentTitle: h.DocumentTitle, Breadcrumb: h.Breadcrumb,
				Content: h.Content, SourceRef: h.SourceRef,
			}
		}
		return out, nil
	})
	svc.SetKBRead(kbReadFromStore(kbStore))

	usageDecorator := api.NewUsageDecorator(flags, fxStore)
	// ledgerAgg backs the mission list's top_model decoration (D-05x):
	// same *pgpool.Pool the rest of brain shares, no separate wiring.
	ledgerAgg := ledger.NewAggregator(app.DB)
	// destinationTest is nil-boxed the same way connLister is inside
	// Register itself: a nil *destinations.Deliverer passed straight as
	// api's destinationTester interface would be a non-nil interface
	// holding nil. interface{ Test(...) } here structurally matches
	// api.destinationTester without naming that unexported type.
	var destinationTest interface {
		Test(ctx context.Context, id string) error
	}
	if destinationDeliverer != nil {
		destinationTest = destinationDeliverer
	}
	api.Register(app.Server, svc, store, broker,
		memoryProxy(memorydURL, app.Log), adminProxy(gatewayURL, usageDecorator.Decorate, app.Log), flags, fxStore,
		agentReg, conns, goog, secrets, agent, packs, missionStore, missionDriver, missionNotifier,
		missionWorkspace, resolveSecret, routeForRole, chat.ClassifyOverGateway(gwc), gwc.ResolveRoute, chat.TitleOverGateway(gwc, app.Log), ledgerAgg.TopModelByMission, missionHub, attachmentStore, &http.Client{}, whisperURL, markitdownURL, token, app.Log, gwc, kbStore, mc, destinationStore, destinationTest)

	if err := app.Run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		app.Log.Error("server exited", "error", err)
		os.Exit(1)
	}
}

// buildSecretStore builds brain's secret-store handle (same DB, same
// master key as the gateway) — shared by connectors and mission push,
// so it's built once here rather than each caller decoding the master
// key independently. A nil return (with an error to log) means an
// unusable master key or init failure; callers nil-gate on it.
// kbReadFromStore builds the kb_read backing lookup: the full stored
// markdown straight from brain's own kb store — no memoryd round trip.
// A document outside the caller's allowed collections reads as not
// found (never "forbidden": the distinction would leak that the id
// exists).
func kbReadFromStore(kbStore *kb.Store) func(ctx context.Context, documentID string, collectionNames []string) (builtin.KBDocument, error) {
	return func(ctx context.Context, documentID string, collectionNames []string) (builtin.KBDocument, error) {
		doc, err := kbStore.GetDocument(ctx, documentID)
		if err != nil {
			return builtin.KBDocument{}, fmt.Errorf("document %s not found", documentID)
		}
		col, err := kbStore.GetCollection(ctx, doc.CollectionID)
		if err != nil || !slices.Contains(collectionNames, col.Name) {
			return builtin.KBDocument{}, fmt.Errorf("document %s not found", documentID)
		}
		return builtin.KBDocument{Title: doc.Title, SourceRef: doc.SourceRef, Markdown: doc.Markdown}, nil
	}
}

func buildSecretStore(db *pgpool.Pool, log *slog.Logger) (*secretstore.Store, error) {
	masterKey, err := secretstore.DecodeMasterKey(os.Getenv(secretstore.MasterKeyEnv))
	if err != nil {
		return nil, fmt.Errorf("no usable master key: %w", err)
	}
	secrets, err := secretstore.New(db, masterKey)
	if err != nil {
		return nil, fmt.Errorf("secret store init failed: %w", err)
	}
	return secrets, nil
}

// buildAttachments wires the image-attachment store (D-045).
// ATTACHMENTS_DIR unset means the feature is off: no store, chat
// rejects attachment refs, and the API surface stays unmounted.
func buildAttachments(db *pgpool.Pool, log *slog.Logger) *attachments.Store {
	dir := os.Getenv("ATTACHMENTS_DIR")
	if dir == "" {
		log.Info("ATTACHMENTS_DIR not set; image attachments disabled")
		return nil
	}
	return attachments.New(dir, db)
}

// buildConnectors wires the integration control plane. secrets is
// brain's already-built secret-store handle (nil when unavailable,
// e.g. no valid master key) — a nil store still nil-gates the
// connector surface exactly as before, it's just built once in main()
// instead of here. The Google half additionally needs
// TIMOTHY_PUBLIC_URL for the OAuth redirect; without it google
// connectors are configured but cannot connect.
func buildConnectors(db *pgpool.Pool, secrets *secretstore.Store, log *slog.Logger) (*connectors.Manager, *connectors.Google) {
	if secrets == nil {
		log.Warn("connectors disabled: no secret store")
		return nil, nil
	}
	resolve := func(ctx context.Context, ref string) (string, error) {
		return secrets.Resolve(ctx, ref)
	}
	store := connectors.NewStore(db, log)
	mgr := connectors.NewManager(store, resolve, log)

	publicURL := os.Getenv("TIMOTHY_PUBLIC_URL")
	if publicURL == "" {
		log.Warn("TIMOTHY_PUBLIC_URL not set; google connectors cannot run their OAuth flow")
	}
	goog := connectors.NewGoogle(secrets, store, publicURL, log)
	if markItDownURL := os.Getenv("MARKITDOWN_URL"); markItDownURL != "" {
		goog.MarkItDownURL = markItDownURL
	} else {
		log.Warn("MARKITDOWN_URL not set; gmail_read falls back to a snippet for HTML-only mail, and gmail_read_attachment is unavailable")
	}
	return mgr, goog
}

// buildDestinations wires the destinations control plane (store +
// adapters + Deliverer). missionStore nil (WORKSPACES unset) disables
// it entirely — delivery has no meaning without missions. conns/goog
// nil still builds the store (webhook destinations work without
// connectors); an email destination's create/update then fails
// validation with a clear error, same nil-gated shape as
// api/missions.go's own repo_url-needs-connectors check.
func buildDestinations(db *pgpool.Pool, conns *connectors.Manager, goog *connectors.Google, flags *settings.Store, missionStore *missions.Store, log *slog.Logger) (*destinations.Store, *destinations.Deliverer) {
	if missionStore == nil {
		return nil, nil
	}
	var connLookup destinationConnectorLookup
	if conns != nil {
		connLookup = destinationConnectorLookup{conns}
	}
	store := destinations.NewStore(db, connLookup, log)
	// goog nil (no secret store) leaves Mail nil: EmailAdapter.Deliver
	// then fails per-delivery with a clear error rather than a create-
	// time block, matching how an email destination's create itself
	// already requires an enabled google connector to exist.
	var email *destinations.EmailAdapter
	if goog != nil {
		email = &destinations.EmailAdapter{Mail: goog}
	}
	webhook := &destinations.WebhookAdapter{}
	deliverer := destinations.NewDeliverer(store, missionStore, email, webhook, flags.WebBaseURL, log)
	return store, deliverer
}

// destinationConnectorLookup adapts *connectors.Manager to
// destinations' own narrow Connector/connectorLookup shape — missions
// has no compile-time dependency on the connectors package's own
// Connector type, same reasoning as connsPRSource above. A zero value
// (conns == nil) is never boxed here; buildDestinations only
// constructs one when conns is non-nil.
type destinationConnectorLookup struct {
	conns *connectors.Manager
}

func (d destinationConnectorLookup) Get(ctx context.Context, id string) (destinations.Connector, error) {
	c, err := d.conns.Store().Get(ctx, id)
	if err != nil {
		return destinations.Connector{}, err
	}
	return destinations.Connector{Kind: c.Kind, Enabled: c.Enabled}, nil
}

// missionWorkSlotMax bounds how many missions may be status='working'
// at once — the absolute ceiling; in practice the D-056 memory
// admission gate (sandboxd's /capacity) binds first, since a host tight
// on memory denies well before 4 concurrent missions.
const missionWorkSlotMax = 4

// missionAgentResolver adapts agentReg.ResolveByID to scheduler.go's
// AgentResolver / driver.go's SetAgentResolver shape — both need the
// SAME resolution (route/review_route/budget/prompt_overlay/
// approval_allowlist from an agents row), just at different moments
// (schedule fire time vs mission provisioning time), so one adapter
// serves both call sites.
func missionAgentResolver(agentReg *agents.Store) missions.AgentResolver {
	return func(ctx context.Context, agentID string) (missions.AgentDefaults, bool) {
		a, ok := agentReg.ResolveByID(ctx, agentID)
		if !ok {
			return missions.AgentDefaults{}, false
		}
		return missions.AgentDefaults{
			Route: a.Route, ReviewRoute: a.ReviewRoute, PromptOverlay: a.PromptOverlay,
			BudgetAmount: a.BudgetUSD, ApprovalAllowlist: a.ApprovalAllowlist,
			Knowledge: a.Knowledge,
		}, true
	}
}

// missionConnectorReadsResolver adapts agentReg + conns to
// missions.ConnectorReadsResolver: an agent's Tools allowlist
// intersected with every built connector's ReadOnly-marked, non-MCP
// tools (connectors.Manager.ReadOnlyTools already excludes MCP and
// non-read-only tools). tools.ToolMatches is the same suffix rule
// filterDefs/matchGrant use, so an allowlist entry authored before any
// connector name is known (e.g. "gmail_search") still matches the
// namespaced tool at resolve time.
func missionConnectorReadsResolver(agentReg *agents.Store, conns *connectors.Manager) missions.ConnectorReadsResolver {
	return func(ctx context.Context, agentID string) []*tools.Tool {
		a, ok := agentReg.ResolveByID(ctx, agentID)
		if !ok {
			return nil
		}
		return intersectReadOnlyConnectorTools(a.Tools, conns.ReadOnlyTools())
	}
}

// intersectReadOnlyConnectorTools is missionConnectorReadsResolver's
// pure matching step, split out so it's unit-testable without a real
// agents.Store/connectors.Manager (both need a live Postgres pool):
// every available (already ReadOnly-marked, non-MCP) tool whose name
// tools.ToolMatches an entry in allow. Empty allow (agent has no Tools
// at all) matches nothing, same as filterDefs' empty-allow-means-
// everything convention does NOT apply here — an agent that has never
// opted into any tool gets no connector reads either.
func intersectReadOnlyConnectorTools(allow []string, available []*tools.Tool) []*tools.Tool {
	if len(allow) == 0 {
		return nil
	}
	var out []*tools.Tool
	for _, t := range available {
		for _, a := range allow {
			if tools.ToolMatches(t.Name, a) {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// buildMissions wires the mission engine (Store, Driver, Notifier,
// Scheduler, Hub). Gated on WORKSPACES: no workspace root configured
// means missions stay entirely inert — no goroutines started, nothing
// scheduled, the API surface unmounted (registerMissions 404s on a nil
// store). agentReg is D-034's agent registry, resolved at scheduler
// fire time and mission provisioning time (never schedule-create
// time) so an agent edited after the fact still applies. The hub
// lives inside the same gate as everything else here — no missions,
// no push events either.
func buildMissions(ctx context.Context, db *pgpool.Pool, agent *loop.Agent, sessions *session.Store, toolWorkspaceRoot string, flags *settings.Store, sandboxMgr *sandboxclient.Client, agentReg *agents.Store, routeForRole func(context.Context, string) string, fxStore *fxrates.Store, gwc *gwclient.Client, secrets *secretstore.Store, conns *connectors.Manager, mc *memclient.Client, log *slog.Logger) (*missions.Store, *missions.Driver, *missions.Notifier, *missions.Workspace, *missions.Hub) {
	root := os.Getenv("WORKSPACES")
	if root == "" {
		log.Info("WORKSPACES not set; missions disabled")
		return nil, nil, nil, nil, nil
	}
	hub := missions.NewHub()
	store := missions.NewStore(db, log)
	store.SetHub(hub)
	identity := func(ctx context.Context) (string, string) {
		return flags.Value(ctx, settings.ValueGitAuthorName), flags.Value(ctx, settings.ValueGitAuthorEmail)
	}
	workspace := missions.NewWorkspace(root, identity, log)
	parker := missions.NewStorePermissionParker(store, log)
	// MISSION_MODEL_FLOOR lists model-name substrings (comma-separated)
	// too weak to drive tool-using mission turns; a turn served by one
	// pauses the mission immediately instead of burning its iteration
	// budget. Unset = floor disabled.
	var floorDeny []string
	for _, s := range strings.Split(os.Getenv("MISSION_MODEL_FLOOR"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			floorDeny = append(floorDeny, s)
		}
	}
	// sandboxMgr routes model-authored command execution (the
	// worker/reviewer shell, verify_cmd) OUT of brain's own process,
	// through sandboxd, into a per-mission Docker container.
	// kb_search: nil-safe (mc is never nil, MEMORYD_URL always resolves
	// to a default), same shape as chat's own SetKBSearch wiring — the
	// mission's OWN Knowledge snapshot (never a live agent lookup)
	// scopes collections per turn (missions.nativeRunner.kbSearchTool).
	kbSearch := func(ctx context.Context, query string, collectionNames []string, mode string, k int) ([]builtin.KBSearchHit, error) {
		hits, err := mc.KBSearch(ctx, query, collectionNames, mode, k)
		if err != nil {
			return nil, err
		}
		out := make([]builtin.KBSearchHit, len(hits))
		for i, h := range hits {
			out[i] = builtin.KBSearchHit{
				DocumentID: h.DocumentID, DocumentTitle: h.DocumentTitle, Breadcrumb: h.Breadcrumb,
				Content: h.Content, SourceRef: h.SourceRef,
			}
		}
		return out, nil
	}
	nativeRunner := missions.NewNativeRunnerWithFloor(agent, parker, floorDeny, sandboxMgr.Exec, kbSearch, kbReadFromStore(kb.New(db)), log)
	if conns != nil {
		nativeRunner.SetConnectorReads(missionConnectorReadsResolver(agentReg, conns))
	}
	// The delegated runner wraps native with D-051/D-052's CLI-executor
	// dispatch — resolve a worker route's chain via the gateway, spawn a
	// harness entry's CLI detached in the mission's own sandbox container,
	// poll it to a verdict. Only wired when a sandbox manager is present
	// (missions already require one for the native shell path); nativeRunner
	// alone is otherwise the floor, same as before this feature existed.
	runner := buildDelegatedRunner(nativeRunner, store, gwc, secrets, sandboxMgr, db, log)
	webhookURL := os.Getenv("NOTIFY_WEBHOOK_URL")
	notifier := missions.NewNotifier(db, webhookURL, log)
	notifier.SetHub(hub)
	// A second tools.Permissions instance, not the one buildAgent built —
	// it's stateless besides the shared db/root (Grant/Resolve hit
	// Postgres directly), so a fresh instance behaves identically. Used
	// only to pre-authorize a mission's hidden session at creation.
	perms := tools.NewPermissions(db, toolWorkspaceRoot)
	driver := missions.NewDriver(store, runner, workspace, notifier, sessions, perms, sandboxMgr.Exec, sandboxMgr, log)
	driver.SetFXRates(fxStore)
	driver.SetCapacityGate(sandboxMgr)
	driver.SetGitBranchPattern(flags.GitBranchPattern)
	driver.SetGitCommitStyle(flags.GitCommitStyle)
	resolveAgent := missionAgentResolver(agentReg)
	driver.SetAgentResolver(resolveAgent)
	driver.SetNameMission(chat.TitleOverGateway(gwc, log))
	if conns != nil && secrets != nil {
		// A repo_url mission's clone token: resolve connector_id straight
		// to its credential_ref's secret value, same as resolveSecret does
		// for the push endpoint — no need to build a full connector
		// Source just to read one credential.
		driver.SetCloneTokenResolver(func(ctx context.Context, connectorID string) (string, error) {
			c, err := conns.Store().Get(ctx, connectorID)
			if err != nil {
				return "", fmt.Errorf("resolve connector %s: %w", connectorID, err)
			}
			return secrets.Resolve(ctx, c.CredentialRef)
		})
		// A repo_url mission's clone commit identity: reuse the same
		// TestIdentity path Settings' connector test button calls, so
		// commits inside the clone are authored as the connection, not the
		// operator's fixed identity. Best-effort at the driver layer
		// (SetCloneIdentityResolver's caller already treats a resolve
		// error as WARN-and-fall-back, never a provisioning failure).
		driver.SetCloneIdentityResolver(func(ctx context.Context, connectorID string) (missions.ResolvedIdentity, error) {
			identity, err := conns.TestIdentity(ctx, connectorID)
			if err != nil {
				return missions.ResolvedIdentity{}, err
			}
			if identity == nil {
				return missions.ResolvedIdentity{}, fmt.Errorf("connector %s has no identity to resolve", connectorID)
			}
			name := identity.Name
			if name == "" {
				name = identity.Login
			}
			result := missions.ResolvedIdentity{Name: name, Email: identity.Email, Login: identity.Login}
			// Signing key resolution is best-effort: a connector row with
			// sign_commits set but a deleted/missing secret must never fail
			// provisioning, just fall back to unsigned commits (same as
			// SetCloneIdentityResolver's own resolve-error contract).
			if c, err := conns.Store().Get(ctx, connectorID); err == nil {
				var cfg connectors.GitHubConfig
				if json.Unmarshal(c.Config, &cfg) == nil && cfg.SignCommits {
					key, err := secrets.Resolve(ctx, connectors.SigningKeyRefSuffix(c.CredentialRef))
					if err != nil {
						log.Warn("driver: signing key resolve failed; commits go unsigned", "connector_id", connectorID, "error", err)
					} else {
						result.SigningKey = key
					}
				}
			}
			return result, nil
		})
	}
	if conns != nil && secrets != nil {
		// The auto-fire-on-done hook's push token: resolve connector_id
		// straight to its credential_ref's secret value, same as the
		// clone token resolver above and the push endpoint's own
		// resolvePushToken — the on_complete path never has an explicit
		// credential_ref override, only ever the mission's own connector.
		resolvePushToken := func(ctx context.Context, connectorID string) (string, error) {
			c, err := conns.Store().Get(ctx, connectorID)
			if err != nil {
				return "", fmt.Errorf("resolve connector %s: %w", connectorID, err)
			}
			return secrets.Resolve(ctx, c.CredentialRef)
		}
		driver.SetCompleter(missions.NewCompleter(workspace, store, resolvePushToken, connsPRSource{conns}))
		driver.SetPushFailedNotifier(func(ctx context.Context, missionID, message string) {
			if err := notifier.NotifyMessage(ctx, missionID, "push_failed", message); err != nil {
				log.Warn("driver: on_complete push_failed notification failed", "mission_id", missionID, "error", err)
			}
		})
	}

	schedulerEnabled := func(ctx context.Context) bool { return flags.Enabled(ctx, settings.KeyScheduler) }
	// routeExists backs DefaultCodingRoute's preference check for a
	// coding template's route (see api/missions.go's own copy of this
	// wiring for the create-request path) — false on any resolve error,
	// never a hard failure.
	routeExists := func(ctx context.Context, name string) bool {
		_, err := gwc.ResolveRoute(ctx, name, "")
		return err == nil
	}
	scheduler := missions.NewScheduler(db, store, resolveAgent, schedulerEnabled, routeForRole, routeExists, flags.CodingExecutor, log)
	go scheduler.Run(ctx)
	return store, driver, notifier, workspace, hub
}

// connsPRSource adapts *connectors.Manager to missions.PRSource for the
// driver's on_complete auto-fire hook — missions has no compile-time
// dependency on the connectors package, same reasoning as
// CloneTokenResolver's closure-based wiring above.
type connsPRSource struct {
	conns *connectors.Manager
}

func (c connsPRSource) DefaultBranch(ctx context.Context, connectorID, owner, repo string) (string, error) {
	repoInfo, err := c.conns.GetRepo(ctx, connectorID, owner, repo)
	if err != nil {
		return "", err
	}
	return repoInfo.DefaultBranch, nil
}

func (c connsPRSource) CreatePR(ctx context.Context, connectorID, owner, repo, title, head, base, body string) (string, int, error) {
	created, err := c.conns.CreatePR(ctx, connectorID, owner, repo, title, head, base, body)
	if err != nil {
		return "", 0, err
	}
	return created.HTMLURL, created.Number, nil
}

// toMissionRecord adapts a missions.Mission into the builtin package's
// own MissionRecord shape — see MissionRecord's doc comment for why
// builtin can't import missions.Mission directly. NotPushable is
// precomputed here since missions.NotPushable also lives in the
// missions package.
func toMissionRecord(m missions.Mission) builtin.MissionRecord {
	passed := 0
	for _, u := range m.Spec.Units {
		if u.Passes {
			passed++
		}
	}
	return builtin.MissionRecord{
		ID: m.ID, Name: m.Name, Goal: m.Goal, Kind: m.Kind,
		Phase: string(m.Phase), Status: string(m.Status), Iteration: m.Iteration,
		Harness: m.Harness, RepoURL: m.RepoURL, Branch: m.Branch, ConnectorID: m.ConnectorID,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
		UnitsPassed: passed, UnitsTotal: len(m.Spec.Units),
		PauseReason: string(m.PauseReason), PauseMessage: m.PauseMessage,
		OnComplete:        m.OnComplete,
		NotPushableReason: missions.NotPushable(m),
	}
}

// missionToolStore adapts *missions.Store to the builtin package's
// missions/mission_push tool interfaces (missionLister,
// missionEventReader) — a thin translation layer since builtin cannot
// import missions.Mission/missions.Event directly (import cycle, see
// MissionRecord's doc comment).
type missionToolStore struct {
	store *missions.Store
}

func (a missionToolStore) ListMissions(ctx context.Context, limit int) ([]builtin.MissionRecord, error) {
	all, err := a.store.List(ctx, missions.ListFilter{Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]builtin.MissionRecord, len(all))
	for i, m := range all {
		out[i] = toMissionRecord(m)
	}
	return out, nil
}

func (a missionToolStore) GetMission(ctx context.Context, id string) (builtin.MissionRecord, error) {
	m, err := a.store.Get(ctx, id)
	if err != nil {
		return builtin.MissionRecord{}, err
	}
	return toMissionRecord(m), nil
}

func (a missionToolStore) MissionEvents(ctx context.Context, id string) ([]builtin.MissionEvent, error) {
	evs, err := a.store.Events(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]builtin.MissionEvent, len(evs))
	for i, e := range evs {
		out[i] = builtin.MissionEvent{Kind: e.Kind, Payload: e.Payload}
	}
	return out, nil
}

// missionCompleterAdapter adapts *missions.Completer to the builtin
// package's missionCompleter interface — mission_push's push/PR calls
// go through the exact same Completer the button/auto-fire paths use.
// It re-Gets the mission by id from the real store before calling
// Completer, so Completer always acts on the authoritative
// missions.Mission (worktree, full spec for PRBody, ...) rather than a
// partial copy shuttled through builtin.MissionRecord.
type missionCompleterAdapter struct {
	store     *missions.Store
	completer *missions.Completer
}

func (a missionCompleterAdapter) PushMissionBranch(ctx context.Context, id, token string) (string, error) {
	m, err := a.store.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("no mission found with id %q", id)
	}
	return a.completer.PushBranch(ctx, m, token)
}

func (a missionCompleterAdapter) OpenMissionPR(ctx context.Context, id, token string) (string, int, error) {
	m, err := a.store.Get(ctx, id)
	if err != nil {
		return "", 0, fmt.Errorf("no mission found with id %q", id)
	}
	return a.completer.OpenPR(ctx, m, token)
}

// credResolveTimeout bounds one credential_ref resolution the delegated
// runner does before spawning a CLI executor — same 3s bound
// cmd/gateway/main.go's credentialLookup uses for the identical
// secretstore.Resolve call.
const credResolveTimeout = 3 * time.Second

// buildDelegatedRunner wraps native with missions.NewDelegatedRunner
// when a sandbox manager is present — missions already require one for
// the native shell/verify_cmd path, so its absence here would mean
// missions are disabled entirely (buildMissions already returned early
// in that case). A nil secrets store still lets subscription-mode
// executors run (the literal "subscription" credential_ref never
// resolves through it); api_key-mode executors simply have no
// resolver, so resolveCredential's ErrExecutorAuth path is what a
// mission sees instead of a working key.
func buildDelegatedRunner(native missions.Runner, store *missions.Store, gwc *gwclient.Client, secrets *secretstore.Store, sandboxMgr *sandboxclient.Client, db *pgpool.Pool, log *slog.Logger) missions.Runner {
	if sandboxMgr == nil {
		return native
	}
	var resolveCred func(context.Context, string) (string, error)
	if secrets != nil {
		resolveCred = func(ctx context.Context, ref string) (string, error) {
			rctx, cancel := context.WithTimeout(ctx, credResolveTimeout)
			defer cancel()
			return secrets.Resolve(rctx, ref)
		}
	}
	led := ledger.New(db, log)
	return missions.NewDelegatedRunner(native, gwc.ResolveRoute, resolveCred, sandboxMgr.ExecEnv, store, store.LastRunState, led, log)
}

// memoryProxy forwards the web's memory-management routes to memoryd
// verbatim — brain adds only the bearer gate. The search route maps
// onto memoryd's retrieve endpoint.
func memoryProxy(memorydURL string, log *slog.Logger) http.Handler {
	target, err := url.Parse(memorydURL)
	if err != nil {
		log.Error("invalid MEMORYD_URL; memory routes disabled", "error", err)
		return nil
	}
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			if r.In.URL.Path == "/v1/memories/search" {
				r.Out.URL.Path = "/v1/retrieve"
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("memoryd proxy error", "path", r.URL.Path, "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"code":"memoryd_unreachable","message":"memory service unavailable"}}`))
		},
	}
}

// adminProxy forwards the browser's control-plane reads to the
// gateway's internal API: /v1/admin/usage/* maps onto
// /internal/usage/*. Brain adds the bearer gate and, for the usage
// sub-tree only, decorates money fields with a converted_amount in the
// user's default_currency (usageDecorate) — the gateway itself has no
// settings access and no fx_rates reader (it's a separate, settings-
// unaware service), so this is the one seam where both are already
// available. Every other admin endpoint (providers, routes, secrets)
// passes through byte-for-byte untouched.
func adminProxy(gatewayURL string, usageDecorate func(*http.Response) error, log *slog.Logger) http.Handler {
	target, err := url.Parse(gatewayURL)
	if err != nil {
		log.Error("invalid GATEWAY_URL; admin routes disabled", "error", err)
		return nil
	}
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = "/internal/admin/" + strings.TrimPrefix(r.In.URL.Path, "/v1/admin/")
		},
		ModifyResponse: func(resp *http.Response) error {
			// resp.Request is the OUTBOUND request — Rewrite above has
			// already turned /v1/admin/... into /internal/admin/..., so
			// the scope check must match the rewritten prefix.
			if usageDecorate == nil || !strings.HasPrefix(resp.Request.URL.Path, "/internal/admin/usage/") {
				return nil
			}
			return usageDecorate(resp)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("gateway admin proxy error", "path", r.URL.Path, "error", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"code":"gateway_unreachable","message":"gateway unavailable"}}`))
		},
	}
}

// gatedCompactor honors the compaction feature switch: off means
// sessions simply grow until it is flipped back on.
type gatedCompactor struct {
	inner chat.Compactor
	flags *settings.Store
}

func (g gatedCompactor) MaybeCompact(ctx context.Context, sessionID string) error {
	if !g.flags.Enabled(ctx, settings.KeyCompaction) {
		return nil
	}
	return g.inner.MaybeCompact(ctx, sessionID)
}

// turnRouter sends chat turns through the agent loop and everything
// else (titles, distills, compaction summaries) straight to the
// gateway. With the tools switch off, chat turns bypass the agent
// loop entirely — plain pass-through completion.
type turnRouter struct {
	agent *loop.Agent
	gw    chat.Gateway
	flags *settings.Store
}

func (r turnRouter) Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	if req.Purpose != "chat" || !r.flags.Enabled(ctx, settings.KeyTools) {
		return r.gw.Stream(ctx, req)
	}
	return r.agent.Start(ctx, loop.Request{
		SessionID:  req.SessionID,
		Route:      req.Route,
		Agent:      req.Agent,
		ToolAllow:  req.ToolAllow,
		ExtraTools: req.ExtraTools,
		ModelHint:  req.ModelHint,
		System:     req.System,
		Messages:   req.Messages,
	})
}

func (r turnRouter) RouteForRole(ctx context.Context, role string) (string, bool, error) {
	return r.gw.RouteForRole(ctx, role)
}

// buildAgent assembles the compiled-in tool registry and its guard
// rails (D-009, D-010). The returned builtin set is the fixed half of
// the tool surface; connector tools join it via swapAgentTools.
func buildAgent(gwc *gwclient.Client, store *session.Store, db *pgpool.Pool, workspace, searxngURL, markitdownURL string, packs []skills.Skill, skillAllow func(context.Context, string) bool, remember builtin.RememberFunc, log *slog.Logger, toolCalls *prometheus.CounterVec, sensitiveRoute func(context.Context) string, fxStore *fxrates.Store) (*loop.Agent, *loop.PermBroker, *tools.Outputs, []*tools.Tool, *tools.Permissions, error) {
	outputs := tools.NewOutputs(db)
	set := []*tools.Tool{
		builtin.CurrentTime(time.Now),
		builtin.ConvertTime(),
		builtin.Calculator(),
		builtin.WebFetch(builtin.WebFetchConfig{MarkitdownURL: markitdownURL}),
		builtin.Shell(builtin.ShellConfig{WorkspaceRoot: workspace}),
		builtin.RetrieveOutput(outputs),
		builtin.Remember(remember),
		builtin.ConvertCurrency(builtin.CurrencyLookupFromStore(fxStore)),
	}
	// Search is optional infra: only registered when a backend is
	// configured, so an environment without SearXNG still runs clean.
	if searxngURL != "" {
		set = append(set, builtin.WebSearch(searxngURL))
	}
	if len(packs) > 0 {
		set = append(set, skills.LoadSkillTool(packs, skillAllow))
	}
	constrained, defs, err := compileToolset(set, nil, log, toolCalls)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	perms := tools.NewPermissions(db, workspace)
	broker := loop.NewPermBroker()
	agent := loop.NewAgent(gwc, constrained, perms, outputs, tools.NewAudit(db), store, broker, defs, log)
	// Mission-driven turns (Request.BuiltinsOnly) get this compiled-in
	// set only — never connector tools or the chat-only mission tools
	// registered later in main(), since neither exists yet at this
	// point. This snapshot never changes at runtime, unlike the shared
	// registry SwapTools maintains.
	agent.SetBaseTools(constrained, defs)
	// Shell dumps grow fast; offload them sooner than the default so a
	// long command output never bloats the context (D-019).
	agent.SetOffloadThreshold("shell", 4<<10)
	// Reading real email content is sensitive: once a Gmail read tool
	// fires, pin the rest of the turn to a trusted route (e.g. one
	// chained only to a local Ollama provider) instead of whatever
	// route the turn started on — optional, since not everyone runs a
	// local model. sensitiveRoute resolving to "" at flip time means
	// the feature is currently off; wired unconditionally since the
	// route is now runtime-configurable from the settings UI, not just
	// boot-time env.
	for _, suffix := range sensitiveToolSuffixes {
		agent.SetForceRoute(suffix, sensitiveRoute)
	}
	return agent, broker, outputs, set, perms, nil
}

// compileToolset registers builtin + connector tools into a fresh
// constrained registry. A connector tool whose name collides with an
// existing tool is skipped with a log — a remote server must never
// shadow a builtin.
func compileToolset(builtins, connectorTools []*tools.Tool, log *slog.Logger, toolCalls *prometheus.CounterVec) (*tools.Constrained, []provider.ToolDef, error) {
	reg := tools.NewRegistry()
	for _, t := range builtins {
		if err := reg.Register(t); err != nil {
			return nil, nil, fmt.Errorf("register tools: %w", err)
		}
	}
	for _, t := range connectorTools {
		if err := reg.Register(t); err != nil {
			log.Warn("connector tool skipped", "tool", t.Name, "error", err)
		}
	}
	constrained, err := tools.NewConstrained(reg, toolCalls)
	if err != nil {
		return nil, nil, fmt.Errorf("compile tool schemas: %w", err)
	}
	constrained.SetClamp("shell", builtin.ShellTimeoutClamp())

	defs := make([]provider.ToolDef, 0, len(reg.List()))
	for _, t := range reg.List() {
		defs = append(defs, provider.ToolDef{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return constrained, defs, nil
}

// swapAgentTools recompiles builtin + connector tools and swaps them
// into the agent. A compile failure keeps the previous surface — a
// broken connector schema must not take down the builtins.
func swapAgentTools(agent *loop.Agent, builtins []*tools.Tool, conns *connectors.Manager, log *slog.Logger, toolCalls *prometheus.CounterVec) {
	constrained, defs, err := compileToolset(builtins, conns.Tools(), log, toolCalls)
	if err != nil {
		log.Warn("connector toolset compile failed; keeping previous tools", "error", err)
		return
	}
	agent.SwapTools(constrained, defs)
	log.Info("agent tool surface updated", "tools", len(defs))
}

// initialReloadRetry bounds the wait between initial-load retries: the
// DB pool is often still warming right at boot (ErrDegraded), and
// falling back to the full-minute ticker for that first success would
// leave chat turns running builtins-only tools for up to a minute.
const initialReloadRetry = 5 * time.Second

// runConnectorReload loads connector sources at startup and keeps
// retrying/refreshing on a slow tick, mirroring the gateway's snapshot
// poll: a DB that wasn't ready at boot, or a row edited outside the
// admin API, converges within a minute. The first load retries on the
// short initialReloadRetry cadence until it succeeds once, THEN falls
// into the normal minute ticker — later reloads don't need the fast
// path since the agent already has a tool surface.
func runConnectorReload(ctx context.Context, conns *connectors.Manager, log *slog.Logger) {
	for {
		if err := conns.Reload(ctx); err != nil {
			log.Warn("initial connector load failed; will retry", "error", err)
		} else {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(initialReloadRetry):
		}
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conns.Reload(ctx); err != nil {
				log.Warn("connector reload failed; keeping previous sources", "error", err)
			}
		}
	}
}

// runOutputGC sweeps expired offloaded outputs (D-019 retention).
func runOutputGC(ctx context.Context, outputs *tools.Outputs, log *slog.Logger) {
	const retention = 7 * 24 * time.Hour
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gctx, cancel := context.WithTimeout(ctx, time.Minute)
			removed, err := outputs.GC(gctx, retention)
			cancel()
			if err != nil {
				log.Warn("tool output gc", "error", err)
			} else if removed > 0 {
				log.Info("tool output gc", "removed", removed)
			}
		}
	}
}
