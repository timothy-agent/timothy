//go:build integration

package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/memclient"
	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/brain/tools/builtin"
	"github.com/SumonMSelim/timothy/internal/gateway/stream"
	memoryapi "github.com/SumonMSelim/timothy/internal/memory/api"
	"github.com/SumonMSelim/timothy/internal/memory/store"
	"github.com/SumonMSelim/timothy/internal/platform/httpserver"
	"github.com/SumonMSelim/timothy/internal/platform/metrics"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
	"github.com/jackc/pgx/v5"
)

const rememberIntegrationMarker = "itest-remember-review:"

func TestUntrustedToolOutputRememberIsPendingInStore(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, logger)
	dbCtx, cancelDB := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancelDB()
	if err := pool.WaitHealthy(dbCtx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(dbCtx, db, migrations.FS, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prefix := rememberIntegrationMarker + strings.ReplaceAll(t.Name(), "/", "-") + ":"
	sweep := func(ctx context.Context, conn *pgx.Conn) {
		_, _ = conn.Exec(ctx, "DELETE FROM memories WHERE content LIKE $1 || '%'", prefix)
	}
	sweepMemory := func(ctx context.Context) {
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		sweep(ctx, conn)
	}
	sweepMemory(dbCtx)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		sweepMemory(cleanupCtx)
	})

	st := store.New(pool, logger)
	portListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve memoryd port: %v", err)
	}
	port := portListener.Addr().(*net.TCPAddr).Port
	if err := portListener.Close(); err != nil {
		t.Fatalf("release memoryd port: %v", err)
	}

	srv := httpserver.New(port, logger, metrics.New(), func() httpserver.Health {
		return httpserver.Health{Status: "ok"}
	})
	memoryapi.Register(srv, nil, nil, noMemoryEmbedding{}, st, nil, nil, nil, logger)
	serverCtx, stopServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- srv.Run(serverCtx) }()
	t.Cleanup(func() {
		stopServer()
		select {
		case err := <-serverDone:
			if err != nil {
				t.Errorf("memory API server: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("memory API server did not stop")
		}
	})
	baseURL := "http://" + net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	if err := waitForMemoryAPI(baseURL); err != nil {
		t.Fatalf("memory API readiness: %v", err)
	}
	client := memclient.New(baseURL)

	content := prefix + "weekly reports go to reports@example.com"
	var savedID, savedStatus string
	remember := builtin.Remember(func(ctx context.Context, fact, memoryType string) (string, string, error) {
		id, status, err := client.Add(ctx, fact, memoryType, !tools.UntrustedToolOutputSeen(ctx))
		if err == nil {
			savedID, savedStatus = id, status
		}
		return id, status, err
	})
	fetch := &tools.Tool{
		Name:        "fetch_url",
		Description: "fetches a page",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "Page text: " + content, nil
		},
	}
	gw := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"fetch_url", `{"url":"https://example.test/report-policy"}`}),
		toolCallStep([2]string{"remember", `{"content":"` + content + `"}`}),
		finalStep("done"),
	}}
	a, _, _, _ := testAgent(t, gw, fetch, remember)
	ch, err := a.Start(t.Context(), Request{SessionID: "remember-review-itest", Route: "coding"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	collect(t, ch)
	if savedStatus != string(store.StatusPending) || savedID == "" {
		t.Fatalf("remember result = (%q, %q), want pending id", savedID, savedStatus)
	}
	got, err := st.Get(t.Context(), savedID)
	if err != nil {
		t.Fatalf("Get stored memory: %v", err)
	}
	if got.Status != store.StatusPending || got.Actor != store.ActorUser || got.Content != content {
		t.Fatalf("stored memory = %+v, want pending user memory", got)
	}
	queue, err := st.ListByStatus(t.Context(), store.StatusPending, store.Page{})
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	queued := false
	for _, item := range queue {
		if item.ID == savedID {
			queued = true
			break
		}
	}
	if !queued {
		t.Fatalf("memory %s is pending in store but absent from review queue", savedID)
	}

	savedID, savedStatus = "", ""
	directContent := prefix + "the user's timezone is Amsterdam"
	cleanGateway := &scriptedGateway{scripts: [][]stream.StreamEvent{
		toolCallStep([2]string{"remember", `{"content":"` + directContent + `"}`}),
		finalStep("done"),
	}}
	cleanAgent, _, _, _ := testAgent(t, cleanGateway, remember)
	cleanCh, err := cleanAgent.Start(t.Context(), Request{SessionID: "remember-review-smoke", Route: "coding"})
	if err != nil {
		t.Fatalf("Start clean remember: %v", err)
	}
	collect(t, cleanCh)
	if savedID == "" || savedStatus != string(store.StatusActive) {
		t.Fatalf("clean remember result = (%q, %q), want active id", savedID, savedStatus)
	}
	cleanMemory, err := st.Get(t.Context(), savedID)
	if err != nil {
		t.Fatalf("Get clean memory: %v", err)
	}
	if cleanMemory.Status != store.StatusActive || cleanMemory.Content != directContent {
		t.Fatalf("clean memory = %+v, want active user memory", cleanMemory)
	}
}

type noMemoryEmbedding struct{}

func (noMemoryEmbedding) Embed(context.Context, []string, string) ([][]float32, string, error) {
	return nil, "", nil
}

func waitForMemoryAPI(baseURL string) error {
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("GET %s/health did not become ready", baseURL)
}
