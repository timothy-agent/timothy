//go:build integration

package store

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func lastConfirmed(t *testing.T, db *pgxpool.Pool, id string) time.Time {
	t.Helper()
	var at time.Time
	if err := db.QueryRow(t.Context(), `SELECT last_confirmed_at FROM memories WHERE id = $1`, id).Scan(&at); err != nil {
		t.Fatalf("last_confirmed_at %s: %v", id, err)
	}
	return at
}

func entityCount(t *testing.T, db *pgxpool.Pool, names ...string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM entities WHERE name = ANY($1)`, names).Scan(&n); err != nil {
		t.Fatalf("count entities: %v", err)
	}
	return n
}

func memoryCount(t *testing.T, db *pgxpool.Pool, contents ...string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM memories WHERE content = ANY($1)`, contents).Scan(&n); err != nil {
		t.Fatalf("count memories: %v", err)
	}
	return n
}

// seedStaleActive inserts an active row whose last_confirmed_at is a
// day old, so a confirmation bump is observable.
func seedStaleActive(t *testing.T, s *Store, db *pgxpool.Pool, content string) (string, time.Time) {
	t.Helper()
	m := mem(content)
	m.Actor = ActorUser
	id, err := s.Insert(t.Context(), m)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE memories SET last_confirmed_at = now() - interval '1 day' WHERE id = $1`, id); err != nil {
		t.Fatalf("age seed: %v", err)
	}
	return id, lastConfirmed(t, db, id)
}

// D-144: one extraction run commits confirmations, entities, inserts
// and promotions together.
func TestApplyExtractionCommitsRun(t *testing.T) {
	s := testStore(t)
	db := rawDB(t)
	ctx := t.Context()
	activeID, before := seedStaleActive(t, s, db, "apply commit restated")

	promoted, pending := mem("apply commit promoted"), mem("apply commit pending")
	ids, err := s.ApplyExtraction(ctx, []Confirmation{{ID: activeID, Confidence: 0.9}}, []Proposal{
		{Memory: promoted, Entities: []EntityKey{{Type: "topic", Name: "itest-entity-apply-1"}}, Promote: true},
		{Memory: pending, Entities: []EntityKey{{Type: "topic", Name: "itest-entity-apply-1"}, {Type: "place", Name: "itest-entity-apply-2"}}},
	})
	if err != nil {
		t.Fatalf("ApplyExtraction: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v, want 2", ids)
	}
	first, err := s.Get(ctx, ids[0])
	if err != nil {
		t.Fatalf("Get first: %v", err)
	}
	second, err := s.Get(ctx, ids[1])
	if err != nil {
		t.Fatalf("Get second: %v", err)
	}
	if first.Content != promoted.Content || first.Status != StatusActive {
		t.Fatalf("first = %+v, want promoted content active", first)
	}
	if second.Content != pending.Content || second.Status != StatusPending {
		t.Fatalf("second = %+v, want pending content pending", second)
	}
	if len(first.EntityRefs) != 1 || len(second.EntityRefs) != 2 || second.EntityRefs[0] != first.EntityRefs[0] {
		t.Fatalf("entity refs = %v / %v, want the shared entity resolved once", first.EntityRefs, second.EntityRefs)
	}
	if after := lastConfirmed(t, db, activeID); !after.After(before) {
		t.Fatalf("confirm not applied: last_confirmed_at %v -> %v", before, after)
	}
}

// D-147: ApplyExtraction's confirm step reconfirms like Confirm:
// decayed_at clears, confidence lifts to the highest restatement and
// never drops, and a non-active id is skipped.
func TestApplyExtractionConfirmClearsDecay(t *testing.T) {
	s := testStore(t)
	db := rawDB(t)
	ctx := t.Context()
	decayed, _ := seedStaleActive(t, s, db, "apply confirm decayed")
	strong, _ := seedStaleActive(t, s, db, "apply confirm strong")
	pendingID, err := s.Insert(ctx, mem("apply confirm pending"))
	if err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE memories SET decayed_at = now(), confidence = 0.15 WHERE id = $1`, decayed); err != nil {
		t.Fatalf("decay seed: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE memories SET decayed_at = now(), confidence = 0.95 WHERE id = $1`, strong); err != nil {
		t.Fatalf("strong seed: %v", err)
	}

	if _, err := s.ApplyExtraction(ctx, []Confirmation{
		{ID: decayed, Confidence: 0.6}, {ID: decayed, Confidence: 0.8},
		{ID: strong, Confidence: 0.3}, {ID: pendingID, Confidence: 1},
	}, nil); err != nil {
		t.Fatalf("ApplyExtraction: %v", err)
	}
	for id, want := range map[string]float32{decayed: 0.8, strong: 0.95} {
		var decayedAt *time.Time
		var conf float32
		if err := db.QueryRow(ctx, `SELECT decayed_at, confidence FROM memories WHERE id = $1`, id).
			Scan(&decayedAt, &conf); err != nil {
			t.Fatalf("read: %v", err)
		}
		if decayedAt != nil || conf != want {
			t.Fatalf("row %s: decayed_at=%v confidence=%v, want NULL/%v", id, decayedAt, conf, want)
		}
	}
	got, err := s.Get(ctx, pendingID)
	if err != nil {
		t.Fatalf("Get pending: %v", err)
	}
	if got.Status != StatusPending || got.Confidence != 0.9 {
		t.Fatalf("pending row = %s/%v, want untouched", got.Status, got.Confidence)
	}
}

// D-144 (#872, #878): a failing insert rolls back the whole run: no
// earlier insert, no entity, no confirmation survives.
func TestApplyExtractionRollsBackOnFailure(t *testing.T) {
	s := testStore(t)
	db := rawDB(t)
	ctx := t.Context()
	activeID, before := seedStaleActive(t, s, db, "apply rollback restated")

	ok, bad := mem("apply rollback first"), mem("apply rollback second")
	bad.SourceSession = "reflection" // not a uuid: the insert fails
	_, err := s.ApplyExtraction(ctx, []Confirmation{{ID: activeID, Confidence: 0.9}}, []Proposal{
		{Memory: ok, Entities: []EntityKey{{Type: "topic", Name: "itest-entity-rollback-1"}}, Promote: true},
		{Memory: bad, Entities: []EntityKey{{Type: "topic", Name: "itest-entity-rollback-2"}}},
	})
	if err == nil {
		t.Fatal("ApplyExtraction succeeded, want the uuid error")
	}
	if n := memoryCount(t, db, ok.Content, bad.Content); n != 0 {
		t.Fatalf("memories left = %d, want 0", n)
	}
	if n := entityCount(t, db, "itest-entity-rollback-1", "itest-entity-rollback-2"); n != 0 {
		t.Fatalf("orphan entities left = %d, want 0", n)
	}
	if after := lastConfirmed(t, db, activeID); !after.Equal(before) {
		t.Fatalf("confirm survived rollback: last_confirmed_at %v -> %v", before, after)
	}
}

// The no-embedding rejected lookup matches normalized text
// (case, whitespace, trailing punctuation) and only rejected rows.
func TestRejectedWithContent(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	rejectedID, err := s.Insert(ctx, mem("User lives in Porto."))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Reject(ctx, rejectedID); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if _, err := s.Insert(ctx, mem("User likes tea.")); err != nil {
		t.Fatalf("Insert pending: %v", err)
	}

	tests := []struct {
		name    string
		content string
		wantOK  bool
	}{
		{name: "exact", content: testMarker + " User lives in Porto.", wantOK: true},
		{name: "case whitespace punctuation", content: "  " + testMarker + "   user LIVES in porto!! ", wantOK: true},
		{name: "different fact", content: testMarker + " User lives in Lisbon.", wantOK: false},
		{name: "pending row is not rejected", content: testMarker + " User likes tea.", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, ok, err := s.RejectedWithContent(ctx, tc.content)
			if err != nil {
				t.Fatalf("RejectedWithContent: %v", err)
			}
			if ok != tc.wantOK || (ok && id != rejectedID) {
				t.Fatalf("RejectedWithContent(%q) = %q, %v; want ok=%v id=%s", tc.content, id, ok, tc.wantOK, rejectedID)
			}
		})
	}
}
