//go:build integration

package missions

import (
	"context"
	"slices"
	"testing"
	"time"
)

// pageMission creates a tagged mission and pins its listing columns.
func pageMission(t *testing.T, s *Store, goal, kind, harness, origin string, created time.Time) string {
	t.Helper()
	ctx := t.Context()
	id, err := s.Create(ctx, Mission{Goal: marker + goal, Kind: KindGeneral, Route: "default"})
	if err != nil {
		t.Fatalf("Create %s: %v", goal, err)
	}
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE missions SET kind = $2, harness = $3, origin_kind = $4, created_at = $5 WHERE id = $1`,
		id, kind, harness, origin, created); err != nil {
		t.Fatalf("pin %s: %v", goal, err)
	}
	return id
}

// pageAll walks every page of filter with page size limit.
func pageAll(t *testing.T, s *Store, filter ListFilter, limit int) []string {
	t.Helper()
	filter.Limit = limit
	var ids []string
	for range 20 {
		page, err := s.List(t.Context(), filter)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, m := range page {
			ids = append(ids, m.ID)
		}
		if len(page) < limit {
			return ids
		}
		last := page[len(page)-1]
		filter.Before, filter.BeforeID = last.CreatedAt, last.ID
	}
	t.Fatal("paging did not terminate")
	return nil
}

// TestListKeysetPagesAcrossTies: rows sharing created_at split across a
// page boundary are neither dropped nor repeated, in (created_at, id)
// DESC order.
func TestListKeysetPagesAcrossTies(t *testing.T) {
	s := testStore(t)
	tag := "pagetag-" + time.Now().Format("150405.000000")
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	newest := pageMission(t, s, tag+" newest", KindGeneral, "", OriginAPI, base.Add(time.Minute))
	var tied []string
	for range 4 {
		tied = append(tied, pageMission(t, s, tag+" tied", KindGeneral, "", OriginAPI, base))
	}
	oldest := pageMission(t, s, tag+" oldest", KindGeneral, "", OriginAPI, base.Add(-time.Minute))

	slices.Sort(tied)
	slices.Reverse(tied)
	want := append(append([]string{newest}, tied...), oldest)

	for _, limit := range []int{1, 2, 3, 6} {
		got := pageAll(t, s, ListFilter{Query: tag}, limit)
		if !slices.Equal(got, want) {
			t.Fatalf("limit %d: paged ids = %v, want %v", limit, got, want)
		}
	}
}

// TestListFiltersCombineWithCursor: kind, harness, source and model
// filters hold on every page, not just the first.
func TestListFiltersCombineWithCursor(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	tag := "filtertag-" + time.Now().Format("150405.000000")
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	var codingNative []string
	for i := range 5 {
		codingNative = append(codingNative, pageMission(t, s, tag+" coding native", KindCoding, "", OriginAPI, base.Add(-time.Duration(i)*time.Second)))
	}
	cli := pageMission(t, s, tag+" coding cli", KindCoding, "claude-cli", OriginAPI, base.Add(time.Second))
	general := pageMission(t, s, tag+" general", KindGeneral, "", OriginAPI, base.Add(2*time.Second))
	auto := pageMission(t, s, tag+" coding auto", KindCoding, "", OriginAutomation, base.Add(-2*time.Second))

	got := pageAll(t, s, ListFilter{Query: tag, Kind: KindCoding, Harness: HarnessNative, Source: SourceManual}, 2)
	if !slices.Equal(got, codingNative) {
		t.Fatalf("coding+native+manual paged = %v, want %v", got, codingNative)
	}
	if got := pageAll(t, s, ListFilter{Query: tag, Harness: "claude-cli"}, 2); !slices.Equal(got, []string{cli}) {
		t.Fatalf("harness=claude-cli = %v, want [%s]", got, cli)
	}
	if got := pageAll(t, s, ListFilter{Query: tag, Source: SourceAutomated}, 2); !slices.Equal(got, []string{auto}) {
		t.Fatalf("source=automated = %v, want [%s]", got, auto)
	}
	if got := pageAll(t, s, ListFilter{Query: tag, Kind: KindGeneral}, 2); !slices.Equal(got, []string{general}) {
		t.Fatalf("kind=general = %v, want [%s]", got, general)
	}

	// Model filter: the top ledger model, ranked by request count.
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	model := "itest-top-" + tag
	ledgerRows := []struct {
		mission, model string
		n              int
	}{
		{codingNative[0], model, 3},
		{codingNative[0], "itest-other", 1},
		{codingNative[2], model, 2},
		{codingNative[4], model, 1},
		{codingNative[3], model, 1},
		{codingNative[3], "itest-other", 4}, // top model is the other one
	}
	for _, r := range ledgerRows {
		for range r.n {
			if _, err := db.Exec(ctx, `INSERT INTO cost_ledger (provider, model, route, latency_ms, status, mission_id)
				VALUES ('itest-provider', $1, 'itest', 1, 'ok', $2)`, r.model, r.mission); err != nil {
				t.Fatalf("insert ledger row: %v", err)
			}
		}
	}
	cleanupExec(t, `DELETE FROM cost_ledger WHERE mission_id = ANY($1)`, codingNative)

	want := []string{codingNative[0], codingNative[2], codingNative[4]}
	if got := pageAll(t, s, ListFilter{Query: tag, Model: model}, 1); !slices.Equal(got, want) {
		t.Fatalf("model filter paged = %v, want %v", got, want)
	}
	if got := pageAll(t, s, ListFilter{Query: tag, Model: model, Kind: KindGeneral}, 1); len(got) != 0 {
		t.Fatalf("model+kind=general = %v, want none", got)
	}
}

// TestNotificationsUnreadFilterAndPaging: unread=true drops read rows,
// and the keyset cursor walks tied created_at rows without loss.
func TestNotificationsUnreadFilterAndPaging(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	n := testNotifier(t, s)
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	id, err := s.Create(ctx, Mission{Goal: marker + "notify-page", Kind: KindGeneral})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Far in the future so these rows lead every page regardless of
	// other rows in the shared database.
	at := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	var unread []string
	for i := range 5 {
		var nid string
		if err := db.QueryRow(ctx, `INSERT INTO notifications (mission_id, kind, message, read, created_at)
			VALUES ($1, 'paused', 'itest page', $2, $3) RETURNING id`, id, i%2 == 1, at).Scan(&nid); err != nil {
			t.Fatalf("insert notification: %v", err)
		}
		if i%2 == 0 {
			unread = append(unread, nid)
		}
	}
	slices.Sort(unread)
	slices.Reverse(unread)

	var got []string
	filter := NotificationFilter{Unread: true, Limit: 2}
	for range 5 {
		page, err := n.List(ctx, filter)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, note := range page {
			if note.Read {
				t.Fatalf("unread filter returned read row %s", note.ID)
			}
			if note.MissionID == id {
				got = append(got, note.ID)
			}
		}
		if len(page) < filter.Limit || len(got) >= len(unread) {
			break
		}
		last := page[len(page)-1]
		filter.Before, filter.BeforeID = last.CreatedAt, last.ID
	}
	if !slices.Equal(got, unread) {
		t.Fatalf("unread paged = %v, want %v", got, unread)
	}

	all, err := n.List(ctx, NotificationFilter{Limit: 5})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("List(limit 5) = %d rows, want 5", len(all))
	}
	for _, note := range all {
		if note.MissionID != id {
			t.Fatalf("first page holds a foreign row %s, want only the future-dated ones", note.ID)
		}
	}
}
