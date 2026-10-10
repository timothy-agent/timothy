package onboarding

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

var errProbe = errors.New("probe failed")

// fullProbes answers every probe positively; roles maps role to route
// name, routes maps route name to its usable flag.
func fullProbes() Probes {
	roles := map[string]string{"default": "r-default", "summarize": "r-sum", "embedding": "r-emb", "vision": "r-vis"}
	return Probes{
		GatewayReady: func(context.Context) (bool, string, error) { return true, "", nil },
		RouteForRole: func(_ context.Context, role string) (string, bool, error) {
			n, ok := roles[role]
			return n, ok, nil
		},
		ResolveRoute: func(_ context.Context, name, harness string) (*gwclient.ResolvedRoute, error) {
			return &gwclient.ResolvedRoute{Route: name, Entries: []gwclient.ResolvedRouteEntry{{Usable: false}, {Usable: true}}}, nil
		},
		SandboxHealth:       func(context.Context) error { return nil },
		HasAssistantReply:   func(context.Context) (bool, error) { return true, nil },
		HasSucceededMission: func(context.Context) (bool, error) { return true, nil },
		CountConnectors:     func(context.Context) (int, error) { return 2, nil },
		CountChannels:       func(context.Context) (int, error) { return 1, nil },
		CountKBCollections:  func(context.Context) (int, error) { return 3, nil },
		CountAutomations:    func(context.Context) (int, error) { return 4, nil },
		AutomationsEnabled:  func(context.Context) bool { return true },
		MissionModelFloor:   []string{"qwen2.5:7b", "nova"},
	}
}

var fullReadiness = Readiness{
	GatewayReady: true, ChatRoute: true, SummarizeRoute: true, EmbeddingRoute: true, VisionRoute: true,
	Sandbox: true, FirstChat: true, FirstMission: true,
	Connectors: 2, Channels: 1, KBCollections: 3, Automations: 4, AutomationsEnabled: true,
	MissionModelFloor: []string{"qwen2.5:7b", "nova"},
}

func TestCompute(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Probes)
		want   func(*Readiness)
	}{
		{"all ready", func(*Probes) {}, func(*Readiness) {}},
		{"nil probes", func(p *Probes) { *p = Probes{} }, func(r *Readiness) { *r = Readiness{} }},
		{"gateway unreachable", func(p *Probes) {
			p.GatewayReady = func(context.Context) (bool, string, error) { return false, "", errProbe }
		}, func(r *Readiness) {
			r.GatewayReady, r.ChatRoute, r.SummarizeRoute, r.EmbeddingRoute, r.VisionRoute = false, false, false, false, false
		}},
		{"gateway not ready", func(p *Probes) {
			p.GatewayReady = func(context.Context) (bool, string, error) { return false, "routing: loading", nil }
		}, func(r *Readiness) {
			r.GatewayReady, r.ChatRoute, r.SummarizeRoute, r.EmbeddingRoute, r.VisionRoute = false, false, false, false, false
		}},
		{"role unbound", func(p *Probes) {
			p.RouteForRole = func(_ context.Context, role string) (string, bool, error) { return role, role != "vision", nil }
		}, func(r *Readiness) { r.VisionRoute = false }},
		{"no usable entry", func(p *Probes) {
			p.ResolveRoute = func(_ context.Context, name, _ string) (*gwclient.ResolvedRoute, error) {
				if name == "r-emb" {
					return &gwclient.ResolvedRoute{Entries: []gwclient.ResolvedRouteEntry{{Usable: false}}}, nil
				}
				return &gwclient.ResolvedRoute{Entries: []gwclient.ResolvedRouteEntry{{Usable: true}}}, nil
			}
		}, func(r *Readiness) { r.EmbeddingRoute = false }},
		{"resolve errors", func(p *Probes) {
			p.ResolveRoute = func(context.Context, string, string) (*gwclient.ResolvedRoute, error) { return nil, errProbe }
		}, func(r *Readiness) {
			r.ChatRoute, r.SummarizeRoute, r.EmbeddingRoute, r.VisionRoute = false, false, false, false
		}},
		{"resolve nil", func(p *Probes) { p.ResolveRoute = nil }, func(r *Readiness) {
			r.ChatRoute, r.SummarizeRoute, r.EmbeddingRoute, r.VisionRoute = false, false, false, false
		}},
		{"sandbox down", func(p *Probes) { p.SandboxHealth = func(context.Context) error { return errProbe } }, func(r *Readiness) { r.Sandbox = false }},
		{"no first chat", func(p *Probes) { p.HasAssistantReply = func(context.Context) (bool, error) { return false, nil } }, func(r *Readiness) { r.FirstChat = false }},
		{"first chat errors", func(p *Probes) { p.HasAssistantReply = func(context.Context) (bool, error) { return true, errProbe } }, func(r *Readiness) { r.FirstChat = false }},
		{"no first mission", func(p *Probes) { p.HasSucceededMission = nil }, func(r *Readiness) { r.FirstMission = false }},
		{"count errors", func(p *Probes) {
			p.CountConnectors = func(context.Context) (int, error) { return 5, errProbe }
			p.CountChannels = nil
		}, func(r *Readiness) { r.Connectors, r.Channels = 0, 0 }},
		{"automations off", func(p *Probes) { p.AutomationsEnabled = func(context.Context) bool { return false } }, func(r *Readiness) { r.AutomationsEnabled = false }},
		{"no mission floor", func(p *Probes) { p.MissionModelFloor = nil }, func(r *Readiness) { r.MissionModelFloor = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := fullProbes()
			tc.mutate(&p)
			want := fullReadiness
			tc.want(&want)
			if got := Compute(t.Context(), p, discard()); !reflect.DeepEqual(got, want) {
				t.Fatalf("Compute =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

// TestComputeRespectsDeadline: a probe that blocks until its context
// ends reads as false once the caller's deadline passes.
func TestComputeRespectsDeadline(t *testing.T) {
	p := fullProbes()
	p.SandboxHealth = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := Compute(ctx, p, discard())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Compute took %v, want it bounded by the deadline", elapsed)
	}
	if got.Sandbox {
		t.Fatal("Sandbox = true for a probe that timed out")
	}
	if !got.ChatRoute {
		t.Fatal("ChatRoute = false, want the fast probes unaffected")
	}
}

// TestComputeInvalidatesBeforeRouteProbes: the cache drop must land
// before any route lookup, or the lookup still answers from cache.
func TestComputeInvalidatesBeforeRouteProbes(t *testing.T) {
	p := fullProbes()
	var mu sync.Mutex
	invalidated, lookedUpBefore := false, false
	p.Invalidate = func() {
		mu.Lock()
		invalidated = true
		mu.Unlock()
	}
	p.RouteForRole = func(_ context.Context, role string) (string, bool, error) {
		mu.Lock()
		if !invalidated {
			lookedUpBefore = true
		}
		mu.Unlock()
		return "r-" + role, true, nil
	}
	if got := Compute(t.Context(), p, discard()); !got.ChatRoute {
		t.Fatal("ChatRoute = false")
	}
	if !invalidated || lookedUpBefore {
		t.Fatalf("invalidated=%v lookedUpBefore=%v, want true/false", invalidated, lookedUpBefore)
	}
}

func ptr(b bool) *bool { return &b }

func TestProgressMerge(t *testing.T) {
	tests := []struct {
		name         string
		stored, patc Progress
		want         Progress
	}{
		{"empty onto empty", Progress{}, Progress{}, Progress{}},
		{"wizard set", Progress{}, Progress{Wizard: WizardDone}, Progress{Wizard: WizardDone}},
		{"wizard kept when patch empty", Progress{Wizard: WizardSkipped}, Progress{}, Progress{Wizard: WizardSkipped}},
		{"wizard pending clears", Progress{Wizard: WizardDone}, Progress{Wizard: WizardPending}, Progress{}},
		{"dismiss", Progress{}, Progress{ChecklistDismissed: ptr(true)}, Progress{ChecklistDismissed: ptr(true)}},
		{"undismiss", Progress{ChecklistDismissed: ptr(true)}, Progress{ChecklistDismissed: ptr(false)}, Progress{ChecklistDismissed: ptr(false)}},
		{"dismiss kept when nil", Progress{ChecklistDismissed: ptr(true)}, Progress{}, Progress{ChecklistDismissed: ptr(true)}},
		{"tours max per page", Progress{ToursSeen: map[string]int{"chat": 2, "kb": 1}}, Progress{ToursSeen: map[string]int{"chat": 1, "kb": 3, "missions": 1}},
			Progress{ToursSeen: map[string]int{"chat": 2, "kb": 3, "missions": 1}}},
		{"tour zero deletes", Progress{ToursSeen: map[string]int{"chat": 2}}, Progress{ToursSeen: map[string]int{"chat": 0}}, Progress{}},
		{"tours nil stored", Progress{}, Progress{ToursSeen: map[string]int{"chat": 1}}, Progress{ToursSeen: map[string]int{"chat": 1}}},
		{"visited union sorted", Progress{Visited: []string{"kb", "chat"}}, Progress{Visited: []string{"chat", "automations", "automations"}},
			Progress{Visited: []string{"automations", "chat", "kb"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.stored.Merge(tc.patc); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Merge = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestProgressMergeDoesNotAlias: the merged value never shares maps or
// pointers with its inputs.
func TestProgressMergeDoesNotAlias(t *testing.T) {
	stored := Progress{ToursSeen: map[string]int{"chat": 1}}
	patch := Progress{ChecklistDismissed: ptr(true)}
	got := stored.Merge(patch)
	got.ToursSeen["chat"] = 9
	*got.ChecklistDismissed = false
	if stored.ToursSeen["chat"] != 1 || !*patch.ChecklistDismissed {
		t.Fatal("Merge aliased its inputs")
	}
}

func TestProgressValidate(t *testing.T) {
	for _, tc := range []struct {
		p    Progress
		fail bool
	}{
		{Progress{}, false},
		{Progress{Wizard: WizardPending}, false},
		{Progress{Wizard: WizardSkipped}, false},
		{Progress{Wizard: WizardDone}, false},
		{Progress{Wizard: "later"}, true},
		{Progress{ToursSeen: map[string]int{"chat": 0}}, false},
		{Progress{ToursSeen: map[string]int{"chat": -1}}, true},
	} {
		if err := tc.p.Validate(); (err != nil) != tc.fail {
			t.Errorf("Validate(%+v) = %v, want fail=%v", tc.p, err, tc.fail)
		}
	}
}
