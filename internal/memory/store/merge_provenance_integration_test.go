//go:build integration

package store

import (
	"testing"
	"time"
)

// A merge of a user row and an agent row keeps the user actor and the
// latest input confirmation time instead of defaulting to agent/now().
func TestApplyMergeKeepsActorAndFreshness(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	user := mem("merge provenance user row")
	user.Actor = ActorUser
	userID, err := s.Insert(ctx, user)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	agentID, err := s.Insert(ctx, mem("merge provenance agent row"))
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := s.Promote(ctx, agentID); err != nil {
		t.Fatalf("promote agent: %v", err)
	}
	older := time.Now().Add(-200 * 24 * time.Hour).UTC().Truncate(time.Microsecond)
	newer := older.Add(30 * 24 * time.Hour)
	for id, ts := range map[string]time.Time{userID: older, agentID: newer} {
		if _, err := db.Exec(ctx, "UPDATE memories SET last_confirmed_at = $2 WHERE id = $1", id, ts); err != nil {
			t.Fatalf("backdate %s: %v", id, err)
		}
	}

	merged := mem("merge provenance merged row")
	merged.Actor = ActorUser
	merged.LastConfirmedAt = newer
	mergedID, err := s.ApplyMerge(ctx, merged, []string{userID, agentID})
	if err != nil {
		t.Fatalf("ApplyMerge: %v", err)
	}
	got, err := s.Get(ctx, mergedID)
	if err != nil {
		t.Fatalf("Get merged: %v", err)
	}
	if got.Actor != ActorUser {
		t.Fatalf("actor = %q, want user", got.Actor)
	}
	if !got.LastConfirmedAt.Equal(newer) {
		t.Fatalf("last_confirmed_at = %v, want %v", got.LastConfirmedAt, newer)
	}
}
