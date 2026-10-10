//go:build integration

package retrieval

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/memory/store"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const goldenMarker = "itest-golden:"

// basis returns a 1024-dim unit vector along one dimension — a
// deterministic stand-in for real embeddings that gives the vector
// leg perfect discrimination between fixtures.
func basis(dim int) store.Vector {
	v := make(store.Vector, 1024)
	v[dim] = 1
	return v
}

// fixture is one corpus memory; queries below reference them by key.
type fixture struct {
	key      string
	typ      store.MemoryType
	content  string
	dim      int      // embedding basis dimension
	entities []string // "type/name" pairs
	conf     float32  // confidence, weighs into Fuse (D-147)
}

// The corpus splits into three groups, each reachable primarily
// through ONE leg:
//   - vector group: queries paraphrase with no lexical overlap
//   - text group: queries share words, embeddings point elsewhere
//   - entity group: queries name an entity the content omits
var corpus = []fixture{
	{key: "v-editor", typ: store.TypeSemantic, dim: 1, conf: 0.9,
		content: goldenMarker + " The user's preferred text editing environment is Neovim."},
	{key: "v-coffee", typ: store.TypeSemantic, dim: 2, conf: 0.9,
		content: goldenMarker + " The user drinks two espressos every morning."},
	{key: "v-transport", typ: store.TypeSemantic, dim: 3, conf: 0.9,
		content: goldenMarker + " The user cycles to the office when weather allows."},
	{key: "v-music", typ: store.TypeSemantic, dim: 4, conf: 0.9,
		content: goldenMarker + " The user listens to ambient playlists while programming."},

	{key: "t-pool", typ: store.TypeProcedural, dim: 20, conf: 0.9,
		content: goldenMarker + " Postgres connection pool size stays at twenty for the homelab."},
	{key: "t-deploy", typ: store.TypeProcedural, dim: 21, conf: 0.9,
		content: goldenMarker + " Deploys run through docker compose with pinned image digests."},
	{key: "t-backup", typ: store.TypeProcedural, dim: 22, conf: 0.9,
		content: goldenMarker + " Nightly backups upload encrypted tarballs to object storage."},
	{key: "t-alerts", typ: store.TypeSemantic, dim: 23, conf: 0.9,
		content: goldenMarker + " Grafana alerting notifies through the oncall channel on Slack."},

	{key: "e-marta", typ: store.TypeSemantic, dim: 40, conf: 0.9,
		content:  goldenMarker + " Her special day falls on the third of March.",
		entities: []string{"person/Marta"}},
	{key: "e-atlas", typ: store.TypeSemantic, dim: 41, conf: 0.9,
		content:  goldenMarker + " The rewrite ships its first milestone in September 2026.",
		entities: []string{"project/Atlas"}},
	{key: "e-lisbon", typ: store.TypeEpisodic, dim: 42, conf: 0.9,
		content:  goldenMarker + " The user visited the aquarium there on 2026-05-02.",
		entities: []string{"place/Lisbon"}},
	{key: "e-vault", typ: store.TypeSemantic, dim: 43, conf: 0.9,
		content:  goldenMarker + " Secrets rotate quarterly through the homelab secret manager.",
		entities: []string{"service/Vault"}},
}

// golden queries: text is what the user asks; dim crafts the query
// embedding (pointing at the target for vector-group queries, at an
// unused dimension otherwise); want is the fixture key expected in
// the top-5.
type golden struct {
	text string
	dim  int
	want string
}

var goldens = []golden{
	// vector group — no lexical overlap with content
	{text: "which program does the user write code in?", dim: 1, want: "v-editor"},
	{text: "how much caffeine does the user consume?", dim: 2, want: "v-coffee"},
	{text: "how does the user commute?", dim: 3, want: "v-transport"},
	{text: "what does the user play in the background while coding?", dim: 4, want: "v-music"},
	{text: "what development setup does the user code with?", dim: 1, want: "v-editor"},
	{text: "morning drink habits?", dim: 2, want: "v-coffee"},
	{text: "getting to work without a car?", dim: 3, want: "v-transport"},

	// text group — shared words, embedding points nowhere useful
	{text: "postgres connection pool size", dim: 900, want: "t-pool"},
	{text: "how do deploys run with docker compose?", dim: 901, want: "t-deploy"},
	{text: "nightly backups to object storage", dim: 902, want: "t-backup"},
	{text: "grafana alerting oncall", dim: 903, want: "t-alerts"},
	{text: "pinned image digests deploys", dim: 904, want: "t-deploy"},
	{text: "encrypted tarballs backups", dim: 905, want: "t-backup"},
	{text: "connection pool homelab postgres", dim: 906, want: "t-pool"},

	// multi-topic questions: AND-semantics would return nothing; each
	// fact answering PART of the question must still surface.
	{text: "what is the postgres pool size and where do backups upload?", dim: 907, want: "t-pool"},
	{text: "what is the postgres pool size and where do backups upload?", dim: 908, want: "t-backup"},

	// entity group — the name appears only via entity_refs
	{text: "when is Marta's birthday?", dim: 910, want: "e-marta"},
	{text: "what is the Atlas milestone date?", dim: 911, want: "e-atlas"},
	{text: "what did the user do in Lisbon in May?", dim: 912, want: "e-lisbon"},
	{text: "how does Vault rotation work?", dim: 913, want: "e-vault"},
	{text: "anything about Marta?", dim: 914, want: "e-marta"},
	{text: "Atlas progress?", dim: 915, want: "e-atlas"},
}

func seedGolden(t *testing.T) (*Searcher, map[string]string) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Sweep at setup AND teardown. The teardown runs after t.Context()
	// is canceled and the pool may already be closed, so it uses an
	// independent connection.
	sweepGolden := func(ctx context.Context, db interface {
		Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	}) {
		_, _ = db.Exec(ctx, "DELETE FROM memories WHERE content LIKE $1 || '%'", goldenMarker)
		_, _ = db.Exec(ctx, "DELETE FROM entities WHERE name IN ('Marta','Atlas','Lisbon','Vault')")
	}
	sweepGolden(ctx, db)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		sweepGolden(cctx, conn)
	})

	st := store.New(pool, log)
	idByKey := map[string]string{}
	for _, f := range corpus {
		var refs []string
		for _, pair := range f.entities {
			parts := strings.SplitN(pair, "/", 2)
			id, err := st.UpsertEntity(ctx, parts[0], parts[1])
			if err != nil {
				t.Fatalf("UpsertEntity %s: %v", pair, err)
			}
			refs = append(refs, id)
		}
		id, err := st.Insert(ctx, store.Memory{
			Type: f.typ, Content: f.content, Embedding: basis(f.dim),
			EntityRefs: refs, Confidence: f.conf,
		})
		if err != nil {
			t.Fatalf("Insert %s: %v", f.key, err)
		}
		if err := st.Promote(ctx, id); err != nil {
			t.Fatalf("Promote %s: %v", f.key, err)
		}
		idByKey[f.key] = id
	}
	return NewSearcher(pool, log), idByKey
}

// recallAt5 runs every golden query through search+fuse and returns
// the fraction whose expected memory lands in the top five.
// dropLeg removes one leg's contribution before fusion ("" = none).
func recallAt5(t *testing.T, s *Searcher, ids map[string]string, dropLeg string) float64 {
	t.Helper()
	hitCount := 0
	for _, q := range goldens {
		emb := basis(q.dim)
		if dropLeg == "vector" {
			emb = nil
		}
		cands, err := s.Search(t.Context(), q.text, emb, nil)
		if err != nil {
			t.Fatalf("Search %q: %v", q.text, err)
		}
		if dropLeg != "" && dropLeg != "vector" {
			for id, c := range cands {
				delete(c.ranks, dropLeg)
				if len(c.ranks) == 0 {
					delete(cands, id)
				}
			}
		}
		fused := Fuse(cands, time.Now())
		top := fused[:min(5, len(fused))]
		for _, sc := range top {
			if sc.ID == ids[q.want] {
				hitCount++
				break
			}
		}
	}
	return float64(hitCount) / float64(len(goldens))
}

func TestGoldenRecallAt5(t *testing.T) {
	s, ids := seedGolden(t)

	recall := recallAt5(t, s, ids, "")
	if recall < 0.8 {
		t.Fatalf("recall@5 = %.2f, want >= 0.8", recall)
	}

	// Every leg must contribute: disabling one drops recall.
	for _, leg := range []string{"vector", "text", "entity"} {
		partial := recallAt5(t, s, ids, leg)
		if partial >= recall {
			t.Fatalf("dropping %s leg did not hurt recall (%.2f -> %.2f); leg contributes nothing",
				leg, recall, partial)
		}
		t.Logf("recall@5 without %s leg: %.2f (full: %.2f)", leg, partial, recall)
	}
}

func TestMarkRetrievedStampsOnlyRetrievalTime(t *testing.T) {
	s, ids := seedGolden(t)
	ctx := t.Context()

	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	target := ids["v-editor"]
	var confirmedBefore time.Time
	if err := db.QueryRow(ctx,
		"SELECT last_confirmed_at FROM memories WHERE id = $1", target).Scan(&confirmedBefore); err != nil {
		t.Fatalf("read: %v", err)
	}

	s.MarkRetrieved(ctx, []string{target})

	var confirmedAfter time.Time
	var retrieved *time.Time
	if err := db.QueryRow(ctx,
		"SELECT last_confirmed_at, last_retrieved_at FROM memories WHERE id = $1", target).
		Scan(&confirmedAfter, &retrieved); err != nil {
		t.Fatalf("read: %v", err)
	}
	if retrieved == nil {
		t.Fatal("last_retrieved_at not stamped")
	}
	if !confirmedAfter.Equal(confirmedBefore) {
		t.Fatal("retrieval bumped last_confirmed_at; only confirmation may (D-011)")
	}
}

func TestSearchSurvivesDegenerateQueries(t *testing.T) {
	s, _ := seedGolden(t)
	// Queries that produce zero lexemes (punctuation, stopwords) or
	// carry quote characters must degrade to empty/partial results,
	// never to an error — partial recall beats none.
	for _, q := range []string{
		"?!,.;:—…",
		"the and of a is",
		`it's "quoted" 'weird' O''Brien`,
		"   ",
	} {
		if _, err := s.Search(t.Context(), q, nil, nil); err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
	}
}

func TestSearchErrorsOnlyWhenEveryLegFails(t *testing.T) {
	s, _ := seedGolden(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // every leg's query now fails
	if _, err := s.Search(ctx, "anything at all", nil, nil); err == nil {
		t.Fatal("want error when all legs fail")
	}
}

// insertActive stores one active golden-marked memory and returns its id.
func insertActive(t *testing.T, s *Searcher, content string, dim int) string {
	t.Helper()
	st := store.New(s.db, s.log)
	id, err := st.Insert(t.Context(), store.Memory{
		Type: store.TypeSemantic, Content: goldenMarker + " " + content,
		Embedding: basis(dim), Confidence: 0.9, Actor: store.ActorUser,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return id
}

func rankOf(scored []Scored, id string) int {
	for i, sc := range scored {
		if sc.ID == id {
			return i
		}
	}
	return -1
}

// tilted returns a unit vector at cosine sim to basis(dim).
func tilted(dim int, sim float64) store.Vector {
	v := make(store.Vector, 1024)
	v[dim] = float32(sim)
	v[dim+1] = float32(math.Sqrt(1 - sim*sim))
	return v
}

// TestRetrievalBumpKeepsDurableFact: a 200-day-old fact found by one
// leg is dropped as stale, and one retrieval bump brings it back
// (D-148).
func TestRetrievalBumpKeepsDurableFact(t *testing.T) {
	s, _ := seedGolden(t)
	ctx := t.Context()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	id := insertActive(t, s, "The user's passport renewal office is in Haarlem.", 320)
	if _, err := db.Exec(ctx, `UPDATE memories SET last_confirmed_at = now() - interval '200 days'
		WHERE id = $1`, id); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	returned := func() bool {
		cands, err := s.Search(ctx, "zzqx", basis(320), nil)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		return rankOf(Fuse(cands, time.Now()), id) >= 0
	}
	if returned() {
		t.Fatal("200-day-old single-leg fact survived without a bump; the fixture no longer shows the gap")
	}
	s.MarkRetrieved(ctx, []string{id})
	if !returned() {
		t.Fatal("200-day-old fact still dropped after a retrieval bump")
	}
}

// TestVectorLegCosineFloor: hits under the D-149 floor are dropped,
// so an off-topic turn can come back empty.
func TestVectorLegCosineFloor(t *testing.T) {
	s, _ := seedGolden(t)
	ctx := t.Context()
	id := insertActive(t, s, "The user keeps bees on the allotment.", 330)
	for _, tc := range []struct {
		sim  float64
		want bool
	}{{0.2, false}, {0.3, true}, {1, true}} {
		cands, err := s.Search(ctx, "zzqx", tilted(330, tc.sim), nil)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		_, got := cands[id]
		if got != tc.want {
			t.Fatalf("cosine %.2f: returned = %v, want %v", tc.sim, got, tc.want)
		}
	}

	// Off-topic: an embedding near nothing in the corpus and no shared
	// words or entities yields an empty block.
	cands, err := s.Search(ctx, "what is 2+2", basis(999), nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	packed, _, err := Pack(Fuse(cands, time.Now()), 0, 0)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if len(packed) != 0 {
		t.Fatalf("off-topic query packed %d memories, want none", len(packed))
	}
}

// entity creates (or reuses) one entity; only ones it created are
// deleted at cleanup, so a real entity of the same name survives.
func entity(t *testing.T, s *Searcher, typ, name string) string {
	t.Helper()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var id string
	err = db.QueryRow(t.Context(), `INSERT INTO entities (type, name) VALUES ($1, $2)
		ON CONFLICT (type, name) DO NOTHING RETURNING id`, typ, name).Scan(&id)
	if err == nil {
		t.Cleanup(func() {
			conn, err := pgx.Connect(context.Background(), os.Getenv("DATABASE_URL"))
			if err != nil {
				return
			}
			defer func() { _ = conn.Close(context.Background()) }()
			_, _ = conn.Exec(context.Background(), "DELETE FROM entities WHERE id = $1", id)
		})
		return id
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("insert entity: %v", err)
	}
	if err := db.QueryRow(t.Context(), `SELECT id FROM entities WHERE type = $1 AND name = $2`,
		typ, name).Scan(&id); err != nil {
		t.Fatalf("read entity: %v", err)
	}
	return id
}

// entityRanks runs only the entity leg and returns rank by memory id.
func entityRanks(t *testing.T, s *Searcher, query string) map[string]int {
	t.Helper()
	m := &merger{into: map[string]*Candidate{}}
	if err := s.leg(t.Context(), "entity", entitySQL, []any{query}, nil, m); err != nil {
		t.Fatalf("entity leg: %v", err)
	}
	out := map[string]int{}
	for id, c := range m.into {
		out[id] = c.ranks["entity"]
	}
	return out
}

// TestEntityLegWordBoundary: names under 4 characters need a word
// boundary, longer names still match inside words (D-149).
func TestEntityLegWordBoundary(t *testing.T) {
	s, _ := seedGolden(t)
	ctx := t.Context()
	st := store.New(s.db, s.log)
	add := func(content string, refs ...string) string {
		id, err := st.Insert(ctx, store.Memory{Type: store.TypeSemantic, Content: goldenMarker + " " + content,
			Confidence: 0.9, Actor: store.ActorUser, EntityRefs: refs})
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
		return id
	}
	goMem := add("Services are written in one compiled language.", entity(t, s, "topic", "Go"))
	cppMem := add("The firmware uses one systems language.", entity(t, s, "topic", "C++"))
	amsMem := add("The user lives near the canals.", entity(t, s, "place", "Amsterdam"))

	for _, tc := range []struct {
		query string
		id    string
		want  bool
	}{
		{"is this a good idea?", goMem, false},
		{"Gopher stickers", goMem, false},
		{"I write Go every day", goMem, true},
		{"go", goMem, true},
		{"Go's tooling", goMem, true},
		{"(Go)", goMem, true},
		{"is C++ fast?", cppMem, true},
		{"is C fast?", cppMem, false},
		{"weekend in Amsterdam", amsMem, true},
		{"Amsterdam's canals", amsMem, true},
		{"amsterdammers", amsMem, true},
		{"Rotterdam", amsMem, false},
	} {
		_, got := entityRanks(t, s, tc.query)[tc.id]
		if got != tc.want {
			t.Errorf("%q: matched = %v, want %v", tc.query, got, tc.want)
		}
	}
}

// TestEntityLegOrdersByMatchQuality: more matched entities first, then
// the longer matched name, and recency never outranks either (D-149).
func TestEntityLegOrdersByMatchQuality(t *testing.T) {
	s, _ := seedGolden(t)
	ctx := t.Context()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	short := entity(t, s, "project", "Zephyrine")
	long := entity(t, s, "project", "Zephyrine Rewrite")
	st := store.New(s.db, s.log)
	add := func(content string, ageDays int, refs ...string) string {
		id, err := st.Insert(ctx, store.Memory{Type: store.TypeSemantic, Content: goldenMarker + " " + content,
			Confidence: 0.9, Actor: store.ActorUser, EntityRefs: refs})
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
		if _, err := db.Exec(ctx, `UPDATE memories SET last_confirmed_at = now() - make_interval(days => $2)
			WHERE id = $1`, id, ageDays); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		return id
	}
	newest := add("Kickoff happened last week.", 0, short)
	specific := add("The rewrite targets the new storage layer.", 20, long)
	both := add("The rewrite replaces the old project core.", 40, short, long)

	ranks := entityRanks(t, s, "how is the Zephyrine Rewrite going?")
	if ranks[both] != 1 || ranks[specific] != 2 || ranks[newest] != 3 {
		t.Fatalf("ranks both=%d specific=%d newest=%d, want 1/2/3", ranks[both], ranks[specific], ranks[newest])
	}
}

// TestDecayPassRanksDecayedDuplicateLower runs a real decay pass and
// retrieves: of two duplicates with equal recency, the decayed one
// ranks below the other (D-147).
func TestDecayPassRanksDecayedDuplicateLower(t *testing.T) {
	s, _ := seedGolden(t)
	ctx := t.Context()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	const content = "The user's spare bicycle is a green gravel frame."
	decayed := insertActive(t, s, content, 300)
	kept := insertActive(t, s, content, 300)
	if _, err := db.Exec(ctx, `UPDATE memories SET last_confirmed_at = now() - interval '10000 days'
		WHERE id = $1`, decayed); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	now := time.Now()
	ids, err := store.New(s.db, s.log).DecayStaleSemantic(ctx,
		now.Add(-365*24*time.Hour), now.Add(-30*24*time.Hour), 0.8, 10)
	if err != nil {
		t.Fatalf("DecayStaleSemantic: %v", err)
	}
	if !slices.Contains(ids, decayed) || slices.Contains(ids, kept) {
		t.Fatalf("decay pass = %v, want the stale duplicate only", ids)
	}
	// Equal recency again, so only confidence separates them.
	if _, err := db.Exec(ctx, `UPDATE memories SET last_confirmed_at =
		(SELECT last_confirmed_at FROM memories WHERE id = $2) WHERE id = $1`, decayed, kept); err != nil {
		t.Fatalf("align recency: %v", err)
	}

	cands, err := s.Search(ctx, "spare bicycle gravel frame", basis(300), nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	fused := Fuse(cands, time.Now())
	ki, di := rankOf(fused, kept), rankOf(fused, decayed)
	if ki < 0 || di < 0 || ki > di {
		t.Fatalf("kept at %d, decayed at %d: want the decayed duplicate below", ki, di)
	}
}

// TestDecayedBelowFloorLeavesRetrievalUntilConfirmed proves D-147's
// floor: a decayed row under it is excluded from every leg, and a
// reconfirmation brings it back.
func TestDecayedBelowFloorLeavesRetrievalUntilConfirmed(t *testing.T) {
	s, _ := seedGolden(t)
	ctx := t.Context()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	id := insertActive(t, s, "The user's rowing club meets at the Amstel boathouse.", 301)
	// Low confidence alone (never decayed) stays retrievable.
	if _, err := db.Exec(ctx, `UPDATE memories SET confidence = 0.1 WHERE id = $1`, id); err != nil {
		t.Fatalf("seed: %v", err)
	}
	found := func() bool {
		cands, err := s.Search(ctx, "rowing club boathouse", basis(301), nil)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		_, ok := cands[id]
		return ok
	}
	if !found() {
		t.Fatal("undecayed low-confidence row excluded; the floor applies to decayed rows only")
	}
	if _, err := db.Exec(ctx, `UPDATE memories SET decayed_at = now(), confidence = 0.15 WHERE id = $1`, id); err != nil {
		t.Fatalf("seed decayed: %v", err)
	}
	if found() {
		t.Fatal("decayed row below the floor still retrieved")
	}
	if err := store.New(s.db, s.log).Confirm(ctx, id, 0.9); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !found() {
		t.Fatal("reconfirmed row not retrieved")
	}
}
