//go:build integration

package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/router"
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

func TestAvailableModelsProxiesAndFallsBack(t *testing.T) {
	adm, _, _ := testAdmin(t)
	ctx := t.Context()
	srv := stubOpenAI(t)

	id, err := adm.Create(ctx, Provider{
		Name: adminMarker + "models", Kind: "api", Driver: "openaicompat",
		BaseURL: srv.URL, DefaultModel: "m-alpha",
		Models: []router.ModelInfo{{ID: "m-alpha"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	models, err := adm.AvailableModels(ctx, id)
	if err != nil {
		t.Fatalf("AvailableModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "m-alpha" || models[1].ID != "m-beta" {
		t.Fatalf("models = %+v", models)
	}

	// Bedrock cannot enumerate models; the sentinel drives the UI's
	// manual-entry fallback (422 at the HTTP layer).
	bid, err := adm.Create(ctx, Provider{
		Name: adminMarker + "models-bedrock", Kind: "api", Driver: "bedrock",
		BaseURL: "us-east-1",
	})
	if err != nil {
		t.Fatalf("Create bedrock: %v", err)
	}
	if _, err := adm.AvailableModels(ctx, bid); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("bedrock AvailableModels err = %v, want ErrUnsupported", err)
	}
}
