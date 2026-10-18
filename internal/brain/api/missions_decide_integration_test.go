//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/chat"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
	gatewayapi "github.com/SumonMSelim/timothy/internal/gateway/api"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
	"github.com/SumonMSelim/timothy/internal/platform/metrics"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

type staticSnapshot struct{ snap *router.Snapshot }

func (s staticSnapshot) Snapshot() *router.Snapshot { return s.snap }
func (s staticSnapshot) Load(context.Context) error { return nil }

type discardLedger struct{}

func (discardLedger) Record(context.Context, ledger.Entry) {}

// previewChatStub is a chat model that answers the classify prompt with
// "coding full", the kind question with "general" and the light
// question with "yes", recording every prompt.
type previewChatStub struct {
	mu      sync.Mutex
	prompts []string
}

func (c *previewChatStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var all strings.Builder
	for _, m := range body.Messages {
		all.WriteString(m.Content)
		all.WriteString("\n")
	}
	prompt := all.String()
	c.mu.Lock()
	c.prompts = append(c.prompts, prompt)
	c.mu.Unlock()
	reply := "coding full"
	switch {
	case strings.Contains(prompt, missions.KindQuestion):
		reply = "general"
	case strings.Contains(prompt, missions.LightQuestion):
		reply = "yes"
	}
	_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", reply)
	_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\n")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func (c *previewChatStub) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.prompts...)
}

// startPreviewGateway serves the real gateway API over routes on one
// chat stub: summarize always, and a decide route when bindDecide.
func startPreviewGateway(t *testing.T, stubURL string, bindDecide bool) string {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rows := []router.ProviderRow{{ID: "p1", Name: "preview-stub", Kind: "api", Driver: "openaicompat", BaseURL: stubURL, DefaultModel: "m1", Enabled: true}}
	routes := []router.RouteRow{{Name: "summarize", Role: "summarize", Chain: []router.ChainEntry{{ProviderID: "p1", Model: "m1"}}, Enabled: true}}
	if bindDecide {
		routes = append(routes, router.RouteRow{Name: "decider", Role: "decide", Chain: []router.ChainEntry{{ProviderID: "p1", Model: "m1"}}, Enabled: true})
	}
	snap, _ := router.BuildSnapshot(rows, routes, func(string) string { return "" }, nil)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve gateway port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	m := metrics.New()
	srv := httpserver.New(port, log, m, func() httpserver.Health { return httpserver.Health{Status: "ok"} })
	gatewayapi.Register(srv, staticSnapshot{snap}, discardLedger{}, log, m, "")
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	t.Cleanup(func() {
		stop()
		<-done
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(base + "/health")
		if err == nil {
			_ = resp.Body.Close()
			return base
		}
		if time.Now().After(deadline) {
			t.Fatalf("gateway not ready: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestMissionsClassifyOnDecideRole runs POST /v1/missions/classify
// through the real gateway on a chat model (D-174). Bound, kind and
// light come from two decide calls with the goal as state; unbound, the
// one classify prompt answers, as before the decide path.
func TestMissionsClassifyOnDecideRole(t *testing.T) {
	const goal = "write a short summary of this week's release notes"
	tests := []struct {
		name      string
		bind      bool
		wantKind  string
		wantLight bool
	}{
		{"decide bound", true, "general", true},
		{"decide unbound", false, "coding", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &previewChatStub{}
			stubSrv := httptest.NewServer(stub)
			defer stubSrv.Close()
			gwc := gwclient.New(startPreviewGateway(t, stubSrv.URL, tc.bind))

			a, _, _ := testAPI(t, "tok", nil)
			a.missionDecide = chat.MissionDecideOverGateway(gwc)
			mx := mux(a)
			store := missions.NewStore(pgpool.New(context.Background(), "postgres://invalid/nope", discard()), discard())
			a.registerMissions(mx.Handle, store, nil, nil, nil, nil, nil, nil, chat.ClassifyOverGateway(gwc), nil, nil, nil, nil, nil, "", nil, nil, nil, nil, "", nil)

			req := httptest.NewRequest("POST", "/v1/missions/classify", strings.NewReader(`{"goal":"`+goal+`"}`))
			req.Header.Set("Authorization", "Bearer tok")
			w := httptest.NewRecorder()
			mx.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("classify = %d %s", w.Code, w.Body.String())
			}
			var out struct {
				Kind  string `json:"kind"`
				Light bool   `json:"light"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.Kind != tc.wantKind || out.Light != tc.wantLight {
				t.Fatalf("classify = %+v, want kind %q light %v", out, tc.wantKind, tc.wantLight)
			}

			prompts := stub.seen()
			if !tc.bind {
				if len(prompts) != 1 || !strings.Contains(prompts[0], "Classify this mission goal along two independent axes") || !strings.Contains(prompts[0], "Goal: "+goal) {
					t.Fatalf("unbound prompts = %q, want the classify prompt once", prompts)
				}
				return
			}
			if len(prompts) != 2 {
				t.Fatalf("bound prompts = %d, want a kind and a light decide", len(prompts))
			}
			kindPrompt, lightPrompt := prompts[0], prompts[1]
			for _, o := range missions.KindOptions() {
				if !strings.Contains(kindPrompt, "- "+o.ID+": "+o.Description) {
					t.Fatalf("kind prompt %q lacks option %q", kindPrompt, o.ID)
				}
			}
			if !strings.Contains(kindPrompt, goal) || !strings.Contains(lightPrompt, missions.LightQuestion) || !strings.Contains(lightPrompt, goal) {
				t.Fatalf("decide prompts lack the goal as state: %q / %q", kindPrompt, lightPrompt)
			}
		})
	}
}
