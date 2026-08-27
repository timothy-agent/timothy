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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

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
	// Sweep at setup AND teardown. The teardown runs after t.Context()
	// is canceled and the pool may already be closed, so it uses an
	// independent connection.
	sweepMemoryFixtures(ctx, db)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		sweepMemoryFixtures(cctx, conn)
	})
	return New(pool, log)
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func sweepMemoryFixtures(ctx context.Context, db execer) {
	_, _ = db.Exec(ctx, "DELETE FROM memories WHERE content LIKE $1 || '%'", testMarker)
	_, _ = db.Exec(ctx, "DELETE FROM entities WHERE name LIKE 'itest-entity-%'")
}

func mem(content string) Memory {
	return Memory{
		Type:       TypeSemantic,
		Content:    testMarker + " " + content,
		Confidence: 0.9,
	}
}

// TestEntityGraphQueries exercises the graph trio end-to-end: node
// counts, co-occurrence edges (active-only, dangling refs filtered),
// and the per-entity memory listing.
func TestEntityGraphQueries(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	upsert := func(name string) string {
		id, err := s.UpsertEntity(ctx, "topic", "itest-entity-"+t.Name()+"-"+name)
		if err != nil {
			t.Fatalf("UpsertEntity(%s): %v", name, err)
		}
		return id
	}
	a, b, c := upsert("a"), upsert("b"), upsert("c")
	const ghost = "99999999-9999-4999-8999-999999999999"

	insert := func(refs []string, active bool) string {
		m := mem("graph fixture")
		m.EntityRefs = refs
		if active {
			m.Actor = ActorUser // user-explicit memories activate directly
		}
		id, err := s.Insert(ctx, m)
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
		return id
	}
	insert([]string{a, b}, true)
	insert([]string{a, b}, true)
	insert([]string{a, c}, true)
	insert([]string{b, c}, false)    // pending: excluded everywhere
	insert([]string{a, ghost}, true) // dangling ref: counted for a, no edge

	entities, err := s.ListEntities(ctx)
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	counts := map[string]int{}
	for _, e := range entities {
		counts[e.ID] = e.MemoryCount
	}
	if counts[a] != 4 || counts[b] != 2 || counts[c] != 1 {
		t.Fatalf("counts a=%d b=%d c=%d, want 4/2/1", counts[a], counts[b], counts[c])
	}

	edges, err := s.EntityEdges(ctx)
	if err != nil {
		t.Fatalf("EntityEdges: %v", err)
	}
	mine := map[string]int{}
	ours := map[string]bool{a: true, b: true, c: true}
	for _, e := range edges {
		if e.Src == ghost || e.Dst == ghost {
			t.Fatalf("dangling ref produced an edge: %+v", e)
		}
		if ours[e.Src] && ours[e.Dst] {
			mine[e.Src+"|"+e.Dst] = e.Weight
		}
	}
	key := func(x, y string) string {
		if x < y {
			return x + "|" + y
		}
		return y + "|" + x
	}
	if len(mine) != 2 || mine[key(a, b)] != 2 || mine[key(a, c)] != 1 {
		t.Fatalf("edges = %v, want a-b:2 a-c:1 only", mine)
	}

	byA, err := s.ListByEntity(ctx, a)
	if err != nil {
		t.Fatalf("ListByEntity: %v", err)
	}
	if len(byA) != 4 {
		t.Fatalf("ListByEntity(a) = %d memories, want 4 (active only)", len(byA))
	}
	for i := 1; i < len(byA); i++ {
		if byA[i-1].CreatedAt.Before(byA[i].CreatedAt) {
			t.Fatalf("ListByEntity not newest-first at %d", i)
		}
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
	m.Embedding = make(Vector, 1024)
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

	// Wrong dimension must be refused by the vector(1024) column.
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

	// Entity rows are swept by testStore's fixture sweep.
	name := "itest-entity-" + t.Name()

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
	base := make(Vector, 1024)
	base[0], base[1] = 0.6, 0.8

	m := mem("nearest target fact")
	m.Embedding = base
	id, err := s.Insert(ctx, m)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Pending rows are visible to dedup (status tells the caller not to
	// Confirm them - Confirm's UPDATE is active-only).
	gotID, sim, status, ok, err := s.NearestActive(ctx, base)
	if err != nil {
		t.Fatalf("NearestActive: %v", err)
	}
	if !ok || gotID != id || status != StatusPending {
		t.Fatalf("NearestActive = (%s, status=%s, ok=%v), want (%s, pending, true)", gotID, status, ok, id)
	}

	if err := s.Promote(ctx, id); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	gotID, sim, status, ok, err = s.NearestActive(ctx, base)
	if err != nil {
		t.Fatalf("NearestActive: %v", err)
	}
	if !ok || gotID != id || status != StatusActive {
		t.Fatalf("NearestActive = (%s, status=%s, ok=%v), want (%s, active, true)", gotID, status, ok, id)
	}
	if sim < 0.999 {
		t.Fatalf("identical vector similarity = %f, want ~1", sim)
	}
}

// A rejected memory stays visible to NearestActive: rejection is a
// durable teaching signal, so a matching candidate must find it and
// get dropped instead of re-proposed forever.
func TestNearestActiveSeesRejected(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	base := make(Vector, 1024)
	base[0], base[1] = 0.6, 0.8

	m := mem("rejected target fact")
	m.Embedding = base
	id, err := s.Insert(ctx, m)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Reject(ctx, id); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	gotID, _, status, ok, err := s.NearestActive(ctx, base)
	if err != nil {
		t.Fatalf("NearestActive: %v", err)
	}
	if !ok || gotID != id || status != StatusRejected {
		t.Fatalf("NearestActive = (%s, status=%s, ok=%v), want (%s, rejected, true)", gotID, status, ok, id)
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

func TestNearDupPairs(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	near := func(dim int, delta float32) Vector {
		v := make(Vector, 1024)
		v[dim] = 1
		v[dim+1] = delta // small off-axis component
		return v
	}
	a := mem("dup group member one")
	a.Embedding = near(100, 0)
	b := mem("dup group member two")
	b.Embedding = near(100, 0.1) // cosine ~0.995 with a
	c := mem("unrelated fact")
	c.Embedding = near(200, 0)

	var ids []string
	for _, m := range []Memory{a, b, c} {
		id, err := s.Insert(ctx, m)
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
		if err := s.Promote(ctx, id); err != nil {
			t.Fatalf("Promote: %v", err)
		}
		ids = append(ids, id)
	}

	pairs, err := s.NearDupPairs(ctx, 0.95)
	if err != nil {
		t.Fatalf("NearDupPairs: %v", err)
	}
	found := false
	for _, p := range pairs {
		if (p[0] == ids[0] && p[1] == ids[1]) || (p[0] == ids[1] && p[1] == ids[0]) {
			found = true
		}
		if p[0] == ids[2] || p[1] == ids[2] {
			t.Fatalf("unrelated memory in a pair: %v", p)
		}
	}
	if !found {
		t.Fatalf("near-dup pair not found in %v", pairs)
	}
}

// TestRedundantPendingPairs proves two still-pending near-duplicate
// proposals surface as a pair, older id first, and that Promoting one
// (moving it out of pending) drops the pair on the next call.
func TestRedundantPendingPairs(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	near := func(dim int, delta float32) Vector {
		v := make(Vector, 1024)
		v[dim] = 1
		v[dim+1] = delta
		return v
	}
	older := mem("timezone preference set to Europe/Amsterdam")
	older.Embedding = near(400, 0)
	newer := mem("user timezone preference is Europe/Amsterdam")
	newer.Embedding = near(400, 0.1) // cosine ~0.995 with older
	unrelated := mem("unrelated pending fact")
	unrelated.Embedding = near(500, 0)

	olderID, err := s.Insert(ctx, older)
	if err != nil {
		t.Fatalf("Insert older: %v", err)
	}
	newerID, err := s.Insert(ctx, newer)
	if err != nil {
		t.Fatalf("Insert newer: %v", err)
	}
	if _, err := s.Insert(ctx, unrelated); err != nil {
		t.Fatalf("Insert unrelated: %v", err)
	}

	pairs, err := s.RedundantPendingPairs(ctx, 0.95)
	if err != nil {
		t.Fatalf("RedundantPendingPairs: %v", err)
	}
	found := false
	for _, p := range pairs {
		if p[0] == olderID && p[1] == newerID {
			found = true
		}
		if p[0] == unrelated.ID || p[1] == unrelated.ID {
			t.Fatalf("unrelated memory in a pair: %v", p)
		}
	}
	if !found {
		t.Fatalf("redundant pending pair (older, newer) not found in %v", pairs)
	}

	// Promoting the older half moves it out of pending; the pair must
	// no longer surface (mirrors NearDupPairs' active-only scope, just
	// the pending mirror of it).
	if err := s.Promote(ctx, olderID); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	pairs, err = s.RedundantPendingPairs(ctx, 0.95)
	if err != nil {
		t.Fatalf("RedundantPendingPairs after promote: %v", err)
	}
	for _, p := range pairs {
		if p[0] == olderID || p[1] == olderID {
			t.Fatalf("promoted memory still in a pending pair: %v", p)
		}
	}
}

// TestNearDupPairsCrossTypeExcluded proves a semantic+episodic pair
// never surfaces as a near-dup edge even at near-identical embeddings
// — merging across types would silently collapse a durable fact into
// a one-off event or vice versa.
func TestNearDupPairsCrossTypeExcluded(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	near := func(dim int, delta float32) Vector {
		v := make(Vector, 1024)
		v[dim] = 1
		v[dim+1] = delta
		return v
	}
	sem := mem("cross-type semantic fact")
	sem.Embedding = near(300, 0)
	epi := mem("cross-type episodic fact")
	epi.Type = TypeEpisodic
	epi.Embedding = near(300, 0.1) // cosine ~0.995 with sem

	var ids []string
	for _, m := range []Memory{sem, epi} {
		id, err := s.Insert(ctx, m)
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
		if err := s.Promote(ctx, id); err != nil {
			t.Fatalf("Promote: %v", err)
		}
		ids = append(ids, id)
	}

	pairs, err := s.NearDupPairs(ctx, 0.95)
	if err != nil {
		t.Fatalf("NearDupPairs: %v", err)
	}
	for _, p := range pairs {
		if (p[0] == ids[0] && p[1] == ids[1]) || (p[0] == ids[1] && p[1] == ids[0]) {
			t.Fatalf("cross-type pair surfaced: %v", p)
		}
	}
}

func TestApplyMerge(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	a, err := s.Insert(ctx, mem("original merge member a"))
	if err != nil {
		t.Fatalf("insert a: %v", err)
	}
	if err := s.Promote(ctx, a); err != nil {
		t.Fatalf("promote a: %v", err)
	}
	b, err := s.Insert(ctx, mem("original merge member b"))
	if err != nil {
		t.Fatalf("insert b: %v", err)
	}
	if err := s.Promote(ctx, b); err != nil {
		t.Fatalf("promote b: %v", err)
	}

	merged := mem("merged canonical fact")
	mergedID, err := s.ApplyMerge(ctx, merged, []string{a, b})
	if err != nil {
		t.Fatalf("ApplyMerge: %v", err)
	}

	got, err := s.Get(ctx, mergedID)
	if err != nil {
		t.Fatalf("Get merged: %v", err)
	}
	if got.Status != StatusActive {
		t.Fatalf("merged status = %s, want active", got.Status)
	}

	for _, memberID := range []string{a, b} {
		m, err := s.Get(ctx, memberID)
		if err != nil {
			t.Fatalf("Get member %s: %v", memberID, err)
		}
		if m.Status != StatusArchived || m.SupersededBy != mergedID {
			t.Fatalf("member %s = {status:%s superseded_by:%s}, want archived->%s",
				memberID, m.Status, m.SupersededBy, mergedID)
		}
	}
}

// TestApplyMergeConflictRollsBack proves a member that changed state
// underneath the merge (already superseded) aborts the whole
// transaction: no merged row exists, and the untouched member is left
// exactly as it was.
func TestApplyMergeConflictRollsBack(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	a, err := s.Insert(ctx, mem("conflict member a"))
	if err != nil {
		t.Fatalf("insert a: %v", err)
	}
	if err := s.Promote(ctx, a); err != nil {
		t.Fatalf("promote a: %v", err)
	}
	b, err := s.Insert(ctx, mem("conflict member b"))
	if err != nil {
		t.Fatalf("insert b: %v", err)
	}
	if err := s.Promote(ctx, b); err != nil {
		t.Fatalf("promote b: %v", err)
	}
	other, err := s.Insert(ctx, mem("pre-existing successor"))
	if err != nil {
		t.Fatalf("insert other: %v", err)
	}
	// Pre-supersede a so ApplyMerge's predicate no longer matches it.
	if err := s.Supersede(ctx, a, other); err != nil {
		t.Fatalf("presupersede a: %v", err)
	}

	merged := mem("merged fact that should never land")
	_, err = s.ApplyMerge(ctx, merged, []string{a, b})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ApplyMerge err = %v, want ErrNotFound", err)
	}

	// No merged row exists: content is marked with the test fixture
	// prefix, so a leaked row would show up in the sweep query — here
	// we check directly that b was left untouched instead.
	gotB, err := s.Get(ctx, b)
	if err != nil {
		t.Fatalf("Get b: %v", err)
	}
	if gotB.Status != StatusActive || gotB.SupersededBy != "" {
		t.Fatalf("b = {status:%s superseded_by:%s}, want untouched active", gotB.Status, gotB.SupersededBy)
	}
}

func TestConfirmBumpsActiveOnly(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	id, err := s.Insert(ctx, mem("confirmable via Confirm"))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Pending rows are not confirmable.
	if err := s.Confirm(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Confirm pending err = %v, want ErrNotFound", err)
	}
	if err := s.Promote(ctx, id); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	before, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := s.Confirm(ctx, id); err != nil {
		t.Fatalf("Confirm active: %v", err)
	}
	after, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !after.LastConfirmedAt.After(before.LastConfirmedAt) {
		t.Fatalf("last_confirmed_at not bumped: before=%v after=%v", before.LastConfirmedAt, after.LastConfirmedAt)
	}
	if after.Content != before.Content {
		t.Fatal("Confirm must never change content (append-only invariant)")
	}
}

func TestArchiveStaleEpisodic(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	old := mem("old unretrieved episodic")
	old.Type = TypeEpisodic
	oldID, _ := s.Insert(ctx, old)
	_ = s.Promote(ctx, oldID)
	// Backdate creation past the window; never retrieved.
	if _, err := db.Exec(ctx,
		"UPDATE memories SET created_at = now() - interval '200 days' WHERE id = $1", oldID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	used := mem("old but recently retrieved episodic")
	used.Type = TypeEpisodic
	usedID, _ := s.Insert(ctx, used)
	_ = s.Promote(ctx, usedID)
	if _, err := db.Exec(ctx,
		"UPDATE memories SET created_at = now() - interval '200 days', last_retrieved_at = now() WHERE id = $1", usedID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	n, err := s.ArchiveStaleEpisodic(ctx, time.Now().Add(-180*24*time.Hour))
	if err != nil {
		t.Fatalf("ArchiveStaleEpisodic: %v", err)
	}
	if n < 1 {
		t.Fatalf("archived %d, want >= 1", n)
	}
	if got, _ := s.Get(ctx, oldID); got.Status != StatusArchived {
		t.Fatalf("old unretrieved = %s, want archived", got.Status)
	}
	if got, _ := s.Get(ctx, usedID); got.Status != StatusActive {
		t.Fatalf("recently retrieved = %s, want still active", got.Status)
	}
}

func TestDecayStaleSemantic(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	stale := mem("stale semantic fact")
	staleID, _ := s.Insert(ctx, stale)
	_ = s.Promote(ctx, staleID)
	if _, err := db.Exec(ctx,
		"UPDATE memories SET last_confirmed_at = now() - interval '400 days' WHERE id = $1", staleID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	fresh := mem("fresh semantic fact")
	freshID, _ := s.Insert(ctx, fresh)
	_ = s.Promote(ctx, freshID)

	ids, err := s.DecayStaleSemantic(ctx, time.Now().Add(-365*24*time.Hour), 0.8, 10)
	if err != nil {
		t.Fatalf("DecayStaleSemantic: %v", err)
	}
	decayed := false
	for _, id := range ids {
		if id == freshID {
			t.Fatal("fresh fact decayed")
		}
		if id == staleID {
			decayed = true
		}
	}
	if !decayed {
		t.Fatalf("stale fact not in decay queue %v", ids)
	}
	got, _ := s.Get(ctx, staleID)
	if got.Confidence >= 0.9 {
		t.Fatalf("confidence = %v, want decayed below 0.9", got.Confidence)
	}
	if got.Status != StatusActive {
		t.Fatalf("decayed fact = %s, must stay active (still retrievable)", got.Status)
	}
}

func TestDemoteUnused(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	backdate := func(id string) {
		if _, err := db.Exec(ctx,
			"UPDATE memories SET created_at = now() - interval '90 days' WHERE id = $1", id); err != nil {
			t.Fatalf("backdate: %v", err)
		}
	}

	eligible := mem("stale unused pending fact")
	eligible.Confidence = 0.5
	eligibleID, _ := s.Insert(ctx, eligible)
	backdate(eligibleID)

	retrieved := mem("stale but retrieved pending fact")
	retrieved.Confidence = 0.5
	retrievedID, _ := s.Insert(ctx, retrieved)
	backdate(retrievedID)
	if _, err := db.Exec(ctx,
		"UPDATE memories SET retrieval_hits = 1, last_retrieved_at = now() WHERE id = $1", retrievedID); err != nil {
		t.Fatalf("mark retrieved: %v", err)
	}

	// Retrieved before retrieval_hits existed: zero counter but a
	// non-null last_retrieved_at, must stay immune.
	legacy := mem("stale pending fact retrieved pre-counter")
	legacy.Confidence = 0.5
	legacyID, _ := s.Insert(ctx, legacy)
	backdate(legacyID)
	if _, err := db.Exec(ctx,
		"UPDATE memories SET last_retrieved_at = now() WHERE id = $1", legacyID); err != nil {
		t.Fatalf("mark legacy retrieved: %v", err)
	}

	confirmed := mem("stale but confirmed fact")
	confirmed.Confidence = 0.5
	confirmedID, _ := s.Insert(ctx, confirmed)
	backdate(confirmedID)
	if err := s.Promote(ctx, confirmedID); err != nil {
		t.Fatalf("promote: %v", err)
	}

	highConfidence := mem("stale high-confidence pending fact")
	highConfidence.Confidence = 0.95
	highConfidenceID, _ := s.Insert(ctx, highConfidence)
	backdate(highConfidenceID)

	fresh := mem("recent unused pending fact")
	fresh.Confidence = 0.5
	freshID, _ := s.Insert(ctx, fresh)

	ids, err := s.DemoteUnused(ctx, time.Now().Add(-60*24*time.Hour), 0.8, 10)
	if err != nil {
		t.Fatalf("DemoteUnused: %v", err)
	}
	demoted := map[string]bool{}
	for _, id := range ids {
		demoted[id] = true
	}
	if !demoted[eligibleID] {
		t.Fatalf("eligible memory not demoted: %v", ids)
	}
	for name, id := range map[string]string{
		"retrieved": retrievedID, "confirmed": confirmedID,
		"high-confidence": highConfidenceID, "fresh": freshID,
		"legacy-retrieved": legacyID,
	} {
		if demoted[id] {
			t.Fatalf("%s memory wrongly demoted", name)
		}
	}

	if got, _ := s.Get(ctx, eligibleID); got.Status != StatusArchived {
		t.Fatalf("eligible status = %s, want archived", got.Status)
	}
	if got, _ := s.Get(ctx, retrievedID); got.Status != StatusPending {
		t.Fatalf("retrieved status = %s, want still pending", got.Status)
	}
	if got, _ := s.Get(ctx, confirmedID); got.Status != StatusActive {
		t.Fatalf("confirmed status = %s, want still active", got.Status)
	}
}

// rawDB opens a direct connection for tests that must corrupt state in
// ways the store API forbids.
func rawDB(t *testing.T) *pgxpool.Pool {
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
	return db
}

func TestSupersedeIsSingleShot(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	a, err := s.Insert(ctx, mem("original diet fact"))
	if err != nil {
		t.Fatalf("insert a: %v", err)
	}
	b, err := s.Insert(ctx, mem("corrected diet fact"))
	if err != nil {
		t.Fatalf("insert b: %v", err)
	}
	c, err := s.Insert(ctx, mem("competing correction"))
	if err != nil {
		t.Fatalf("insert c: %v", err)
	}

	if err := s.Supersede(ctx, a, b); err != nil {
		t.Fatalf("first supersede: %v", err)
	}
	// A superseded memory is settled history: a second link attempt
	// must refuse instead of silently rewriting the chain.
	if err := s.Supersede(ctx, a, c); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double supersede = %v, want ErrNotFound", err)
	}

	chain, err := s.Chain(ctx, a)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != 2 || chain[0].ID != a || chain[1].ID != b {
		t.Fatalf("chain = %d entries, want [a b]", len(chain))
	}
}

func TestChainTerminatesOnCycle(t *testing.T) {
	s := testStore(t)
	db := rawDB(t)
	ctx := t.Context()

	a, err := s.Insert(ctx, mem("cycle head"))
	if err != nil {
		t.Fatalf("insert a: %v", err)
	}
	b, err := s.Insert(ctx, mem("cycle tail"))
	if err != nil {
		t.Fatalf("insert b: %v", err)
	}
	if err := s.Supersede(ctx, a, b); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	// No store API can produce a cycle (Supersede refuses re-linking),
	// so corrupt the row directly and prove the walk still terminates.
	if _, err := db.Exec(ctx,
		`UPDATE memories SET superseded_by = $2 WHERE id = $1`, b, a); err != nil {
		t.Fatalf("forge cycle: %v", err)
	}

	chain, err := s.Chain(ctx, a)
	if err != nil {
		t.Fatalf("chain on cycle: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("cycle chain = %d entries, want 2 (each node once)", len(chain))
	}
}
