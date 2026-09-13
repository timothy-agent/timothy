package connectors

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gcpTestKeyPEM is one RSA key reused across the package's gcp tests:
// generating a 2048-bit key is the slowest thing these tests do, and
// none of them care which key it is.
var gcpTestKeyPEM = func() (string, *rsa.PrivateKey) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), key
}

var gcpTestPEM, gcpTestKey = gcpTestKeyPEM()

// gcpSAKey renders a service-account key JSON pointing its token_uri
// at tokenURI.
func gcpSAKey(t *testing.T, tokenURI string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   "demo-project",
		"client_email": "timothy@demo-project.iam.gserviceaccount.com",
		"private_key":  gcpTestPEM,
		"token_uri":    tokenURI,
	})
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return string(raw)
}

// fakeGCP is a fake Google token endpoint plus Storage/BigQuery API,
// recording the assertions and request bodies it saw.
type fakeGCP struct {
	assertions []string
	bodies     []string
	tokenCode  int
	apiCode    int
	apiBody    string
}

func (f *fakeGCP) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		f.assertions = append(f.assertions, r.Form.Get("assertion"))
		if f.tokenCode != 0 && f.tokenCode != http.StatusOK {
			w.WriteHeader(f.tokenCode)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"ya29.fake","expires_in":3600}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.bodies = append(f.bodies, string(body))
		//nolint:gosec // G101: fake test token, not a real credential.
		if r.Header.Get("Authorization") != "Bearer ya29.fake" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"no token"}}`))
			return
		}
		if f.apiCode != 0 && f.apiCode != http.StatusOK {
			w.WriteHeader(f.apiCode)
		}
		_, _ = w.Write([]byte(f.apiBody))
	})
	return mux
}

// buildGCP builds a gcp source with its token endpoint and API bases
// pointed at a fake server.
func buildGCP(t *testing.T, f *fakeGCP, config string) (*gcpSource, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	if config == "" {
		config = "{}"
	}
	//nolint:gosec // G101: CredentialRef is a ref NAME, not a credential value.
	src, err := GCPBuilder(srv.Client())(t.Context(), Connector{
		Name: "gcp", Kind: "gcp",
		Config:        json.RawMessage(config),
		CredentialRef: "GCP_SA_KEY",
	}, func(context.Context, string) (string, error) { return gcpSAKey(t, srv.URL+"/token"), nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	gcp := src.(*gcpSource)
	gcp.storageBase, gcp.bigQueryBase = srv.URL+"/storage/v1", srv.URL+"/bigquery/v2"
	return gcp, srv
}

// TestGCPBuilderRejectsBadConfig covers every build-time failure and
// pins that no key material reaches the error text.
func TestGCPBuilderRejectsBadConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  string
		ref     string
		secret  string
		wantErr string
	}{
		{
			name: "missing credential_ref", config: "{}", ref: "",
			wantErr: "credential_ref is required",
		},
		{
			name: "config is not json", config: `{"project_id":`, ref: "GCP_SA_KEY",
			wantErr: "config:",
		},
		//nolint:gosec // G101: deliberately malformed fixture, not a real key.
		{
			name: "key is not json", config: "{}", ref: "GCP_SA_KEY",
			secret: `{"client_email":"a@b.com"`, wantErr: "not JSON",
		},
		//nolint:gosec // G101: deliberately malformed fixture, not a real key.
		{
			name: "wrong key type", config: "{}", ref: "GCP_SA_KEY",
			secret:  `{"type":"authorized_user","client_email":"a@b.com","private_key":"x"}`,
			wantErr: "want service_account",
		},
		{
			name: "missing client_email", config: "{}", ref: "GCP_SA_KEY",
			secret: `{"type":"service_account","private_key":"x"}`, wantErr: "missing client_email",
		},
		{
			name: "missing private_key", config: "{}", ref: "GCP_SA_KEY",
			secret:  `{"type":"service_account","client_email":"a@b.com"}`,
			wantErr: "missing private_key",
		},
		{
			name: "private_key not pem", config: "{}", ref: "GCP_SA_KEY",
			secret:  `{"type":"service_account","client_email":"a@b.com","private_key":"SUPERSECRETNOTPEM"}`,
			wantErr: "not PEM",
		},
		{
			name: "no project anywhere", config: "{}", ref: "GCP_SA_KEY",
			secret:  `{"type":"service_account","client_email":"a@b.com","private_key":"` + strings.ReplaceAll(gcpTestPEM, "\n", `\n`) + `"}`,
			wantErr: "set config.project_id",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := GCPBuilder(nil)(t.Context(), Connector{
				Name: "gcp", Kind: "gcp",
				Config:        json.RawMessage(tc.config),
				CredentialRef: tc.ref,
			}, func(context.Context, string) (string, error) { return tc.secret, nil })
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), "SUPERSECRETNOTPEM") || strings.Contains(err.Error(), "BEGIN PRIVATE KEY") {
				t.Fatalf("error leaked key material: %v", err)
			}
		})
	}
}

// TestGCPBuilderResolveFailure pins that an unresolvable ref fails the
// build, same as every other credential-bearing kind.
func TestGCPBuilderResolveFailure(t *testing.T) {
	t.Parallel()
	//nolint:gosec // G101: CredentialRef is a ref NAME, not a credential value.
	_, err := GCPBuilder(nil)(t.Context(), Connector{
		Name: "gcp", Kind: "gcp", Config: json.RawMessage(`{}`), CredentialRef: "GCP_SA_KEY",
	}, func(context.Context, string) (string, error) { return "", errors.New("no such secret") })
	if err == nil || !strings.Contains(err.Error(), "resolve credential_ref") {
		t.Fatalf("err = %v, want a resolve failure", err)
	}
}

// TestGCPConfigProjectOverride pins that config.project_id wins over
// the key's own project, and that the key's project is the default.
func TestGCPConfigProjectOverride(t *testing.T) {
	t.Parallel()
	src, _ := buildGCP(t, &fakeGCP{}, "{}")
	if src.project != "demo-project" {
		t.Fatalf("project = %q, want the key's project", src.project)
	}
	override, _ := buildGCP(t, &fakeGCP{}, `{"project_id":"other-project"}`)
	if override.project != "other-project" {
		t.Fatalf("project = %q, want the config override", override.project)
	}
}

// TestGCPAccessTokenMintsSignedAssertion proves the JWT bearer flow
// produces an assertion the endpoint can verify, and that the minted
// token is cached rather than re-minted per call.
func TestGCPAccessTokenMintsSignedAssertion(t *testing.T) {
	t.Parallel()
	f := &fakeGCP{}
	src, srv := buildGCP(t, f, "{}")

	token, err := src.accessToken(t.Context())
	if err != nil {
		t.Fatalf("accessToken: %v", err)
	}
	if token != "ya29.fake" {
		t.Fatalf("token = %q", token)
	}
	if _, err := src.accessToken(t.Context()); err != nil {
		t.Fatalf("second accessToken: %v", err)
	}
	if len(f.assertions) != 1 {
		t.Fatalf("minted %d times, want 1 (cached)", len(f.assertions))
	}

	parts := strings.Split(f.assertions[0], ".")
	if len(parts) != 3 {
		t.Fatalf("assertion has %d parts, want 3", len(parts))
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims struct {
		Iss   string `json:"iss"`
		Scope string `json:"scope"`
		Aud   string `json:"aud"`
		Iat   int64  `json:"iat"`
		Exp   int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if claims.Iss != "timothy@demo-project.iam.gserviceaccount.com" {
		t.Fatalf("iss = %q", claims.Iss)
	}
	if claims.Scope != gcpTokenScope {
		t.Fatalf("scope = %q", claims.Scope)
	}
	if claims.Aud != srv.URL+"/token" {
		t.Fatalf("aud = %q", claims.Aud)
	}
	if claims.Exp-claims.Iat != int64(gcpTokenLifetime.Seconds()) {
		t.Fatalf("lifetime = %d seconds", claims.Exp-claims.Iat)
	}
	if err := verifyGCPAssertion(f.assertions[0]); err != nil {
		t.Fatalf("signature: %v", err)
	}
}

// verifyGCPAssertion checks the assertion's RS256 signature against
// the test key's public half: the same check the token endpoint does.
func verifyGCPAssertion(assertion string) error {
	parts := strings.Split(assertion, ".")
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return rsa.VerifyPKCS1v15(&gcpTestKey.PublicKey, crypto.SHA256, sum[:], sig)
}

// TestGCPTestReportsTokenFailure pins that a rejected key surfaces as
// a reconnect-shaped error with no key material in it.
func TestGCPTestReportsTokenFailure(t *testing.T) {
	t.Parallel()
	src, _ := buildGCP(t, &fakeGCP{tokenCode: http.StatusBadRequest}, "{}")
	err := src.Test(t.Context())
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v, want the token endpoint's error code", err)
	}
	if strings.Contains(err.Error(), "BEGIN PRIVATE KEY") || strings.Contains(err.Error(), "assertion") {
		t.Fatalf("error leaked credential material: %v", err)
	}
}

// TestGCPAccountInfo pins the kind and email the aggregate description
// lists this connector under.
func TestGCPAccountInfo(t *testing.T) {
	t.Parallel()
	src, _ := buildGCP(t, &fakeGCP{}, "{}")
	kind, email := src.AccountInfo()
	if kind != "gcp" || email != "timothy@demo-project.iam.gserviceaccount.com" {
		t.Fatalf("AccountInfo() = %q, %q", kind, email)
	}
}

// TestGCPToolsAreReadOnly pins the tool surface and that every tool
// carries the ReadOnly marker missions' connector-reads resolver uses.
func TestGCPToolsAreReadOnly(t *testing.T) {
	t.Parallel()
	src, _ := buildGCP(t, &fakeGCP{}, "{}")
	want := map[string]bool{"list_gcs_objects": true, "read_gcs_object": true, "run_bigquery_query": true}
	got := map[string]bool{}
	for _, tl := range src.Tools() {
		got[tl.Name] = true
		if !tl.ReadOnly {
			t.Fatalf("tool %s is not marked ReadOnly", tl.Name)
		}
		if !json.Valid(tl.InputSchema) {
			t.Fatalf("tool %s has an invalid input schema", tl.Name)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

// TestGCPIAMDenialCarriesHint pins that a 403 from the API explains
// what IAM grant is missing rather than echoing a bare status.
func TestGCPIAMDenialCarriesHint(t *testing.T) {
	t.Parallel()
	f := &fakeGCP{apiCode: http.StatusForbidden, apiBody: `{"error":{"message":"caller does not have permission"}}`}
	src, _ := buildGCP(t, f, "{}")
	_, err := gcpExec(t, src, "list_gcs_objects", `{"bucket":"reports"}`)
	if err == nil || !strings.Contains(err.Error(), gcpIAMHint) {
		t.Fatalf("err = %v, want the IAM hint", err)
	}
	if !strings.Contains(err.Error(), "caller does not have permission") {
		t.Fatalf("err = %v, want the API's own reason", err)
	}
}

func TestGCSListObjects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		args     string
		apiBody  string
		want     string
		wantErr  string
		wantPath string
	}{
		{
			name: "lists objects",
			args: `{"bucket":"reports","prefix":"2026/"}`,
			apiBody: `{"items":[
				{"name":"2026/q1.csv","size":"412","contentType":"text/csv","updated":"2026-01-02T03:04:05Z"},
				{"name":"2026/q2.csv","size":"918","contentType":"text/csv","updated":"2026-04-02T03:04:05Z"}]}`,
			want: "2026/q1.csv\tsize: 412\tcontentType: text/csv\tupdated: 2026-01-02T03:04:05Z\n" +
				"2026/q2.csv\tsize: 918\tcontentType: text/csv\tupdated: 2026-04-02T03:04:05Z",
		},
		{
			name:    "empty bucket",
			args:    `{"bucket":"reports"}`,
			apiBody: `{"items":[]}`,
			want:    "no objects in gs://reports",
		},
		{
			name:    "empty prefix match names the prefix",
			args:    `{"bucket":"reports","prefix":"2027/"}`,
			apiBody: `{"items":[]}`,
			want:    `no objects in gs://reports matching prefix "2027/"`,
		},
		{
			name:    "gs:// prefix accepted",
			args:    `{"bucket":"gs://reports/"}`,
			apiBody: `{"items":[{"name":"a.txt","size":"1","contentType":"text/plain","updated":"x"}]}`,
			want:    "a.txt\tsize: 1\tcontentType: text/plain\tupdated: x",
		},
		{
			name:    "bucket with a path rejected",
			args:    `{"bucket":"reports/2026"}`,
			wantErr: "bucket must be a bucket name with no path",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src, _ := buildGCP(t, &fakeGCP{apiBody: tc.apiBody}, "{}")
			got, err := gcpExec(t, src, "list_gcs_objects", tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestGCSReadObject(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    string
		apiBody string
		want    string
		wantErr string
	}{
		{
			name:    "reads text",
			args:    `{"bucket":"reports","name":"2026/q1.csv"}`,
			apiBody: "a,b\n1,2",
			want:    "gs://reports/2026/q1.csv\n\na,b\n1,2",
		},
		{
			name:    "empty name rejected",
			args:    `{"bucket":"reports","name":"  "}`,
			wantErr: "name must be an object name",
		},
		{
			name:    "oversize object truncated",
			args:    `{"bucket":"reports","name":"big.log"}`,
			apiBody: strings.Repeat("x", gcpObjectReadMax+500),
			want: "gs://reports/big.log\n\n" + strings.Repeat("x", gcpObjectReadMax) +
				"\n\n[truncated: object continues]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src, _ := buildGCP(t, &fakeGCP{apiBody: tc.apiBody}, "{}")
			got, err := gcpExec(t, src, "read_gcs_object", tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestBigQueryQuery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    string
		apiBody string
		want    string
		wantErr string
	}{
		{
			name: "renders rows",
			args: `{"query":"SELECT name, n FROM demo.t"}`,
			apiBody: `{"jobComplete":true,"totalRows":"2",
				"schema":{"fields":[{"name":"name"},{"name":"n"}]},
				"rows":[{"f":[{"v":"alpha"},{"v":"1"}]},{"f":[{"v":"beta"},{"v":null}]}]}`,
			want: "name\tn\nalpha\t1\nbeta\tNULL",
		},
		{
			name:    "no rows",
			args:    `{"query":"SELECT 1 FROM demo.t WHERE false"}`,
			apiBody: `{"jobComplete":true,"schema":{"fields":[{"name":"a"}]},"rows":[]}`,
			want:    "the query returned no rows",
		},
		{
			name:    "incomplete job reports instead of parking",
			args:    `{"query":"SELECT * FROM demo.huge"}`,
			apiBody: `{"jobComplete":false}`,
			wantErr: "still running",
		},
		{
			name:    "write statement rejected",
			args:    `{"query":"DELETE FROM demo.t WHERE true"}`,
			wantErr: "read-only: DELETE statements are not allowed",
		},
		{
			name:    "ddl rejected",
			args:    `{"query":"  create table demo.t (a INT64)"}`,
			wantErr: "read-only: CREATE statements are not allowed",
		},
		{
			name:    "chained statement rejected",
			args:    `{"query":"SELECT 1; DROP TABLE demo.t"}`,
			wantErr: "must be a single statement",
		},
		{
			name:    "trailing semicolon allowed",
			args:    `{"query":"SELECT a FROM demo.t;"}`,
			apiBody: `{"jobComplete":true,"totalRows":"1","schema":{"fields":[{"name":"a"}]},"rows":[{"f":[{"v":"7"}]}]}`,
			want:    "a\n7",
		},
		{
			name:    "with is allowed",
			args:    `{"query":"WITH x AS (SELECT 1 AS a) SELECT a FROM x"}`,
			apiBody: `{"jobComplete":true,"totalRows":"1","schema":{"fields":[{"name":"a"}]},"rows":[{"f":[{"v":"1"}]}]}`,
			want:    "a\n1",
		},
		{
			name:    "empty query rejected",
			args:    `{"query":"   "}`,
			wantErr: "query is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src, _ := buildGCP(t, &fakeGCP{apiBody: tc.apiBody}, "{}")
			got, err := gcpExec(t, src, "run_bigquery_query", tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// TestBigQueryTruncationNote pins that a capped result says so rather
// than silently looking complete.
func TestBigQueryTruncationNote(t *testing.T) {
	t.Parallel()
	src, _ := buildGCP(t, &fakeGCP{apiBody: `{"jobComplete":true,"totalRows":"9000",
		"schema":{"fields":[{"name":"a"}]},"rows":[{"f":[{"v":"1"}]}]}`}, "{}")
	got, err := gcpExec(t, src, "run_bigquery_query", `{"query":"SELECT a FROM demo.t","max_results":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "[truncated: 1 of 9000 rows]") {
		t.Fatalf("got:\n%s\nwant a truncation note", got)
	}
}

// TestBigQueryQuerySendsLocation pins that config.location reaches the
// job, since a query against an EU dataset fails without it.
func TestBigQueryQuerySendsLocation(t *testing.T) {
	t.Parallel()
	f := &fakeGCP{apiBody: `{"jobComplete":true,"schema":{"fields":[]},"rows":[]}`}
	src, _ := buildGCP(t, f, `{"location":"EU"}`)
	if _, err := gcpExec(t, src, "run_bigquery_query", `{"query":"SELECT 1"}`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.bodies) == 0 || !strings.Contains(f.bodies[len(f.bodies)-1], `"location":"EU"`) {
		t.Fatalf("request bodies = %v, want the configured location", f.bodies)
	}
}

// gcpExec runs one of the source's tools by name.
func gcpExec(t *testing.T, src *gcpSource, name, args string) (string, error) {
	t.Helper()
	for _, tl := range src.Tools() {
		if tl.Name == name {
			return tl.Execute(t.Context(), json.RawMessage(args))
		}
	}
	t.Fatalf("no tool named %q", name)
	return "", nil
}
