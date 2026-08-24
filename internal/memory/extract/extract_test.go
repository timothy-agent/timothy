package extract

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/memory/store"
)

func TestParseFacts(t *testing.T) {
	t.Parallel()
	valid := `[{"type":"semantic","content":"User lives in Porto.","entities":[{"type":"place","name":"Porto"}],"confidence":0.9}]`
	tests := []struct {
		name    string
		raw     string
		wantN   int
		wantErr bool
	}{
		{name: "valid", raw: valid, wantN: 1},
		{name: "fenced", raw: "```json\n" + valid + "\n```", wantN: 1},
		{name: "empty array", raw: "[]", wantN: 0},
		{name: "prose", raw: "Here are the facts: " + valid, wantErr: true},
		{name: "bad fact type", raw: `[{"type":"opinion","content":"x","entities":[],"confidence":0.5}]`, wantErr: true},
		{name: "empty content", raw: `[{"type":"semantic","content":"  ","entities":[],"confidence":0.5}]`, wantErr: true},
		{name: "confidence out of range", raw: `[{"type":"semantic","content":"x","entities":[],"confidence":1.5}]`, wantErr: true},
		{name: "bad entity type", raw: `[{"type":"semantic","content":"x","entities":[{"type":"animal","name":"cat"}],"confidence":0.5}]`, wantErr: true},
		{name: "empty entity name", raw: `[{"type":"semantic","content":"x","entities":[{"type":"person","name":""}],"confidence":0.5}]`, wantErr: true},
		{name: "unknown field", raw: `[{"type":"semantic","content":"x","entities":[],"confidence":0.5,"extra":1}]`, wantErr: true},
		{name: "object not array", raw: `{"type":"semantic","content":"x"}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			facts, err := ParseFacts(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseFacts succeeded with %d facts, want error", len(facts))
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFacts: %v", err)
			}
			if len(facts) != tc.wantN {
				t.Fatalf("got %d facts, want %d", len(facts), tc.wantN)
			}
		})
	}
}

func TestParseFactsCapsCount(t *testing.T) {
	t.Parallel()
	one := `{"type":"episodic","content":"fact","entities":[],"confidence":0.9}`
	raw := "[" + strings.Repeat(one+",", 30) + one + "]"
	facts, err := ParseFacts(raw)
	if err != nil {
		t.Fatalf("ParseFacts: %v", err)
	}
	if len(facts) != maxFacts {
		t.Fatalf("got %d facts, want cap %d", len(facts), maxFacts)
	}
}

func TestAutoPromote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		f    Fact
		want bool
	}{
		{name: "confident episodic", f: Fact{Type: "episodic", Content: "Deployed v2 on 2026-07-11.", Confidence: 0.9}, want: true},
		{name: "low confidence episodic", f: Fact{Type: "episodic", Content: "Maybe deployed v2.", Confidence: 0.5}, want: false},
		{name: "semantic never auto", f: Fact{Type: "semantic", Content: "User lives in Porto.", Confidence: 0.99}, want: false},
		{name: "procedural never auto", f: Fact{Type: "procedural", Content: "Deploy with make deploy.", Confidence: 0.99}, want: false},
		{name: "credentials-adjacent episodic", f: Fact{Type: "episodic", Content: "User rotated the API key on 2026-07-11.", Confidence: 0.95}, want: false},
		{name: "preference-phrased episodic", f: Fact{Type: "episodic", Content: "User said they prefer dark mode.", Confidence: 0.95}, want: false},
		{name: "standing instruction phrasing", f: Fact{Type: "episodic", Content: "Always run tests before pushing.", Confidence: 0.95}, want: false},
		{name: "directive smuggled as episodic event", f: Fact{Type: "episodic", Content: "During the 2026-07-01 planning call the user directed that all future deploys stay verbose and the primary key stays in the homelab vault.", Confidence: 0.92}, want: false},
		{name: "must-phrased episodic", f: Fact{Type: "episodic", Content: "The user said the report must go out on Fridays.", Confidence: 0.9}, want: false},
		{name: "rule-phrased episodic", f: Fact{Type: "episodic", Content: "The team adopted a rule about commit messages on 2026-07-02.", Confidence: 0.9}, want: false},
		{name: "innocent event still promotes", f: Fact{Type: "episodic", Content: "User visited Lisbon on 2026-07-05.", Confidence: 0.9}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := AutoPromote(tc.f); got != tc.want {
				t.Fatalf("AutoPromote(%+v) = %v, want %v", tc.f, got, tc.want)
			}
		})
	}
}

// fakeGateway scripts LLM replies (consumed in order) and returns
// deterministic embeddings.
type fakeGateway struct {
	replies []string
	calls   int
	embeds  [][]float32
	routes  []string
}

func (g *fakeGateway) Stream(ctx context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	g.routes = append(g.routes, req.Route)
	ch := make(chan stream.StreamEvent, 2)
	reply := g.replies[min(g.calls, len(g.replies)-1)]
	g.calls++
	ch <- stream.StreamEvent{Type: stream.EventChunk, Text: reply}
	ch <- stream.StreamEvent{Type: stream.EventDone}
	close(ch)
	return ch, nil
}

func (g *fakeGateway) Embed(_ context.Context, texts []string, _ string) ([][]float32, string, error) {
	if g.embeds != nil {
		return g.embeds, "fake-embed", nil
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0}
	}
	return out, "fake-embed", nil
}

// fakeStore records pipeline actions.
type fakeStore struct {
	inserted  []store.Memory
	promoted  []string
	confirmed []string
	entities  map[string]string
	nearest   struct {
		id  string
		sim float64
		ok  bool
	}
	nextID int
}

func (s *fakeStore) Insert(_ context.Context, m store.Memory) (string, error) {
	s.nextID++
	id := fmt.Sprintf("mem-%d", s.nextID)
	s.inserted = append(s.inserted, m)
	return id, nil
}

func (s *fakeStore) Promote(_ context.Context, id string) error {
	s.promoted = append(s.promoted, id)
	return nil
}

func (s *fakeStore) Confirm(_ context.Context, id string) error {
	s.confirmed = append(s.confirmed, id)
	return nil
}

func (s *fakeStore) UpsertEntity(_ context.Context, typ, name string) (string, error) {
	if s.entities == nil {
		s.entities = map[string]string{}
	}
	key := typ + "/" + name
	if _, ok := s.entities[key]; !ok {
		s.entities[key] = "ent-" + key
	}
	return s.entities[key], nil
}

func (s *fakeStore) NearestActive(context.Context, store.Vector) (string, float64, bool, error) {
	return s.nearest.id, s.nearest.sim, s.nearest.ok, nil
}

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestExtractInsertsAndPromotes(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{replies: []string{`[
		{"type":"episodic","content":"Deployed v2 on 2026-07-11.","entities":[{"type":"project","name":"v2"}],"confidence":0.9},
		{"type":"semantic","content":"User lives in Porto.","entities":[{"type":"place","name":"Porto"}],"confidence":0.9}
	]`}}
	st := &fakeStore{}
	ids, err := New(gw, st, testLog()).Extract(t.Context(), Request{SessionID: "11111111-1111-1111-1111-111111111111", SourceSeq: 7, Text: "turn text"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ids) != 2 || len(st.inserted) != 2 {
		t.Fatalf("inserted %d ids %d, want 2/2", len(st.inserted), len(ids))
	}
	// Episodic ≥0.8 auto-promotes; semantic stays pending.
	if len(st.promoted) != 1 || st.promoted[0] != ids[0] {
		t.Fatalf("promoted = %v, want [%s]", st.promoted, ids[0])
	}
	first := st.inserted[0]
	if first.SourceSession != "11111111-1111-1111-1111-111111111111" || first.SourceSeq != 7 {
		t.Fatalf("provenance = %s/%d", first.SourceSession, first.SourceSeq)
	}
	if len(first.EntityRefs) != 1 || first.EntityRefs[0] != "ent-project/v2" {
		t.Fatalf("entity refs = %v", first.EntityRefs)
	}
	if len(first.Embedding) == 0 {
		t.Fatal("embedding not attached")
	}
	// The side-call must ride a route migrations actually seed —
	// "mini" never existed and every call on it failed with no_route.
	if len(gw.routes) != 1 || gw.routes[0] != "summarize" {
		t.Fatalf("routes = %v, want [summarize]", gw.routes)
	}
}

// TestExtractUsesRequestRouteOverride pins the sensitive-route
// override: a non-empty Request.Route rides the LLM call instead of
// sideRoute, so extraction honors the same floor the tool loop already
// pinned a sensitive turn/session to.
func TestExtractUsesRequestRouteOverride(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{replies: []string{`[{"type":"semantic","content":"User lives in Porto.","entities":[],"confidence":0.9}]`}}
	st := &fakeStore{}
	_, err := New(gw, st, testLog()).Extract(t.Context(), Request{Text: "x", Route: "local"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(gw.routes) != 1 || gw.routes[0] != "local" {
		t.Fatalf("routes = %v, want [local] (Request.Route overrides sideRoute)", gw.routes)
	}
}

func TestExtractDropsExactDuplicate(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{replies: []string{`[{"type":"semantic","content":"User lives in Porto.","entities":[],"confidence":0.9}]`}}
	st := &fakeStore{}
	st.nearest.id, st.nearest.sim, st.nearest.ok = "existing", 0.995, true
	ids, err := New(gw, st, testLog()).Extract(t.Context(), Request{Text: "x"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ids) != 0 || len(st.inserted) != 0 {
		t.Fatalf("exact dup inserted: ids=%v inserted=%d", ids, len(st.inserted))
	}
	// Dropping the duplicate must not drop the confirmation signal.
	if len(st.confirmed) != 1 || st.confirmed[0] != "existing" {
		t.Fatalf("confirmed = %v, want [existing]", st.confirmed)
	}
}

func TestExtractNearDuplicateReinforcesInsteadOfInserting(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{replies: []string{`[{"type":"semantic","content":"User is based in Porto, Portugal.","entities":[],"confidence":0.9}]`}}
	st := &fakeStore{}
	st.nearest.id, st.nearest.sim, st.nearest.ok = "existing", 0.96, true
	ids, err := New(gw, st, testLog()).Extract(t.Context(), Request{Text: "x"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ids) != 0 || len(st.inserted) != 0 {
		t.Fatalf("near dup inserted: ids=%v inserted=%d", ids, len(st.inserted))
	}
	if len(st.confirmed) != 1 || st.confirmed[0] != "existing" {
		t.Fatalf("confirmed = %v, want [existing]", st.confirmed)
	}
}

// A mission-source job uses the mission contract and drops facts that
// merely restate the digest's goal/title header lines.
func TestExtractMissionSourceFiltersGoalEchoes(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{replies: []string{`[` +
		`{"type":"semantic","content":"The user mandated a mission goal to create a file named redirects.md explaining 301 302 and 307 redirects in ten lines.","entities":[],"confidence":0.95},` +
		`{"type":"semantic","content":"The bKash production API rejects requests without an app key header.","entities":[],"confidence":0.9}]`}}
	st := &fakeStore{}
	digest := "mission goal: Create a file named redirects.md explaining 301, 302, and 307 redirects in ten lines\n" +
		"mission title: Understanding Redirects\nmission kind: general\n\nterminal state: done\n"
	ids, err := New(gw, st, testLog()).Extract(t.Context(), Request{Text: digest, Source: "mission"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ids) != 1 || len(st.inserted) != 1 {
		t.Fatalf("want the goal echo dropped and the real fact kept: ids=%v inserted=%d", ids, len(st.inserted))
	}
	if !strings.Contains(st.inserted[0].Content, "bKash") {
		t.Fatalf("kept the wrong fact: %q", st.inserted[0].Content)
	}
}

func TestEchoesDeny(t *testing.T) {
	t.Parallel()
	deny := []string{"create a file named redirects.md explaining 301, 302, and 307 redirects"}
	if !echoesDeny("The user mandated a mission goal to create a file named redirects.md that explains 301, 302, and 307 redirects.", deny) {
		t.Fatal("paraphrased goal echo not caught")
	}
	if echoesDeny("The bKash production API rejects requests without an app key header.", deny) {
		t.Fatal("unrelated fact wrongly dropped")
	}
}

func TestExtractRetriesOnInvalidJSON(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{replies: []string{
		"sorry, here are the facts...",
		`[{"type":"episodic","content":"Fixed the build on 2026-07-11.","entities":[],"confidence":0.9}]`,
	}}
	st := &fakeStore{}
	ids, err := New(gw, st, testLog()).Extract(t.Context(), Request{Text: "x"})
	if err != nil {
		t.Fatalf("Extract after retry: %v", err)
	}
	if gw.calls != 2 {
		t.Fatalf("llm calls = %d, want 2", gw.calls)
	}
	if len(ids) != 1 {
		t.Fatalf("ids = %v, want one", ids)
	}
}

func TestExtractGivesUpAfterTwoInvalid(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{replies: []string{"garbage", "still garbage"}}
	st := &fakeStore{}
	_, err := New(gw, st, testLog()).Extract(t.Context(), Request{Text: "x"})
	if err == nil {
		t.Fatal("Extract succeeded on garbage, want error")
	}
	if gw.calls != 2 {
		t.Fatalf("llm calls = %d, want exactly 2", gw.calls)
	}
	if len(st.inserted) != 0 {
		t.Fatal("garbage produced inserts")
	}
}

func TestExtractNoFactsIsSuccess(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{replies: []string{"[]"}}
	st := &fakeStore{}
	ids, err := New(gw, st, testLog()).Extract(t.Context(), Request{Text: "hello"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("ids = %v, want none", ids)
	}
}

// errGateway fails Stream with a terminal error event.
type errGateway struct{ fakeGateway }

func (g *errGateway) Stream(context.Context, gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	ch := make(chan stream.StreamEvent, 1)
	ch <- stream.StreamEvent{Type: stream.EventError, Err: &stream.StreamError{Message: "boom"}}
	close(ch)
	return ch, nil
}

func TestExtractSurfacesLLMError(t *testing.T) {
	t.Parallel()
	st := &fakeStore{}
	_, err := New(&errGateway{}, st, testLog()).Extract(t.Context(), Request{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want boom", err)
	}
}

// embedlessGateway fails Embed but streams facts fine.
type embedlessGateway struct{ fakeGateway }

func (g *embedlessGateway) Embed(context.Context, []string, string) ([][]float32, string, error) {
	return nil, "", fmt.Errorf("no route for task category embedding")
}

func TestExtractDegradesWithoutEmbeddings(t *testing.T) {
	t.Parallel()
	gw := &embedlessGateway{fakeGateway{replies: []string{
		`[{"type":"episodic","content":"Deployed v2 on 2026-07-11.","entities":[],"confidence":0.9}]`,
	}}}
	st := &fakeStore{}
	ids, err := New(gw, st, testLog()).Extract(t.Context(), Request{Text: "x"})
	if err != nil {
		t.Fatalf("Extract must degrade, not fail: %v", err)
	}
	if len(ids) != 1 || len(st.inserted) != 1 {
		t.Fatalf("fact not stored: ids=%v", ids)
	}
	if len(st.inserted[0].Embedding) != 0 {
		t.Fatal("phantom embedding attached")
	}
	// Promotion policy still applies in degraded mode.
	if len(st.promoted) != 1 {
		t.Fatalf("promoted = %v", st.promoted)
	}
}

// The utility gate drops facts the model itself marked as not
// behavior-changing; absent field (older reply shape) keeps the fact.
func TestExtractUtilityGateDropsExplicitFalseOnly(t *testing.T) {
	t.Parallel()
	gw := &fakeGateway{replies: []string{`[` +
		`{"type":"semantic","content":"DynamoDB local secondary indexes are defined at table creation.","entities":[],"confidence":0.9,"changes_behavior":false},` +
		`{"type":"semantic","content":"The user prefers explanations with code examples.","entities":[],"confidence":0.9,"changes_behavior":true},` +
		`{"type":"semantic","content":"The user is based in Purmerend.","entities":[],"confidence":0.9}]`}}
	st := &fakeStore{}
	ids, err := New(gw, st, testLog()).Extract(t.Context(), Request{Text: "x"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ids) != 2 || len(st.inserted) != 2 {
		t.Fatalf("want explicit-false dropped, true and absent kept: ids=%v inserted=%d", ids, len(st.inserted))
	}
	for _, m := range st.inserted {
		if strings.Contains(m.Content, "DynamoDB") {
			t.Fatalf("utility-gated fact inserted: %q", m.Content)
		}
	}
}
