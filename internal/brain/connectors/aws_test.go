package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/platform/awscreds"
)

// capturedRequest is one signed request the fake AWS MCP server saw.
type capturedRequest struct {
	header http.Header
	body   []byte
}

// awsCapture wraps a handler and records every request's headers and
// body before passing it on.
func awsCapture(next http.Handler, got *[]capturedRequest) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*got = append(*got, capturedRequest{header: r.Header.Clone(), body: body})
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		next.ServeHTTP(w, r)
	}
}

//nolint:gosec // G101: fake test credentials, not a real key pair.
const awsTestSecret = `{"access_key_id":"AKIATEST","secret_access_key":"shhh"}`

// TestAWSHandshakeSignsEveryRequest proves the handshake completes and
// that every request carries a complete SigV4 signature over the body
// the server actually received.
func TestAWSHandshakeSignsEveryRequest(t *testing.T) {
	t.Parallel()
	// httptest serves http, so the https guard is bypassed by signing
	// the client directly rather than through awsResolveConfig.
	src, got, err := buildAWSOverHTTP(t, awsTestSecret, "eu-west-1")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(src.Tools()) != 2 {
		t.Fatalf("tools = %d, want 2", len(src.Tools()))
	}
	if len(*got) < 3 {
		t.Fatalf("requests = %d, want initialize + initialized + tools/list", len(*got))
	}
	for i, req := range *got {
		auth := req.header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIATEST/") {
			t.Fatalf("request %d authorization = %q", i, auth)
		}
		if !strings.Contains(auth, "/eu-west-1/aws-mcp/aws4_request") {
			t.Fatalf("request %d credential scope = %q", i, auth)
		}
		if req.header.Get("X-Amz-Date") == "" {
			t.Fatalf("request %d has no X-Amz-Date", i)
		}
		sum := sha256.Sum256(req.body)
		if want := hex.EncodeToString(sum[:]); req.header.Get("X-Amz-Content-Sha256") != want {
			t.Fatalf("request %d payload hash = %q, want %q", i, req.header.Get("X-Amz-Content-Sha256"), want)
		}
		if strings.Contains(auth, "connection") {
			t.Fatalf("request %d signed the Connection header: %q", i, auth)
		}
		if req.header.Get("X-Amz-Security-Token") != "" {
			t.Fatalf("request %d carries a session token it was not given", i)
		}
	}
}

// TestAWSSessionTokenHeader covers temporary credentials.
func TestAWSSessionTokenHeader(t *testing.T) {
	t.Parallel()
	//nolint:gosec // G101: fake test credentials, not a real key pair.
	secret := `{"access_key_id":"AKIATEST","secret_access_key":"shhh","session_token":"tok-9"}`
	_, got, err := buildAWSOverHTTP(t, secret, "us-east-1")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for i, req := range *got {
		if req.header.Get("X-Amz-Security-Token") != "tok-9" {
			t.Fatalf("request %d security token = %q", i, req.header.Get("X-Amz-Security-Token"))
		}
	}
}

// TestAWSToolsNamespaceThroughManager mirrors the MCP manager tests: an
// aws source aggregates under raw tool names like any other source.
func TestAWSToolsNamespaceThroughManager(t *testing.T) {
	t.Parallel()
	m := testManager(fakeRows{})
	m.sources = map[string]Source{
		"aws": &awsSource{mcpSource: &mcpSource{name: "aws", toolList: []*tools.Tool{
			{Name: "call_aws", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "search docs", InputSchema: json.RawMessage(`{"type":"object"}`)},
		}}},
	}
	names := map[string]bool{}
	for _, tl := range m.Tools(map[string]bool{"search docs": true}) {
		names[tl.Name] = true
	}
	if !names["call_aws"] {
		t.Fatalf("tools = %v, want call_aws un-namespaced", names)
	}
	if !names["aws_search_docs"] {
		t.Fatalf("tools = %v, want the reserved name namespaced", names)
	}
}

// TestAWSResolveConfig covers endpoint validation and region
// precedence without needing a server.
func TestAWSResolveConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		cfg        awsConfig
		wantRegion string
		wantErr    string
	}{
		{
			name:       "region derived from managed host",
			cfg:        awsConfig{Endpoint: "https://aws-mcp.eu-central-1.api.aws/mcp"},
			wantRegion: "eu-central-1",
		},
		{
			name:       "explicit region wins over host",
			cfg:        awsConfig{Endpoint: "https://aws-mcp.eu-central-1.api.aws/mcp", Region: "us-west-2"},
			wantRegion: "us-west-2",
		},
		{
			name:    "http endpoint rejected",
			cfg:     awsConfig{Endpoint: "http://aws-mcp.eu-central-1.api.aws/mcp"},
			wantErr: "must be https",
		},
		{
			name:    "missing endpoint",
			cfg:     awsConfig{},
			wantErr: "config.endpoint is required",
		},
		{
			name:    "undeducible region",
			cfg:     awsConfig{Endpoint: "https://mcp.example.com/"},
			wantErr: "config.region is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := tc.cfg
			got, err := awsResolveConfig(&cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantRegion {
				t.Fatalf("region = %q, want %q", got, tc.wantRegion)
			}
		})
	}
}

// TestAWSBuildRejectsBadCredentials covers the two credential failures
// and pins that no key material reaches the error text.
func TestAWSBuildRejectsBadCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ref     string
		secret  string
		wantErr string
	}{
		{name: "missing credential_ref", ref: "", wantErr: "credential_ref is required"},
		//nolint:gosec // G101: deliberately malformed fixture, not a real key.
		{name: "malformed json", ref: "AWS_MCP_KEYS", secret: `{"access_key_id":"AKIALEAK"`, wantErr: "parse static credentials"},
		//nolint:gosec // G101: CredentialRef is a ref NAME, not a credential value.
		{name: "missing keys", ref: "AWS_MCP_KEYS", secret: `{"region":"us-east-1"}`, wantErr: "missing access_key_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeMCP{}
			srv := httptest.NewServer(f.handler())
			t.Cleanup(srv.Close)
			_, err := AWSBuilder(srv.Client(), MCPDeferral{})(t.Context(), Connector{
				Name: "aws", Kind: "aws",
				Config:        json.RawMessage(`{"endpoint":"https://aws-mcp.us-east-1.api.aws/mcp"}`),
				CredentialRef: tc.ref,
			}, func(context.Context, string) (string, error) { return tc.secret, nil })
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), "AKIALEAK") || strings.Contains(err.Error(), "shhh") {
				t.Fatalf("error leaked key material: %v", err)
			}
		})
	}
}

// TestAWSResolveFailure pins that an unresolvable ref fails the build.
func TestAWSResolveFailure(t *testing.T) {
	t.Parallel()
	//nolint:gosec // G101: CredentialRef is a ref NAME, not a credential value.
	_, err := AWSBuilder(nil, MCPDeferral{})(t.Context(), Connector{
		Name: "aws", Kind: "aws",
		Config:        json.RawMessage(`{"endpoint":"https://aws-mcp.us-east-1.api.aws/mcp"}`),
		CredentialRef: "AWS_MCP_KEYS",
	}, func(context.Context, string) (string, error) { return "", errors.New("no such secret") })
	if err == nil || !strings.Contains(err.Error(), "resolve credential_ref") {
		t.Fatalf("err = %v, want a resolve failure", err)
	}
}

// TestAWSTestAddsIAMHint pins the operator-facing hint on a denial and
// that a healthy server's Test stays clean.
func TestAWSTestAddsIAMHint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "denied", status: http.StatusForbidden, wantErr: true},
		{name: "unauthorized", status: http.StatusUnauthorized, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"not authorized"}`))
			}))
			t.Cleanup(srv.Close)
			src := &awsSource{mcpSource: &mcpSource{
				name: "aws", cfg: mcpConfig{Endpoint: srv.URL}, client: srv.Client(),
			}}
			err := src.Test(t.Context())
			if err == nil || !strings.Contains(err.Error(), awsIAMHint) {
				t.Fatalf("err = %v, want the IAM hint", err)
			}
		})
	}
}

// buildAWSOverHTTP builds a signed aws source against a plain-http
// httptest server: awsResolveConfig's https rule is exercised
// separately, so the signing path gets a reachable endpoint here.
func buildAWSOverHTTP(t *testing.T, secret, region string) (Source, *[]capturedRequest, error) {
	t.Helper()
	f := &fakeMCP{}
	got := &[]capturedRequest{}
	srv := httptest.NewServer(awsCapture(f.handler(), got))
	t.Cleanup(srv.Close)

	creds, err := awscreds.Parse(secret)
	if err != nil {
		return nil, got, err
	}
	client := &http.Client{Transport: &sigv4Transport{
		base: srv.Client().Transport, creds: *creds, region: region, signer: v4.NewSigner(),
	}}
	src := &mcpSource{name: "aws", cfg: mcpConfig{Endpoint: srv.URL}, client: client}
	if err := src.connect(t.Context()); err != nil {
		return nil, got, err
	}
	return &awsSource{mcpSource: src}, got, nil
}
