//go:build integration

package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
)

// stubOpenAI serves the two endpoints the admin layer touches: a
// minimal chat/completions SSE stream and a /models listing.
func stubOpenAI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"pong"}}]}`)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"m-alpha"},{"id":"m-beta"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestValidateProbesWithoutPersisting(t *testing.T) {
	adm, _, pool := testAdmin(t)
	ctx := t.Context()
	srv := stubOpenAI(t)
	name := adminMarker + "validate"

	res, err := adm.Validate(ctx, Provider{
		Name: name, Kind: "api", Driver: "openaicompat", BaseURL: srv.URL,
	}, "m-alpha")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.OK || res.Model != "m-alpha" {
		t.Fatalf("Validate = %+v, want ok probe of m-alpha", res)
	}

	// Nothing persisted: validation must leave no provider row behind.
	db, _ := pool.Get()
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM providers WHERE name = $1`, name).Scan(&count); err != nil {
		t.Fatalf("providers count: %v", err)
	}
	if count != 0 {
		t.Fatalf("validate persisted %d provider row(s)", count)
	}

	// The probe books as purpose='test' like Test does.
	var purpose string
	if err := db.QueryRow(ctx, `SELECT COALESCE(purpose, '') FROM cost_ledger
		WHERE provider = $1 ORDER BY ts DESC LIMIT 1`, name).Scan(&purpose); err != nil {
		t.Fatalf("ledger row: %v", err)
	}
	if purpose != "test" {
		t.Fatalf("probe purpose = %q, want test", purpose)
	}

	// A dead endpoint fails honestly inside the result, not as an error.
	res, err = adm.Validate(ctx, Provider{
		Name: name, Kind: "api", Driver: "openaicompat", BaseURL: "http://127.0.0.1:1/v1",
	}, "m-alpha")
	if err != nil {
		t.Fatalf("Validate dead endpoint: %v", err)
	}
	if res.OK || res.Detail == "" {
		t.Fatalf("dead endpoint = %+v, want honest failure", res)
	}

	// Config errors reject before any probe.
	if _, err := adm.Validate(ctx, Provider{Name: name, Kind: "api", Driver: "nope"}, "m"); err == nil {
		t.Fatal("unknown driver accepted")
	}
	if _, err := adm.Validate(ctx, Provider{Name: name, Kind: "api", Driver: "openaicompat", BaseURL: srv.URL}, ""); err == nil {
		t.Fatal("missing model accepted")
	}
}

func TestConnectionProbePricesACostedModel(t *testing.T) {
	ctx := t.Context()
	srv := stubOpenAI(t)
	name := adminMarker + "priced-probe"

	// Test's cost probe prices via the router snapshot (Snapshot.Prices),
	// which reads router.Store's own catalog reference set at
	// construction — swapping admin.catalog after the fact (as
	// CatalogPrices-only tests do) never reaches it. Seed the shared
	// catalog before building admin/store. stubOpenAI's httptest server
	// binds 127.0.0.1, which candidatesForHost restricts to
	// litellm_provider "ollama" — match that so the seeded model is
	// actually a candidate.
	catSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"m-alpha": {"litellm_provider": "ollama", "mode": "chat",
			"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002}}`))
	}))
	defer catSrv.Close()
	cat := catalog.NewWithURL(slog.New(slog.NewTextHandler(io.Discard, nil)), catSrv.URL)
	if _, err := cat.Sync(ctx); err != nil {
		t.Fatalf("catalog sync: %v", err)
	}
	adm, _, pool := testAdminWithCatalog(t, cat)

	id, err := adm.Create(ctx, Provider{
		Name: name, Kind: "api", Driver: "openaicompat", BaseURL: srv.URL,
		DefaultModel: "m-alpha",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		if _, err := conn.Exec(cctx, "DELETE FROM admin_audit WHERE entity_id = $1", id); err != nil {
			t.Errorf("cleanup probe audit rows: %v", err)
		}
	})

	res, err := adm.Test(ctx, id, "m-alpha")
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !res.OK {
		t.Fatalf("Test = %+v, want ok", res)
	}

	// stubOpenAI reports 2 prompt + 1 completion token; at $1/$2 per
	// million that's (2*1 + 1*2) / 1e6.
	const want = (2*1.0 + 1*2.0) / 1_000_000.0
	db, _ := pool.Get()
	var got float64
	if err := db.QueryRow(ctx, `SELECT cost FROM cost_ledger
		WHERE provider = $1 ORDER BY ts DESC LIMIT 1`, name).Scan(&got); err != nil {
		t.Fatalf("ledger row: %v", err)
	}
	if got != want {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestAvailableModelsProxiesAndFallsBack(t *testing.T) {
	adm, store, _ := testAdmin(t)
	ctx := t.Context()
	srv := stubOpenAI(t)

	name := adminMarker + "models"
	id, err := adm.Create(ctx, Provider{
		Name: name, Kind: "api", Driver: "openaicompat",
		BaseURL: srv.URL, DefaultModel: "m-alpha",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	waitSnapshot(t, store, name)
	models, err := adm.AvailableModels(ctx, id)
	if err != nil {
		t.Fatalf("AvailableModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "m-alpha" || models[1].ID != "m-beta" {
		t.Fatalf("models = %+v", models)
	}

	// Bedrock cannot enumerate models; the sentinel drives the UI's
	// manual-entry fallback (422 at the HTTP layer). Static keys are the
	// only supported auth (D-048), so the credential_ref must resolve
	// for the provider to build and reach the serving snapshot at all.
	bedrockRef := adminMarker + "models-bedrock-key" //nolint:gosec // test fixture name, not a secret
	if err := adm.SetSecret(ctx, bedrockRef, `{"access_key_id":"AKIA123","secret_access_key":"shh"}`); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	bedrockName := adminMarker + "models-bedrock"
	bid, err := adm.Create(ctx, Provider{
		Name: bedrockName, Kind: "api", Driver: "bedrock",
		CredentialRef: bedrockRef, Options: map[string]string{"region": "us-east-1"},
	})
	if err != nil {
		t.Fatalf("Create bedrock: %v", err)
	}
	waitSnapshot(t, store, bedrockName)
	if _, err := adm.AvailableModels(ctx, bid); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("bedrock AvailableModels err = %v, want ErrUnsupported", err)
	}
}
