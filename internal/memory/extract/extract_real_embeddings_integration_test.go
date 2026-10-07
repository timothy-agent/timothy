//go:build integration && real_embeddings

package extract

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	"github.com/SumonMSelim/timothy/internal/memory/store"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

type realEmbeddingGateway struct {
	client *gwclient.Client
	reply  string
}

func (g *realEmbeddingGateway) Stream(_ context.Context, _ gwclient.StreamRequest) (<-chan stream.StreamEvent, error) {
	ch := make(chan stream.StreamEvent, 2)
	ch <- stream.StreamEvent{Type: stream.EventChunk, Text: g.reply}
	ch <- stream.StreamEvent{Type: stream.EventDone}
	close(ch)
	return ch, nil
}

func (g *realEmbeddingGateway) Embed(ctx context.Context, texts []string, purpose string) ([][]float32, string, error) {
	return g.client.Embed(ctx, texts, purpose)
}

func TestExtractAmsterdamBerlinWithRealEmbeddings(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Fatal("DATABASE_URL is required with the real_embeddings build tag")
	}
	gatewayURL := os.Getenv("GATEWAY_URL")
	if gatewayURL == "" {
		t.Fatal("GATEWAY_URL is required with the real_embeddings build tag")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(ctx, dsn, logger)
	waitCtx, waitCancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer waitCancel()
	if err := pool.WaitHealthy(waitCtx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(waitCtx, db, migrations.FS, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	client := gwclient.New(gatewayURL)
	ctx, requestCancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer requestCancel()
	texts := []string{"User lives in Amsterdam.", "User lives in Berlin."}
	vecs, model, err := client.Embed(ctx, texts, "memory-extract")
	if err != nil {
		t.Fatalf("real embeddings: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("embedding count = %d, want 2", len(vecs))
	}
	similarity := cosineSimilarity(store.Vector(vecs[0]), store.Vector(vecs[1]))
	if similarity < NearDupSimilarity {
		t.Fatalf("Amsterdam/Berlin cosine = %.4f from %s, below %.2f", similarity, model, NearDupSimilarity)
	}

	marker := fmt.Sprintf("real-embedding-%d", time.Now().UnixNano())
	old := store.Memory{
		Type: store.TypeSemantic, Content: marker + " " + texts[0],
		Embedding: store.Vector(vecs[0]), Actor: store.ActorUser, Confidence: 1,
	}
	memoryStore := store.New(pool, logger)
	oldID, err := memoryStore.Insert(ctx, old)
	if err != nil {
		t.Fatalf("Insert old memory: %v", err)
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

	gw := &realEmbeddingGateway{
		client: client,
		reply:  `[{"type":"semantic","content":"User lives in Berlin.","entities":[],"confidence":0.9,"changes_behavior":true}]`,
	}
	ids, err := New(gw, memoryStore, logger).Extract(ctx, Request{Text: "I moved to Berlin."})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("Extract ids = %v, want one pending correction", ids)
	}
	candidateID = ids[0]
	gotOld, err := memoryStore.Get(ctx, oldID)
	if err != nil {
		t.Fatalf("Get old memory: %v", err)
	}
	gotCandidate, err := memoryStore.Get(ctx, candidateID)
	if err != nil {
		t.Fatalf("Get correction: %v", err)
	}
	if gotOld.Status != store.StatusActive || gotOld.SupersededBy != "" {
		t.Fatalf("old memory = %+v, want active until user confirmation", gotOld)
	}
	if gotCandidate.Status != store.StatusPending || gotCandidate.Supersedes != oldID {
		t.Fatalf("candidate = %+v, want pending superseding %s", gotCandidate, oldID)
	}
}
