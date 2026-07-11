//go:build integration

package store

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const testMarker = "itest-memory:"

func testStore(t *testing.T) *Store {
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
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(),
			"DELETE FROM memories WHERE content LIKE $1 || '%'", testMarker)
	})
	return New(pool, log)
}

func mem(content string) Memory {
	return Memory{
		Type:       TypeSemantic,
		Content:    testMarker + " " + content,
		Confidence: 0.9,
	}
}

func TestInsertStagesAgentWrites(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Insert(ctx, mem("user lives in Porto"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusPending {
		t.Fatalf("agent write status = %s, want pending", got.Status)
	}
	if got.Actor != "agent" {
		t.Fatalf("actor = %q, want agent", got.Actor)
	}

	m := mem("user prefers aisle seats")
	m.Actor = ActorUser
	uid, err := s.Insert(ctx, m)
	if err != nil {
		t.Fatalf("Insert user-explicit: %v", err)
	}
	ugot, err := s.Get(ctx, uid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ugot.Status != StatusActive {
		t.Fatalf("user-explicit status = %s, want active", ugot.Status)
	}
}

func TestInsertStoresEmbedding(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	m := mem("embedded fact")
	m.Embedding = make(Vector, 1536)
	m.Embedding[0] = 0.5
	id, err := s.Insert(ctx, m)
	if err != nil {
		t.Fatalf("Insert with embedding: %v", err)
	}

	db, _ := s.db.Get()
	var hasEmbedding bool
	if err := db.QueryRow(ctx,
		"SELECT embedding IS NOT NULL FROM memories WHERE id = $1", id).Scan(&hasEmbedding); err != nil {
		t.Fatalf("check embedding: %v", err)
	}
	if !hasEmbedding {
		t.Fatal("embedding not stored")
	}

	// Wrong dimension must be refused by the vector(1536) column.
	bad := mem("bad dimension")
	bad.Embedding = Vector{0.1, 0.2}
	if _, err := s.Insert(ctx, bad); err == nil {
		t.Fatal("2-dim embedding accepted, want dimension error")
	}
}

func TestLifecycleTransitions(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	tests := []struct {
		name string
		move func(context.Context, string) error
		want Status
	}{
		{name: "promote", move: s.Promote, want: StatusActive},
		{name: "reject", move: s.Reject, want: StatusRejected},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, err := s.Insert(ctx, mem("lifecycle "+tc.name))
			if err != nil {
				t.Fatalf("Insert: %v", err)
			}
			if err := tc.move(ctx, id); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			got, err := s.Get(ctx, id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %s, want %s", got.Status, tc.want)
			}
			// Same transition twice must fail: the row left the
			// source status.
			if err := tc.move(ctx, id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("second %s err = %v, want ErrNotFound", tc.name, err)
			}
		})
	}
}

func TestPromoteBumpsConfirmation(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Insert(ctx, mem("confirmable fact"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	before, _ := s.Get(ctx, id)
	if err := s.Promote(ctx, id); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	after, _ := s.Get(ctx, id)
	if !after.LastConfirmedAt.After(before.LastConfirmedAt) {
		t.Fatalf("last_confirmed_at not bumped: before=%v after=%v",
			before.LastConfirmedAt, after.LastConfirmedAt)
	}
}

func TestArchiveOnlyActive(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Insert(ctx, mem("to archive"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Pending rows are not archivable (they get rejected instead).
	if err := s.Archive(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Archive pending err = %v, want ErrNotFound", err)
	}
	if err := s.Promote(ctx, id); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if err := s.Archive(ctx, id); err != nil {
		t.Fatalf("Archive active: %v", err)
	}
	got, _ := s.Get(ctx, id)
	if got.Status != StatusArchived {
		t.Fatalf("status = %s, want archived", got.Status)
	}
}

func TestSupersedeChain(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	// v1 active, superseded by v2, superseded by v3.
	v1, err := s.Insert(ctx, mem("user lives in Berlin"))
	if err != nil {
		t.Fatalf("Insert v1: %v", err)
	}
	if err := s.Promote(ctx, v1); err != nil {
		t.Fatalf("Promote v1: %v", err)
	}
	v2, err := s.Insert(ctx, mem("user lives in Lisbon"))
	if err != nil {
		t.Fatalf("Insert v2: %v", err)
	}
	if err := s.Supersede(ctx, v1, v2); err != nil {
		t.Fatalf("Supersede v1→v2: %v", err)
	}
	if err := s.Promote(ctx, v2); err != nil {
		t.Fatalf("Promote v2: %v", err)
	}
	v3, err := s.Insert(ctx, mem("user lives in Porto"))
	if err != nil {
		t.Fatalf("Insert v3: %v", err)
	}
	if err := s.Supersede(ctx, v2, v3); err != nil {
		t.Fatalf("Supersede v2→v3: %v", err)
	}

	old, _ := s.Get(ctx, v1)
	if old.Status != StatusArchived || old.SupersededBy != v2 {
		t.Fatalf("v1 = {status:%s superseded_by:%s}, want archived→%s",
			old.Status, old.SupersededBy, v2)
	}

	chain, err := s.Chain(ctx, v1)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if len(chain) != 3 || chain[0].ID != v1 || chain[1].ID != v2 || chain[2].ID != v3 {
		ids := make([]string, len(chain))
		for i, m := range chain {
			ids[i] = m.ID
		}
		t.Fatalf("chain = %v, want [%s %s %s]", ids, v1, v2, v3)
	}

	// A superseded row cannot be superseded again — the chain is
	// append-only through its tail.
	if err := s.Supersede(ctx, v1, v3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("re-supersede err = %v, want ErrNotFound", err)
	}
}

func TestUpsertEntityIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	name := "itest-entity-" + t.Name()
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM entities WHERE name = $1", name)
	})

	first, err := s.UpsertEntity(ctx, "project", name)
	if err != nil {
		t.Fatalf("UpsertEntity: %v", err)
	}
	second, err := s.UpsertEntity(ctx, "project", name)
	if err != nil {
		t.Fatalf("UpsertEntity again: %v", err)
	}
	if first != second {
		t.Fatalf("upsert returned different ids: %s vs %s", first, second)
	}
	// Same name, different type = a different entity.
	other, err := s.UpsertEntity(ctx, "topic", name)
	if err != nil {
		t.Fatalf("UpsertEntity other type: %v", err)
	}
	if other == first {
		t.Fatal("distinct (type, name) collapsed to one entity")
	}
}

func TestNearestActive(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	// No active embedded rows that match closely → still returns the
	// nearest, so assert with a known-identical vector instead.
	base := make(Vector, 1536)
	base[0], base[1] = 0.6, 0.8

	m := mem("nearest target fact")
	m.Embedding = base
	id, err := s.Insert(ctx, m)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Pending rows are invisible to dedup.
	if gotID, _, ok, err := s.NearestActive(ctx, base); err != nil {
		t.Fatalf("NearestActive: %v", err)
	} else if ok && gotID == id {
		t.Fatal("pending row surfaced in NearestActive")
	}

	if err := s.Promote(ctx, id); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	gotID, sim, ok, err := s.NearestActive(ctx, base)
	if err != nil {
		t.Fatalf("NearestActive: %v", err)
	}
	if !ok || gotID != id {
		t.Fatalf("NearestActive = (%s, ok=%v), want %s", gotID, ok, id)
	}
	if sim < 0.999 {
		t.Fatalf("identical vector similarity = %f, want ~1", sim)
	}
}

func TestListByStatusFiltersTypes(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	sem := mem("semantic pending fact for listing")
	if _, err := s.Insert(ctx, sem); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	epi := mem("episodic pending fact for listing")
	epi.Type = TypeEpisodic
	if _, err := s.Insert(ctx, epi); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := s.ListByStatus(ctx, StatusPending, TypeEpisodic)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	for _, m := range got {
		if m.Type != TypeEpisodic {
			t.Fatalf("type filter leaked %s row %s", m.Type, m.ID)
		}
	}
	all, err := s.ListByStatus(ctx, StatusPending)
	if err != nil {
		t.Fatalf("ListByStatus all: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("unfiltered list has %d rows, want >= 2", len(all))
	}
}
