package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/destinations"
	"github.com/SumonMSelim/timothy/internal/brain/gwclient"
)

// fakeGatewaySecrets stubs the gateway round trip so the credentials
// directory's merge logic can be tested without a live gateway.
type fakeGatewaySecrets struct {
	refs       []gwclient.SecretRef
	listErr    error
	deleteErr  error
	deleteCode int
	deletedRef string
}

func (f *fakeGatewaySecrets) ListSecrets(context.Context) ([]gwclient.SecretRef, error) {
	return f.refs, f.listErr
}

func (f *fakeGatewaySecrets) DeleteSecret(_ context.Context, refName string) (int, error) {
	f.deletedRef = refName
	if f.deleteErr != nil {
		return f.deleteCode, f.deleteErr
	}
	return http.StatusNoContent, nil
}

// fakeConnectorLister stubs connectors.Store's List for the same
// reason; no DB needed to test the merge/guard logic.
type fakeConnectorLister struct {
	rows []connectors.Connector
	err  error
}

func (f *fakeConnectorLister) List(context.Context) ([]connectors.Connector, error) {
	return f.rows, f.err
}

// fakeDestinationLister stubs destinations.Store's List for the same
// reason; no DB needed to test the merge/guard logic.
type fakeDestinationLister struct {
	rows []destinations.Destination
	err  error
}

func (f *fakeDestinationLister) List(context.Context) ([]destinations.Destination, error) {
	return f.rows, f.err
}

func TestListSecretsMergesProviderAndConnectorReferents(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	gw := &fakeGatewaySecrets{refs: []gwclient.SecretRef{
		{RefName: "GITHUB_PAT", ReferencedBy: []string{"github-provider"}},
		{RefName: "GITHUB_PAT_SIGNING_KEY"},
		{RefName: "ORPHAN_KEY"},
	}}
	conns := &fakeConnectorLister{rows: []connectors.Connector{
		{Name: "github-mcp", CredentialRef: "GITHUB_PAT"},
		{Name: "github-signed", Kind: "github", CredentialRef: "GITHUB_PAT", Config: json.RawMessage(`{"sign_commits":true,"signing_public_key":"ssh-ed25519 AAAA"}`)},
		{Name: "no-cred", CredentialRef: ""},
	}}
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, gw, conns, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/secrets", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Secrets []secretRefEntry `json:"secrets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Secrets) != 3 {
		t.Fatalf("secrets = %+v, want 3 entries", body.Secrets)
	}
	byName := map[string]secretRefEntry{}
	for _, s := range body.Secrets {
		byName[s.RefName] = s
	}
	got := byName["GITHUB_PAT"].ReferencedBy
	if len(got) != 3 {
		t.Fatalf("GITHUB_PAT referenced_by = %+v, want provider + 2 connectors", got)
	}
	want := map[referenceInfo]bool{
		{Kind: "provider", Name: "github-provider", Role: "credential"}: true,
		{Kind: "connector", Name: "github-mcp", Role: "credential"}:     true,
		{Kind: "connector", Name: "github-signed", Role: "credential"}:  true,
	}
	for _, r := range got {
		if !want[r] {
			t.Fatalf("unexpected referent %+v in %+v", r, got)
		}
	}
	signKey := byName["GITHUB_PAT_SIGNING_KEY"].ReferencedBy
	if len(signKey) != 1 || signKey[0] != (referenceInfo{Kind: "connector", Name: "github-signed", Role: "signing_key"}) {
		t.Fatalf("GITHUB_PAT_SIGNING_KEY referenced_by = %+v, want the signing connector with role signing_key (derived ref must never look orphaned)", signKey)
	}
	if len(byName["ORPHAN_KEY"].ReferencedBy) != 0 {
		t.Fatalf("ORPHAN_KEY referenced_by = %+v, want empty", byName["ORPHAN_KEY"].ReferencedBy)
	}
	// An orphaned ref must serialize referenced_by as [], never null —
	// the frontend indexes it unconditionally, and null once crashed the
	// credential picker.
	if !strings.Contains(w.Body.String(), `"referenced_by":[]`) {
		t.Fatalf("body = %s, want orphaned referenced_by serialized as []", w.Body.String())
	}
}

func TestListSecretsPropagatesGatewayFailure(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	gw := &fakeGatewaySecrets{listErr: errors.New("gateway down")}
	conns := &fakeConnectorLister{}
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, gw, conns, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/secrets", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestListSecretsCountsGoogleClientSecretRef(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	gw := &fakeGatewaySecrets{refs: []gwclient.SecretRef{
		{RefName: "GMAIL_GOOGLE_OAUTH"},
		{RefName: "GMAIL_GOOGLE_CLIENT_SECRET"},
	}}
	conns := &fakeConnectorLister{rows: []connectors.Connector{
		//nolint:gosec // G101: fixture ref names, not credential values.
		{Name: "gmail", Kind: "google", CredentialRef: "GMAIL_GOOGLE_OAUTH",
			Config: json.RawMessage(`{"client_id":"x.apps.googleusercontent.com","client_secret_ref":"GMAIL_GOOGLE_CLIENT_SECRET"}`)},
	}}
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, gw, conns, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/secrets", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Secrets []secretRefEntry `json:"secrets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byName := map[string]secretRefEntry{}
	for _, s := range body.Secrets {
		byName[s.RefName] = s
	}
	got := byName["GMAIL_GOOGLE_CLIENT_SECRET"].ReferencedBy
	if len(got) != 1 || got[0] != (referenceInfo{Kind: "connector", Name: "gmail", Role: "client_secret"}) {
		t.Fatalf("GMAIL_GOOGLE_CLIENT_SECRET referenced_by = %+v, want the google connector with role client_secret (client secret is resolved on every token refresh, must never look orphaned)", got)
	}
	oauth := byName["GMAIL_GOOGLE_OAUTH"].ReferencedBy
	if len(oauth) != 1 || oauth[0] != (referenceInfo{Kind: "connector", Name: "gmail", Role: "oauth_tokens"}) {
		t.Fatalf("GMAIL_GOOGLE_OAUTH referenced_by = %+v, want the google connector with role oauth_tokens (machine-managed token bundle, never a manual pick)", oauth)
	}
}

func TestDeleteSecretRefusesGoogleClientSecretRef(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	gw := &fakeGatewaySecrets{}
	conns := &fakeConnectorLister{rows: []connectors.Connector{
		//nolint:gosec // G101: fixture ref names, not credential values.
		{Name: "gmail", Kind: "google", CredentialRef: "GMAIL_GOOGLE_OAUTH",
			Config: json.RawMessage(`{"client_secret_ref":"GMAIL_GOOGLE_CLIENT_SECRET"}`)},
	}}
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, gw, conns, nil)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/secrets/GMAIL_GOOGLE_CLIENT_SECRET", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if gw.deletedRef != "" {
		t.Fatalf("gateway DeleteSecret called with %q, want never called", gw.deletedRef)
	}
}

func TestDeleteSecretRefusesWhenConnectorReferencesIt(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	gw := &fakeGatewaySecrets{}
	conns := &fakeConnectorLister{rows: []connectors.Connector{
		{Name: "github-mcp", CredentialRef: "GITHUB_PAT"},
	}}
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, gw, conns, nil)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/secrets/GITHUB_PAT", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if gw.deletedRef != "" {
		t.Fatalf("gateway DeleteSecret called with %q, want never called (connector guard must short-circuit)", gw.deletedRef)
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Message == "" {
		t.Fatal("error message empty, want it to list the referencing connector")
	}
}

func TestDeleteSecretForwardsOrphanedRefToGateway(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	gw := &fakeGatewaySecrets{}
	conns := &fakeConnectorLister{}
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, gw, conns, nil)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/secrets/ORPHAN_KEY", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if gw.deletedRef != "ORPHAN_KEY" {
		t.Fatalf("gateway DeleteSecret called with %q, want ORPHAN_KEY", gw.deletedRef)
	}
}

// TestDeleteSecretPropagatesGatewayInUseRefusal covers the other half
// of the split guard: a provider-only reference is invisible to
// brain's connector check, so the gateway's own 409 must still surface
// unchanged.
func TestDeleteSecretPropagatesGatewayInUseRefusal(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	gw := &fakeGatewaySecrets{deleteErr: errors.New("SOME_KEY is referenced by provider(s) [openai]: in use"), deleteCode: http.StatusConflict}
	conns := &fakeConnectorLister{}
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, gw, conns, nil)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/secrets/SOME_KEY", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 from the gateway's own refusal", w.Code)
	}
}

// TestSecretsRoutesCoexistWithAdminProxy pins the boot path that once
// panicked: registerAdmin's proxied patterns and registerSecrets'
// local ones share the /v1/admin/secrets namespace, and net/http's
// ServeMux panics on any duplicate — both must register side by side.
func TestSecretsRoutesCoexistWithAdminProxy(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	m := http.NewServeMux()
	a.registerAdmin(m.Handle, http.NotFoundHandler())
	a.registerSecrets(m.Handle, &fakeGatewaySecrets{}, &fakeConnectorLister{}, nil)
}

// TestRegisterSecretsMountsWithoutConnectors pins that a nil connector
// lister (connectors disabled) still serves DELETE — it has no proxied
// fallback, so unmounting it here would remove secret deletion
// entirely; only the connector-reference guard drops out.
func TestRegisterSecretsMountsWithoutConnectors(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	gw := &fakeGatewaySecrets{}
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, gw, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/secrets/SOME_KEY", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (delete must survive disabled connectors)", w.Code)
	}
	if gw.deletedRef != "SOME_KEY" {
		t.Fatalf("gateway DeleteSecret called with %q, want SOME_KEY", gw.deletedRef)
	}
}

func TestRegisterSecretsUnmountedWithoutGatewayOrConnectors(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/secrets", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (route unmounted)", w.Code)
	}
}

// TestListSecretsIncludesDestinationReferent pins the bug this test
// file was extended for: a telegram destination's credential_ref must
// never look orphaned.
func TestListSecretsIncludesDestinationReferent(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	gw := &fakeGatewaySecrets{refs: []gwclient.SecretRef{
		{RefName: "TELEGRAM_DEST_REF"},
	}}
	dests := &fakeDestinationLister{rows: []destinations.Destination{
		{Name: "alerts-bot", Kind: "telegram", CredentialRef: "TELEGRAM_DEST_REF"}, //nolint:gosec // ref name, not a secret value
		{Name: "webhook-sink", Kind: "webhook", CredentialRef: ""},
	}}
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, gw, nil, dests)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/secrets", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Secrets []secretRefEntry `json:"secrets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Secrets) != 1 {
		t.Fatalf("secrets = %+v, want 1 entry", body.Secrets)
	}
	got := body.Secrets[0].ReferencedBy
	want := referenceInfo{Kind: "destination", Name: "alerts-bot", Role: "credential"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("TELEGRAM_DEST_REF referenced_by = %+v, want [%+v] (never orphaned)", got, want)
	}
}

// TestDeleteSecretRefusesWhenDestinationReferencesIt mirrors the
// connector-guard test: a destination-only reference must refuse
// deletion without ever asking the gateway.
func TestDeleteSecretRefusesWhenDestinationReferencesIt(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	gw := &fakeGatewaySecrets{}
	dests := &fakeDestinationLister{rows: []destinations.Destination{
		{Name: "alerts-bot", Kind: "telegram", CredentialRef: "TELEGRAM_DEST_REF"}, //nolint:gosec // ref name, not a secret value
	}}
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, gw, nil, dests)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/secrets/TELEGRAM_DEST_REF", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if gw.deletedRef != "" {
		t.Fatalf("gateway DeleteSecret called with %q, want never called (destination guard must short-circuit)", gw.deletedRef)
	}
	var respBody struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body := respBody.Message; body == "" {
		t.Fatal("error message empty, want it to name the referencing destination")
	}
}

// TestListSecretsSkipsDestinationsWithoutCredentialRef pins that
// email/webhook destinations (empty credential_ref) never contribute a
// spurious referent.
func TestListSecretsSkipsDestinationsWithoutCredentialRef(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	gw := &fakeGatewaySecrets{refs: []gwclient.SecretRef{
		{RefName: "ORPHAN_KEY"},
	}}
	dests := &fakeDestinationLister{rows: []destinations.Destination{
		{Name: "webhook-sink", Kind: "webhook", CredentialRef: ""},
		{Name: "email-out", Kind: "email", CredentialRef: ""},
	}}
	m := http.NewServeMux()
	a.registerSecrets(m.Handle, gw, nil, dests)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/secrets", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Secrets []secretRefEntry `json:"secrets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Secrets) != 1 || len(body.Secrets[0].ReferencedBy) != 0 {
		t.Fatalf("secrets = %+v, want ORPHAN_KEY with empty referenced_by", body.Secrets)
	}
}
