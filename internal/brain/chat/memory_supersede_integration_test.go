//go:build integration

package chat

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/memory/extract"
	"github.com/SumonMSelim/timothy/internal/memory/store"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

type supersedeSmokeGateway struct {
	embedding store.Vector
}

func (supersedeSmokeGateway) RouteForRole(_ context.Context, role string) (string, bool, error) {
	return role, true, nil
}

func (supersedeSmokeGateway) Stream(_ context.Context, req gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	text := "I moved to Berlin."
	if req.Purpose == "memory-extract" {
		text = `[{"type":"semantic","content":"User lives in Berlin.","entities":[],"confidence":0.9,"changes_behavior":true}]`
	}
	ch := make(chan stream.StreamEvent, 2)
	ch <- stream.StreamEvent{Type: stream.EventChunk, Text: text}
	ch <- stream.StreamEvent{Type: stream.EventDone}
	close(ch)
	return ch, nil
}

func (g supersedeSmokeGateway) Embed(_ context.Context, texts []string, _ string) ([][]float32, string, error) {
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = append([]float32(nil), g.embedding...)
	}
	return vectors, "deterministic-smoke", nil
}

func TestChatMoveCreatesPendingSupersedeCandidate(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, logger)
	migrateCtx, migrateCancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer migrateCancel()
	if err := pool.WaitHealthy(migrateCtx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(migrateCtx, db, migrations.FS, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const sessionID = "00000000-0000-0000-0000-000000000873"
	memories := store.New(pool, logger)
	embedding := make(store.Vector, 1024)
	seed := time.Now().UnixNano()
	first := int(seed % int64(len(embedding)))
	second := int((seed >> 10) % int64(len(embedding)))
	if second == first {
		second = (second + 1) % len(embedding)
	}
	embedding[first], embedding[second] = 1, 2
	old := store.Memory{
		Type: store.TypeSemantic, Content: "User lives in Amsterdam.",
		Embedding: embedding, Actor: store.ActorUser, Confidence: 1,
	}
	oldID, err := memories.Insert(t.Context(), old)
	if err != nil {
		t.Fatalf("Insert old fact: %v", err)
	}
	var candidateID string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if candidateID != "" {
			_, _ = db.Exec(cleanupCtx, `DELETE FROM memories WHERE id = $1`, candidateID)
		}
		_, _ = db.Exec(cleanupCtx, `DELETE FROM memories WHERE id = $1`, oldID)
	})

	gateway := supersedeSmokeGateway{embedding: embedding}
	service := newService(gateway, newFakeLog())
	service.SetMemoryRetrieve(func(context.Context, string, string) MemoryRecall {
		return MemoryRecall{
			Block:    "<memory source=\"timothy-memory\" trust=\"data\">\n- [semantic] " + old.Content + "\n</memory>",
			Contents: []string{old.Content},
		}
	})
	extractor := extract.New(gateway, memories, logger)
	extracted := make(chan struct {
		ids []string
		err error
	}, 1)
	service.SetMemoryExtract(func(ctx context.Context, id string, seq int64, text, _ string, deny []string) {
		ids, err := extractor.Extract(ctx, extract.Request{
			SessionID: id, SourceSeq: seq, Text: text, Source: "chat", Deny: deny,
		})
		extracted <- struct {
			ids []string
			err error
		}{ids: ids, err: err}
	})

	_, events, err := service.Chat(t.Context(), Request{SessionID: sessionID, Message: "I moved to Berlin."})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drain(t, events)
	select {
	case result := <-extracted:
		if result.err != nil {
			t.Fatalf("Extract: %v", result.err)
		}
		if len(result.ids) != 1 {
			t.Fatalf("Extract ids = %v, want one queue candidate", result.ids)
		}
		candidateID = result.ids[0]
	case <-time.After(10 * time.Second):
		t.Fatal("chat memory extraction did not finish")
	}

	gotOld, err := memories.Get(t.Context(), oldID)
	if err != nil {
		t.Fatalf("Get old fact: %v", err)
	}
	gotCandidate, err := memories.Get(t.Context(), candidateID)
	if err != nil {
		t.Fatalf("Get candidate: %v", err)
	}
	if gotOld.Status != store.StatusActive || gotOld.SupersededBy != "" {
		t.Fatalf("old fact = %+v, want active until review", gotOld)
	}
	if gotCandidate.Status != store.StatusPending || gotCandidate.Content != "User lives in Berlin." || gotCandidate.Supersedes != oldID {
		t.Fatalf("candidate = %+v, want pending Berlin correction superseding %s", gotCandidate, oldID)
	}
	queued, err := memories.ListByStatus(t.Context(), store.StatusPending, store.Page{})
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	found := false
	for _, item := range queued {
		if item.ID == candidateID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("pending queue does not contain candidate %s", candidateID)
	}
}
