package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/platform/netguard"
)

// probeHintTools covers each readOnlyHint shape plus the reserved
// load_tool name.
const probeHintTools = `[{"name":"read_doc","description":"Read","annotations":{"readOnlyHint":true}},` +
	`{"name":"write_doc","description":"Write","annotations":{"readOnlyHint":false}},` +
	`{"name":"plain","description":"No hint"},{"name":"load_tool","description":"mimic"}]`

func TestProbeMCPOutcomes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		handler    http.Handler
		closed     bool
		token      string
		wantStatus string
		wantMsg    string
	}{
		{name: "ok", handler: (&fakeMCP{toolsJSON: probeHintTools}).handler(), wantStatus: ProbeOK},
		{name: "ok with token", handler: (&fakeMCP{token: "tok-1"}).handler(), token: "tok-1", wantStatus: ProbeOK},
		{name: "401 without metadata", handler: (&fakeMCP{token: "tok-1"}).handler(), wantStatus: ProbeNeedsToken},
		{name: "401 with wrong token", handler: (&fakeMCP{token: "tok-1"}).handler(), token: "nope", wantStatus: ProbeNeedsToken},
		{
			name: "401 with resource_metadata",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`)
				w.WriteHeader(http.StatusUnauthorized)
			}),
			wantStatus: ProbeNeedsOAuth,
		},
		{
			name: "non-JSON body",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<html>login</html>"))
			}),
			wantStatus: ProbeError,
			wantMsg:    "decode",
		},
		{
			name: "404",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.NotFound(w, nil)
			}),
			wantStatus: ProbeError,
			wantMsg:    "status 404",
		},
		{name: "network error", handler: http.NotFoundHandler(), closed: true, wantStatus: ProbeUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(tc.handler)
			if tc.closed {
				srv.Close()
			} else {
				t.Cleanup(srv.Close)
			}
			got := ProbeMCP(t.Context(), srv.Client(), srv.URL, nil, tc.token)
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q (%s), want %q", got.Status, got.Message, tc.wantStatus)
			}
			if !strings.Contains(got.Message, tc.wantMsg) {
				t.Fatalf("message = %q, want it to contain %q", got.Message, tc.wantMsg)
			}
			if got.Tools == nil {
				t.Fatal("tools is nil, want an empty list for JSON")
			}
		})
	}
}

func TestProbeMCPToolsAndHints(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&fakeMCP{toolsJSON: probeHintTools}).handler())
	t.Cleanup(srv.Close)

	got := ProbeMCP(t.Context(), srv.Client(), srv.URL, nil, "")
	if got.Server.Name != "fake" {
		t.Fatalf("server = %+v", got.Server)
	}
	if got.ToolCount != 3 || len(got.Tools) != 3 {
		t.Fatalf("count = %d, tools = %+v, want load_tool dropped", got.ToolCount, got.Tools)
	}
	hint := func(i int) string {
		if got.Tools[i].ReadOnlyHint == nil {
			return "nil"
		}
		return fmt.Sprint(*got.Tools[i].ReadOnlyHint)
	}
	if hint(0) != "true" || hint(1) != "false" || hint(2) != "nil" {
		t.Fatalf("hints = %s %s %s, want true false nil", hint(0), hint(1), hint(2))
	}
	if string(got.Tools[2].InputSchema) != `{"type":"object"}` {
		t.Fatalf("default schema = %s", got.Tools[2].InputSchema)
	}
}

func TestProbeMCPTruncatesAndCaps(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", ProbeDescriptionMax+50)
	var b strings.Builder
	b.WriteString(`[{"name":"t0","description":"` + long + `"}`)
	for i := 1; i <= ProbeToolCap; i++ {
		fmt.Fprintf(&b, `,{"name":"t%d"}`, i)
	}
	b.WriteString("]")
	srv := httptest.NewServer((&fakeMCP{toolsJSON: b.String()}).handler())
	t.Cleanup(srv.Close)

	got := ProbeMCP(t.Context(), srv.Client(), srv.URL, nil, "")
	if got.ToolCount != ProbeToolCap+1 || len(got.Tools) != ProbeToolCap {
		t.Fatalf("count = %d, returned = %d, want %d and %d", got.ToolCount, len(got.Tools), ProbeToolCap+1, ProbeToolCap)
	}
	if n := utf8.RuneCountInString(got.Tools[0].Description); n != ProbeDescriptionMax+1 {
		t.Fatalf("description runes = %d, want %d plus the ellipsis", n, ProbeDescriptionMax)
	}
	if !strings.HasSuffix(got.Tools[0].Description, "…") {
		t.Fatalf("description = %q, want an ellipsis", got.Tools[0].Description)
	}
}

func TestProbeMCPRedactsTokenFromMessage(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, `{"message":"token %s lacks scope"}`, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) //nolint:gosec // G705: a fake server echoing the token is the case under test
	}))
	t.Cleanup(srv.Close)

	got := ProbeMCP(t.Context(), srv.Client(), srv.URL, nil, "tok-secret-9")
	if got.Status != ProbeError || strings.Contains(got.Message, "tok-secret-9") || !strings.Contains(got.Message, "[redacted]") {
		t.Fatalf("result = %+v, want the echoed token redacted", got)
	}
}

func TestProbeMCPThroughNetguardBlocksPrivate(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: netguard.Guard{}.Transport()}
	got := ProbeMCP(t.Context(), client, "http://127.0.0.1:1/mcp", nil, "")
	if got.Status != ProbeUnreachable || !strings.Contains(got.Message, "outbound host allowlist") {
		t.Fatalf("result = %+v, want unreachable with the allowlist hint", got)
	}
}

func TestProbeFinalNames(t *testing.T) {
	t.Parallel()
	schemaA := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	schemaB := json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}}}`)
	other := &mcpSource{name: "other", toolList: []*tools.Tool{
		{Name: "search", InputSchema: schemaA},
		{Name: "fetch", InputSchema: schemaA},
	}}
	cases := []struct {
		name      string
		probed    []ProbeTool
		total     int // server count when above len(probed): a capped preview
		surface   []string
		threshold int
		want      map[string]string
	}{
		{
			name:    "lone tool keeps raw name",
			probed:  []ProbeTool{{Name: "create_page", InputSchema: schemaA}},
			surface: []string{"shell", "search", "fetch"},
			want:    map[string]string{"create_page": "create_page"},
		},
		{
			name:    "builtin name is namespaced",
			probed:  []ProbeTool{{Name: "shell", InputSchema: schemaA}},
			surface: []string{"shell", "search", "fetch"},
			want:    map[string]string{"shell": "notion_shell"},
		},
		{
			name:    "same name same schema merges",
			probed:  []ProbeTool{{Name: "search", InputSchema: schemaA}},
			surface: []string{"shell", "search", "fetch"},
			want:    map[string]string{"search": "search"},
		},
		{
			name:    "same name other schema splits",
			probed:  []ProbeTool{{Name: "fetch", InputSchema: schemaB}},
			surface: []string{"shell", "search", "fetch"},
			want:    map[string]string{"fetch": "notion_fetch"},
		},
		{
			name:      "over threshold all namespaced",
			probed:    []ProbeTool{{Name: "a", InputSchema: schemaA}, {Name: "b", InputSchema: schemaA}},
			surface:   []string{"shell"},
			threshold: 1,
			want:      map[string]string{"a": "notion_a", "b": "notion_b"},
		},
		{
			name:      "capped preview of an indexed server",
			probed:    []ProbeTool{{Name: "a", InputSchema: schemaA}},
			total:     600,
			surface:   []string{"shell"},
			threshold: 8,
			want:      map[string]string{"a": "notion_a"},
		},
		{
			name:      "threshold zero keeps eager",
			probed:    []ProbeTool{{Name: "a", InputSchema: schemaA}, {Name: "b", InputSchema: schemaA}},
			surface:   []string{"shell"},
			threshold: 0,
			want:      map[string]string{"a": "a", "b": "b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := testManager(fakeRows{})
			m.sources["other"] = other
			res := ProbeResult{Tools: tc.probed, ToolCount: max(tc.total, len(tc.probed))}
			got := m.ProbeFinalNames("notion", res, tc.surface, tc.threshold)
			for raw, want := range tc.want {
				if got[raw] != want {
					t.Fatalf("final name of %q = %q, want %q (all: %v)", raw, got[raw], want, got)
				}
			}
			if _, ok := m.sources["notion"]; ok {
				t.Fatal("probe leaked the candidate into live sources")
			}
		})
	}
}

func TestReservedFromSurface(t *testing.T) {
	t.Parallel()
	sources := map[string]Source{"gh": &mcpSource{name: "gh", toolList: []*tools.Tool{{Name: "create_issue", InputSchema: json.RawMessage(`{}`)}}}}
	cases := []struct {
		name    string
		surface []string
		want    []string
		notWant []string
	}{
		{name: "connector raw name is not reserved", surface: []string{"shell", "create_issue"}, want: []string{"shell"}, notWant: []string{"create_issue"}},
		{name: "builtin sharing a connector name is reserved", surface: []string{"create_issue", "gh_create_issue"}, want: []string{"create_issue"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := reservedFromSurface(sources, tc.surface)
			for _, n := range tc.want {
				if !got[n] {
					t.Fatalf("reserved = %v, want %q in it", got, n)
				}
			}
			for _, n := range tc.notWant {
				if got[n] {
					t.Fatalf("reserved = %v, want %q out of it", got, n)
				}
			}
		})
	}
}

func TestProbeStoredBearer(t *testing.T) {
	t.Parallel()
	fake := &fakeMCP{token: "tok-1", toolsJSON: probeHintTools}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	m := testManager(fakeRows{})
	m.resolve = func(_ context.Context, ref string) (string, error) {
		if ref == "NOTION_MCP_TOKEN" {
			return "tok-1", nil
		}
		return "", fmt.Errorf("no secret %s", ref)
	}
	row := func(ref string) Connector {
		return Connector{ID: "m1", Name: "notion", Kind: "mcp", Config: json.RawMessage(`{"endpoint":"` + srv.URL + `","headers":{"X-Team":"core"}}`), CredentialRef: ref}
	}

	res := m.ProbeStored(t.Context(), srv.Client(), row("NOTION_MCP_TOKEN"), nil)
	if res.Status != ProbeOK || res.ToolCount != 3 {
		t.Fatalf("stored token: %+v", res)
	}
	if fake.gotAuth != "Bearer tok-1" {
		t.Fatalf("auth = %q", fake.gotAuth)
	}
	if res := m.ProbeStored(t.Context(), srv.Client(), row("MISSING"), nil); res.Status != ProbeNeedsToken {
		t.Fatalf("missing secret: %+v", res)
	}
	bad := Connector{ID: "m1", Name: "notion", Kind: "mcp", Config: json.RawMessage(`{"endpoint":"` + srv.URL + `","auth_mode":"basic"}`)}
	if res := m.ProbeStored(t.Context(), srv.Client(), bad, nil); res.Status != ProbeError || !strings.Contains(res.Message, "auth_mode") {
		t.Fatalf("bad auth mode: %+v", res)
	}
}

func TestProbeStoredOAuth(t *testing.T) {
	t.Parallel()
	as, _, client := newOAuthFakes(t, true)
	auth, rows, _ := testMCPAuth(t, client, oauthRow(as.base+"/mcp", ""))
	m := testManager(rows)

	before, err := rows.Get(t.Context(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if res := m.ProbeStored(t.Context(), client, before, auth); res.Status != ProbeNeedsOAuth {
		t.Fatalf("before connect: %+v", res)
	}
	if res := m.ProbeStored(t.Context(), client, before, nil); res.Status != ProbeNeedsOAuth {
		t.Fatalf("no auth engine: %+v", res)
	}

	connectMCP(t, auth)
	connected, err := rows.Get(t.Context(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	res := m.ProbeStored(t.Context(), client, connected, auth)
	if res.Status != ProbeOK || res.ToolCount == 0 {
		t.Fatalf("connected: %+v", res)
	}

	// The server revokes the live access token: one forced refresh,
	// then the probe succeeds on the new one.
	as.revoke(lastIssued(as))
	if res := m.ProbeStored(t.Context(), client, connected, auth); res.Status != ProbeOK {
		t.Fatalf("after revoke: %+v", res)
	}
	if as.grants("refresh_token") != 1 {
		t.Fatalf("refresh grants = %d, want 1", as.grants("refresh_token"))
	}

	// Refresh refused: the page must ask to reconnect, and the message
	// never carries the token the server rejected.
	as.mu.Lock()
	as.refreshStatus = http.StatusBadRequest
	as.mu.Unlock()
	as.revoke(lastIssued(as))
	res = m.ProbeStored(t.Context(), client, connected, auth)
	if res.Status != ProbeNeedsOAuth || !strings.Contains(res.Message, "reconnect") {
		t.Fatalf("refresh refused: %+v", res)
	}
}

// lastIssued is the access token the fake authorization server
// handed out most recently.
func lastIssued(as *fakeAS) string {
	as.mu.Lock()
	defer as.mu.Unlock()
	return fmt.Sprintf("at-%d", as.issued)
}

func TestDiffProbe(t *testing.T) {
	t.Parallel()
	at := time.Now()
	stored := func(toolCount int, names ...string) MCPLastProbe {
		rec := MCPLastProbe{At: at, ToolCount: toolCount}
		for _, n := range names {
			rec.Tools = append(rec.Tools, MCPProbedTool{Name: n})
		}
		return rec
	}
	probed := func(toolCount int, names ...string) ProbeResult {
		res := ProbeResult{Status: ProbeOK, ToolCount: toolCount}
		for _, n := range names {
			res.Tools = append(res.Tools, ProbeTool{Name: n})
		}
		return res
	}
	cases := []struct {
		name        string
		prev        MCPLastProbe
		next        ProbeResult
		wantAdded   []string
		wantRemoved []string
	}{
		{name: "no record yet", prev: MCPLastProbe{}, next: probed(2, "a", "b")},
		{name: "unchanged", prev: stored(2, "a", "b"), next: probed(2, "a", "b")},
		{name: "added and removed", prev: stored(2, "a", "b"), next: probed(2, "b", "c"), wantAdded: []string{"c"}, wantRemoved: []string{"a"}},
		{name: "capped previous preview", prev: stored(600, "a"), next: probed(2, "a", "b")},
		{name: "capped next preview", prev: stored(2, "a", "b"), next: probed(600, "a")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			added, removed := DiffProbe(tc.prev, tc.next)
			if fmt.Sprint(added) != fmt.Sprint(tc.wantAdded) || fmt.Sprint(removed) != fmt.Sprint(tc.wantRemoved) {
				t.Fatalf("diff = %v/%v, want %v/%v", added, removed, tc.wantAdded, tc.wantRemoved)
			}
		})
	}
}

func TestLastProbeRoundTrip(t *testing.T) {
	t.Parallel()
	if got := LastProbeOf(Connector{Config: json.RawMessage(`{"endpoint":"https://x"}`)}); !got.At.IsZero() || len(got.Tools) != 0 {
		t.Fatalf("absent = %+v", got)
	}
	if got := LastProbeOf(Connector{Config: json.RawMessage(`nope`)}); !got.At.IsZero() {
		t.Fatalf("malformed = %+v", got)
	}
	ro := true
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	rec := ProbeResult{ToolCount: 1, Tools: []ProbeTool{{Name: "search", FinalName: "notion_search", Description: "dropped", ReadOnlyHint: &ro, InputSchema: json.RawMessage(`{}`)}}}.Record(at)
	raw, err := json.Marshal(map[string]any{"endpoint": "https://x", "last_probe": rec})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "dropped") || strings.Contains(string(raw), "input_schema") {
		t.Fatalf("record carries more than the table needs: %s", raw)
	}
	got := LastProbeOf(Connector{Config: raw})
	if !got.At.Equal(at) || got.ToolCount != 1 || len(got.Tools) != 1 || got.Tools[0].FinalName != "notion_search" || got.Tools[0].ReadOnlyHint == nil || !*got.Tools[0].ReadOnlyHint {
		t.Fatalf("round trip = %+v", got)
	}
}
