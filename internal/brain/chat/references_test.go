package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// fakeMissionStore is an in-memory MissionStore.
type fakeMissionStore struct {
	missions map[string]missions.Mission
	events   map[string][]missions.Event
}

func (f *fakeMissionStore) Get(_ context.Context, id string) (missions.Mission, error) {
	m, ok := f.missions[id]
	if !ok {
		return missions.Mission{}, errors.New("not found")
	}
	return m, nil
}

func (f *fakeMissionStore) Events(_ context.Context, id string) ([]missions.Event, error) {
	return f.events[id], nil
}

// fakeKBDocStore is an in-memory KBDocStore.
type fakeKBDocStore struct {
	docs map[string]kb.Document
}

func (f *fakeKBDocStore) GetDocument(_ context.Context, id string) (kb.Document, error) {
	d, ok := f.docs[id]
	if !ok {
		return kb.Document{}, errors.New("not found")
	}
	return d, nil
}

func TestResolveReferencesMission(t *testing.T) {
	svc := newService(&fakeGW{}, newFakeLog())
	svc.SetMissionStore(&fakeMissionStore{
		missions: map[string]missions.Mission{
			"m1": {ID: "m1", Goal: "fix the login bug", Name: "Fix login", Kind: "coding", Phase: missions.PhaseDone},
		},
	})

	docs, err := svc.ResolveReferences(context.Background(), []Reference{{Kind: ReferenceKindMission, ID: "m1"}})
	if err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	if docs[0].Name != "Fix login" {
		t.Fatalf("Name = %q, want %q", docs[0].Name, "Fix login")
	}
	if !strings.Contains(docs[0].Markdown, "fix the login bug") {
		t.Fatalf("Markdown = %q, want it to mention the mission goal", docs[0].Markdown)
	}
}

func TestResolveReferencesMissionMissingID(t *testing.T) {
	svc := newService(&fakeGW{}, newFakeLog())
	svc.SetMissionStore(&fakeMissionStore{missions: map[string]missions.Mission{}})

	docs, err := svc.ResolveReferences(context.Background(), []Reference{{Kind: ReferenceKindMission, ID: "missing"}})
	if err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("got %d docs, want 0 (missing id skipped, not rejected)", len(docs))
	}
}

func TestResolveReferencesMissionStoreUnwired(t *testing.T) {
	svc := newService(&fakeGW{}, newFakeLog()) // SetMissionStore never called

	docs, err := svc.ResolveReferences(context.Background(), []Reference{{Kind: ReferenceKindMission, ID: "m1"}})
	if err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("got %d docs, want 0 (nil store skips silently)", len(docs))
	}
}

func TestResolveReferencesSession(t *testing.T) {
	log := newFakeLog()
	svc := newService(&fakeGW{}, log)

	sessionID := "sess-1"
	if err := log.SetTitleIfEmpty(context.Background(), sessionID, "My chat"); err != nil {
		t.Fatalf("SetTitleIfEmpty: %v", err)
	}
	if _, err := log.Append(context.Background(), sessionID, "user_message", map[string]any{"text": "hello there"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	docs, err := svc.ResolveReferences(context.Background(), []Reference{{Kind: ReferenceKindSession, ID: sessionID}})
	if err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	if docs[0].Name != "My chat" {
		t.Fatalf("Name = %q, want %q", docs[0].Name, "My chat")
	}
	if !strings.Contains(docs[0].Markdown, "hello there") {
		t.Fatalf("Markdown = %q, want it to mention the session's message", docs[0].Markdown)
	}
}

func TestResolveReferencesKBDoc(t *testing.T) {
	svc := newService(&fakeGW{}, newFakeLog())
	svc.SetKBDocStore(&fakeKBDocStore{docs: map[string]kb.Document{
		"d1": {ID: "d1", Title: "Runbook", Markdown: "do the thing"},
	}})

	docs, err := svc.ResolveReferences(context.Background(), []Reference{{Kind: ReferenceKindKBDoc, ID: "d1"}})
	if err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	if docs[0].Name != "Runbook" || docs[0].Markdown != "do the thing" {
		t.Fatalf("doc = %+v, want title/markdown to round-trip", docs[0])
	}
}

func TestResolveReferencesKBDocStoreUnwired(t *testing.T) {
	svc := newService(&fakeGW{}, newFakeLog()) // SetKBDocStore never called

	docs, err := svc.ResolveReferences(context.Background(), []Reference{{Kind: ReferenceKindKBDoc, ID: "d1"}})
	if err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("got %d docs, want 0 (nil store skips silently)", len(docs))
	}
}

func TestResolveReferencesUnknownKindSkipped(t *testing.T) {
	svc := newService(&fakeGW{}, newFakeLog())

	docs, err := svc.ResolveReferences(context.Background(), []Reference{{Kind: "bogus", ID: "x"}})
	if err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("got %d docs, want 0 (unknown kind skipped)", len(docs))
	}
}

func TestResolveReferencesOverCapRejected(t *testing.T) {
	svc := newService(&fakeGW{}, newFakeLog())

	refs := make([]Reference, maxReferences+1)
	for i := range refs {
		refs[i] = Reference{Kind: ReferenceKindKBDoc, ID: "d1"}
	}
	if _, err := svc.ResolveReferences(context.Background(), refs); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("ResolveReferences over cap = %v, want ErrBadRequest", err)
	}
}

func TestResolveReferencesEmpty(t *testing.T) {
	svc := newService(&fakeGW{}, newFakeLog())

	docs, err := svc.ResolveReferences(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}
	if docs != nil {
		t.Fatalf("docs = %v, want nil for no references", docs)
	}
}
