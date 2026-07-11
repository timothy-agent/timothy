//go:build integration

package retrieval

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/store"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const goldenMarker = "itest-golden:"

// basis returns a 1536-dim unit vector along one dimension — a
// deterministic stand-in for real embeddings that gives the vector
// leg perfect discrimination between fixtures.
func basis(dim int) store.Vector {
	v := make(store.Vector, 1536)
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
}

// The corpus splits into three groups, each reachable primarily
// through ONE leg:
//   - vector group: queries paraphrase with no lexical overlap
//   - text group: queries share words, embeddings point elsewhere
//   - entity group: queries name an entity the content omits
var corpus = []fixture{
	{key: "v-editor", typ: store.TypeSemantic, dim: 1,
		content: goldenMarker + " The user's preferred text editing environment is Neovim."},
	{key: "v-coffee", typ: store.TypeSemantic, dim: 2,
		content: goldenMarker + " The user drinks two espressos every morning."},
	{key: "v-transport", typ: store.TypeSemantic, dim: 3,
		content: goldenMarker + " The user cycles to the office when weather allows."},
	{key: "v-music", typ: store.TypeSemantic, dim: 4,
		content: goldenMarker + " The user listens to ambient playlists while programming."},

	{key: "t-pool", typ: store.TypeProcedural, dim: 20,
		content: goldenMarker + " Postgres connection pool size stays at twenty for the homelab."},
	{key: "t-deploy", typ: store.TypeProcedural, dim: 21,
		content: goldenMarker + " Deploys run through docker compose with pinned image digests."},
	{key: "t-backup", typ: store.TypeProcedural, dim: 22,
		content: goldenMarker + " Nightly backups upload encrypted tarballs to object storage."},
	{key: "t-alerts", typ: store.TypeSemantic, dim: 23,
		content: goldenMarker + " Grafana alerting notifies through the oncall channel on Slack."},

	{key: "e-marta", typ: store.TypeSemantic, dim: 40,
		content:  goldenMarker + " Her special day falls on the third of March.",
		entities: []string{"person/Marta"}},
	{key: "e-atlas", typ: store.TypeSemantic, dim: 41,
		content:  goldenMarker + " The rewrite ships its first milestone in September 2026.",
		entities: []string{"project/Atlas"}},
	{key: "e-lisbon", typ: store.TypeEpisodic, dim: 42,
		content:  goldenMarker + " The user visited the aquarium there on 2026-05-02.",
		entities: []string{"place/Lisbon"}},
	{key: "e-vault", typ: store.TypeSemantic, dim: 43,
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
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(),
			"DELETE FROM memories WHERE content LIKE $1 || '%'", goldenMarker)
		_, _ = db.Exec(context.Background(),
			"DELETE FROM entities WHERE name IN ('Marta','Atlas','Lisbon','Vault')")
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
			EntityRefs: refs, Confidence: 0.9,
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

