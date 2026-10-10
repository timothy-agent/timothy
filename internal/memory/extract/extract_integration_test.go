//go:build integration

package extract

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/memory/store"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const itestMarker = "itest-extract:"

type itestExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func sweepExtractFixtures(ctx context.Context, db itestExecer) {
	_, _ = db.Exec(ctx, `DELETE FROM memories WHERE content LIKE $1 || '%'`, itestMarker)
	_, _ = db.Exec(ctx, `DELETE FROM entities WHERE name LIKE 'itest-extract-%'`)
}

// itestStore returns a real store plus its pool for assertions. Rows
// whose content starts with itestMarker and entities named
// itest-extract-* are swept at setup and teardown.
func itestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
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
	sweepExtractFixtures(ctx, db)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		sweepExtractFixtures(cctx, conn)
	})
	return store.New(pool, log), db
}

// dimGateway scripts one LLM reply and returns 1024-dim one-hot
// embeddings (the memories.embedding width), one axis per text so a
// batch never self-dedups.
type dimGateway struct {
	reply string
	axis  int
}

func (g *dimGateway) Stream(context.Context, gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	ch := make(chan stream.StreamEvent, 2)
	ch <- stream.StreamEvent{Type: stream.EventChunk, Text: g.reply}
	ch <- stream.StreamEvent{Type: stream.EventDone}
	close(ch)
	return ch, nil
}

func (g *dimGateway) Embed(_ context.Context, texts []string, _ string) ([][]float32, string, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, 1024)
		v[(g.axis+i)%1024] = 1
		out[i] = v
	}
	return out, "fake-embed", nil
}

// Integration (#872): the reflection pass against the real store lands
// its insight with source_session NULL and the reflection actor; before
// the fix the uuid cast rejected the "reflection" session id.
func TestReflectionInsertsIntoRealStore(t *testing.T) {
	st, db := itestStore(t)
	ctx := t.Context()
	content := fmt.Sprintf("%s reflection insight %d", itestMarker, time.Now().UnixNano())

	episodics := make([]store.Memory, reflectMinEpisodics)
	for i := range episodics {
		episodics[i] = store.Memory{ID: fmt.Sprintf("e%d", i), Type: store.TypeEpisodic,
			Status: store.StatusActive, Content: "user asked about the ALB alarm again", CreatedAt: time.Now()}
	}
	gw := &dimGateway{axis: 1013, reply: fmt.Sprintf(`[{"type":"semantic","content":%q,`+
		`"entities":[{"type":"topic","name":"itest-extract-reflect"}],"confidence":0.8,"changes_behavior":true}]`, content)}
	c := NewConsolidator(gw, &consolidateStore{recentEpisodic: episodics}, testLog(), Metrics{})
	reflector := New(gw, st, testLog())
	c.SetReflector(reflector)

	summary, err := c.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Reflected != 1 {
		t.Fatalf("Reflected = %d, want 1", summary.Reflected)
	}

	var id, actor string
	var status store.Status
	var sessionNull bool
	err = db.QueryRow(ctx, `SELECT id::text, actor, status, source_session IS NULL
		FROM memories WHERE content = $1`, content).Scan(&id, &actor, &status, &sessionNull)
	if err != nil {
		t.Fatalf("reflection row not found: %v", err)
	}
	if actor != store.ActorReflection || !sessionNull || status != store.StatusPending {
		t.Fatalf("row %s: actor=%q session_null=%v status=%q, want reflection/true/pending", id, actor, sessionNull, status)
	}
	got, err := st.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Actor != store.ActorReflection || got.SourceSession != "" || len(got.EntityRefs) != 1 {
		t.Fatalf("Get = %+v, want reflection actor, empty session, one entity ref", got)
	}

	// The Extractor itself returns the inserted id.
	gw.reply = fmt.Sprintf(`[{"type":"semantic","content":%q,"entities":[],"confidence":0.8,"changes_behavior":true}]`, content+" second")
	gw.axis = 1017
	ids, err := reflector.Extract(ctx, Request{Text: "x", Source: "reflection", Actor: store.ActorReflection})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("ids = %v, want one", ids)
	}
	if _, err := st.Get(ctx, ids[0]); err != nil {
		t.Fatalf("returned id %s not stored: %v", ids[0], err)
	}
}

// Integration (#878, #872): Extract is one transaction. The 2nd fact's
// insert fails (NUL byte), so the 1st fact's row and both facts'
// entities roll back: no orphan entities, no half batch.
func TestExtractRollsBackRunAgainstRealStore(t *testing.T) {
	st, db := itestStore(t)
	ctx := t.Context()
	first := fmt.Sprintf("%s tx first %d", itestMarker, time.Now().UnixNano())
	// The JSON escape decodes to a NUL byte, which a Postgres text
	// column rejects.
	gw := &dimGateway{axis: 1001, reply: `[` +
		`{"type":"episodic","content":"` + first + `","entities":[{"type":"topic","name":"itest-extract-tx-1"}],"confidence":0.9,"changes_behavior":true},` +
		`{"type":"semantic","content":"` + first + ` second \u0000","entities":[{"type":"topic","name":"itest-extract-tx-2"}],"confidence":0.9,"changes_behavior":true}]`}

	ids, err := New(gw, st, testLog()).Extract(ctx, Request{Text: "x"})
	if err == nil {
		t.Fatalf("Extract succeeded with ids %v, want the insert error", ids)
	}
	var memories, entities int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM memories WHERE content LIKE $1 || '%'`, first).Scan(&memories); err != nil {
		t.Fatalf("count memories: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM entities WHERE name IN ('itest-extract-tx-1', 'itest-extract-tx-2')`).Scan(&entities); err != nil {
		t.Fatalf("count entities: %v", err)
	}
	if memories != 0 || entities != 0 {
		t.Fatalf("after rollback: memories=%d entities=%d, want 0/0", memories, entities)
	}
}

// Integration (#878): without embeddings, a fact whose normalized text
// equals a rejected row is dropped; other facts still store.
func TestExtractTextFallbackAgainstRealStore(t *testing.T) {
	st, _ := itestStore(t)
	ctx := t.Context()
	rejected := fmt.Sprintf("%s user rejected fact %d.", itestMarker, time.Now().UnixNano())
	id, err := st.Insert(ctx, store.Memory{Type: store.TypeSemantic, Content: rejected, Confidence: 0.9})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.Reject(ctx, id); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	kept := rejected + " kept"
	gw := &embedlessGateway{fakeGateway{replies: []string{fmt.Sprintf(`[`+
		`{"type":"semantic","content":%q,"entities":[{"type":"topic","name":"itest-extract-text-1"}],"confidence":0.9,"changes_behavior":true},`+
		`{"type":"semantic","content":%q,"entities":[],"confidence":0.9,"changes_behavior":true}]`,
		"  "+strings.ToUpper(rejected[:len(rejected)-1])+"  ", kept)}}}

	ids, err := New(gw, st, testLog()).Extract(ctx, Request{Text: "x"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("ids = %v, want only the non-rejected fact", ids)
	}
	got, err := st.Get(ctx, ids[0])
	if err != nil || got.Content != kept {
		t.Fatalf("stored = %+v, %v; want %q", got, err, kept)
	}
}
