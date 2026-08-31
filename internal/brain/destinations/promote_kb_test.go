package destinations

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// fakeOpener stubs artifactOpener over an in-memory id -> content map.
type fakeOpener struct {
	data map[string][]byte
}

func (f *fakeOpener) Open(_ context.Context, id string) (io.ReadCloser, attachments.Attachment, error) {
	data, ok := f.data[id]
	if !ok {
		return nil, attachments.Attachment{}, attachments.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), attachments.Attachment{ID: id, Mime: "text/plain", SizeBytes: int64(len(data))}, nil
}

// fakeKBStore stubs kbDocStore over an in-memory document map, keyed by
// (source_type, source_ref) for FindDocumentBySource: mirrors kb.Store's
// own dedup contract without a real Postgres pool.
type fakeKBStore struct {
	docs   map[string]kb.Document
	nextID int
}

func newFakeKBStore() *fakeKBStore {
	return &fakeKBStore{docs: map[string]kb.Document{}}
}

func (f *fakeKBStore) FindDocumentBySource(_ context.Context, sourceType, sourceRef string) (kb.Document, error) {
	for _, d := range f.docs {
		if d.SourceType == sourceType && d.SourceRef == sourceRef {
			return d, nil
		}
	}
	return kb.Document{}, kb.ErrNotFound
}

func (f *fakeKBStore) CreateDocument(_ context.Context, collectionID, title, sourceType, sourceRef, provenance, markdown string, bytes int64) (string, error) {
	f.nextID++
	id := "doc" + strconv.Itoa(f.nextID)
	f.docs[id] = kb.Document{ID: id, CollectionID: collectionID, Title: title, SourceType: sourceType, SourceRef: sourceRef, Provenance: provenance, Markdown: markdown, Bytes: bytes, Status: "pending"}
	return id, nil
}

func (f *fakeKBStore) ReplaceDocumentContent(_ context.Context, id, title, markdown string, bytes int64, collectionID string) error {
	d, ok := f.docs[id]
	if !ok {
		return kb.ErrNotFound
	}
	d.Title, d.Markdown, d.Bytes = title, markdown, bytes
	if collectionID != "" {
		d.CollectionID = collectionID
	}
	d.Status = "pending"
	f.docs[id] = d
	return nil
}

func (f *fakeKBStore) SetIngesting(_ context.Context, id string) error {
	d, ok := f.docs[id]
	if !ok {
		return kb.ErrNotFound
	}
	d.Status = "ingesting"
	f.docs[id] = d
	return nil
}

func (f *fakeKBStore) SetFailed(_ context.Context, id, errMsg string) error {
	d, ok := f.docs[id]
	if !ok {
		return kb.ErrNotFound
	}
	d.Status, d.Error = "failed", errMsg
	f.docs[id] = d
	return nil
}

func (f *fakeKBStore) GetDocument(_ context.Context, id string) (kb.Document, error) {
	d, ok := f.docs[id]
	if !ok {
		return kb.Document{}, kb.ErrNotFound
	}
	return d, nil
}

// fakeIngest stubs kbIngester, optionally erroring for a fixed doc id.
type fakeIngest struct {
	failFor string
	calls   int
}

func (f *fakeIngest) IngestDocument(_ context.Context, documentID, _, _ string) (int, error) {
	f.calls++
	if documentID == f.failFor {
		return 0, errors.New("ingest failed")
	}
	return 1, nil
}

func TestPromoteMissionHappyPath(t *testing.T) {
	opener := &fakeOpener{data: map[string][]byte{"a1": []byte("# Report\n\nbody")}}
	store := newFakeKBStore()
	ingest := &fakeIngest{}
	m := missions.Mission{ID: "m1", ArtifactRefs: []missions.ArtifactRef{{ID: "a1", Mime: "text/plain", Name: "report.md"}}}

	promoted, errs := PromoteMission(t.Context(), opener, store, ingest, m, "col1")
	if promoted != 1 || len(errs) != 0 {
		t.Fatalf("promoted=%d errs=%v, want 1 promoted, no errors", promoted, errs)
	}
	if len(store.docs) != 1 {
		t.Fatalf("documents = %d, want 1", len(store.docs))
	}
	for _, d := range store.docs {
		if d.Provenance != "mission" || d.SourceType != "mission" || d.SourceRef != "mission:m1:report.md" {
			t.Fatalf("document = %+v, want provenance=mission source_type=mission source_ref=mission:m1:report.md", d)
		}
		if d.Status != "ingesting" && d.Status != "ready" {
			// SetIngesting runs before ingest; the fake never advances to
			// ready, so ingesting is the expected terminal state here.
			t.Fatalf("status = %q, want ingesting", d.Status)
		}
	}
	if ingest.calls != 1 {
		t.Fatalf("ingest calls = %d, want 1", ingest.calls)
	}
}

func TestPromoteMissionIgnoresNonMarkdownArtifacts(t *testing.T) {
	opener := &fakeOpener{data: map[string][]byte{"a1": []byte("binary")}}
	store := newFakeKBStore()
	m := missions.Mission{ID: "m1", ArtifactRefs: []missions.ArtifactRef{{ID: "a1", Mime: "application/pdf", Name: "chart.pdf"}}}

	promoted, errs := PromoteMission(t.Context(), opener, store, &fakeIngest{}, m, "col1")
	if promoted != 0 || len(errs) != 0 {
		t.Fatalf("promoted=%d errs=%v, want 0 and no errors (non-markdown skipped silently)", promoted, errs)
	}
	if len(store.docs) != 0 {
		t.Fatalf("documents = %d, want 0", len(store.docs))
	}
}

func TestPromoteMissionIdempotentReplacesContent(t *testing.T) {
	opener := &fakeOpener{data: map[string][]byte{"a1": []byte("v1")}}
	store := newFakeKBStore()
	m := missions.Mission{ID: "m1", ArtifactRefs: []missions.ArtifactRef{{ID: "a1", Mime: "text/plain", Name: "report.md"}}}

	if _, errs := PromoteMission(t.Context(), opener, store, &fakeIngest{}, m, "col1"); len(errs) != 0 {
		t.Fatalf("first promote errs = %v", errs)
	}
	if len(store.docs) != 1 {
		t.Fatalf("documents after first promote = %d, want 1", len(store.docs))
	}

	// Re-promote with updated content: must replace in place, not add a
	// second document.
	opener.data["a1"] = []byte("v2")
	if _, errs := PromoteMission(t.Context(), opener, store, &fakeIngest{}, m, "col1"); len(errs) != 0 {
		t.Fatalf("second promote errs = %v", errs)
	}
	if len(store.docs) != 1 {
		t.Fatalf("documents after re-promote = %d, want 1 (idempotent)", len(store.docs))
	}
	for _, d := range store.docs {
		if d.Markdown != "v2" {
			t.Fatalf("markdown = %q, want v2 (content replaced)", d.Markdown)
		}
	}
}

func TestPromoteMissionCollectsIngestErrors(t *testing.T) {
	opener := &fakeOpener{data: map[string][]byte{"a1": []byte("body")}}
	store := newFakeKBStore()
	m := missions.Mission{ID: "m1", ArtifactRefs: []missions.ArtifactRef{{ID: "a1", Mime: "text/plain", Name: "report.md"}}}

	// failFor targets the first created document id (doc1, per
	// fakeKBStore.CreateDocument's counter).
	promoted, errs := PromoteMission(t.Context(), opener, store, &fakeIngest{failFor: "doc1"}, m, "col1")
	if promoted != 0 || len(errs) != 1 {
		t.Fatalf("promoted=%d errs=%v, want 0 promoted, 1 error", promoted, errs)
	}
	for _, d := range store.docs {
		if d.Status != "failed" {
			t.Fatalf("status = %q, want failed", d.Status)
		}
	}
}

func TestPromoteKBLogsAndNeverReturnsError(t *testing.T) {
	opener := &fakeOpener{data: map[string][]byte{}} // a1 vanished
	store := newFakeKBStore()
	m := missions.Mission{ID: "m1", ArtifactRefs: []missions.ArtifactRef{{ID: "a1", Mime: "text/plain", Name: "report.md"}}}

	// Must not panic: PromoteKB's contract is fire-and-forget, same as
	// CopyArtifacts.
	PromoteKB(opener, store, &fakeIngest{}, slog.Default())(t.Context(), m, "col1")
	if len(store.docs) != 0 {
		t.Fatalf("documents = %d, want 0 (vanished artifact skipped)", len(store.docs))
	}
}
