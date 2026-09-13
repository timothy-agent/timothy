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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// GCPConfig is the connectors.config shape for kind='gcp'. Auth is
// entirely the connector's credential_ref (a service-account key JSON
// in the secret store); config only narrows what that key is used
// against. ProjectID overrides the key's own project_id: a key may be
// granted access to projects beyond the one it was minted in.
type GCPConfig struct {
	ProjectID string `json:"project_id,omitempty"`
	// Location is BigQuery's job location (e.g. "EU", "us-central1");
	// empty lets BigQuery pick from the queried datasets.
	Location string `json:"location,omitempty"`
}

const (
	gcpStorageBase  = "https://storage.googleapis.com/storage/v1"
	gcpBigQueryBase = "https://bigquery.googleapis.com/bigquery/v2"

	// gcpTokenScope is the single scope every tool here runs under:
	// cloud-platform covers Storage and BigQuery, and the actual limit
	// on what the connector can do is the service account's IAM roles,
	// not the scope.
	//nolint:gosec // G101: an OAuth scope URL, not a credential.
	gcpTokenScope = "https://www.googleapis.com/auth/cloud-platform"

	// gcpTokenLifetime is the assertion's validity window; Google caps
	// it at one hour.
	gcpTokenLifetime = time.Hour

	// gcpObjectReadMax bounds read_gcs_object's returned text, same
	// convention as driveReadMaxResult.
	gcpObjectReadMax = 64 << 10

	// gcpQueryMaxRows caps run_bigquery_query's returned rows so a broad
	// query cannot blow the loop's context budget.
	gcpQueryMaxRows = 200

	// gcpQueryTimeout is how long the BigQuery API waits for a job
	// before replying "not complete"; the tool reports that rather than
	// polling, so a long query never parks a turn.
	gcpQueryTimeout = 30 * time.Second
)

// gcpServiceAccount is the subset of a service-account key JSON the
// JWT bearer flow needs. PrivateKey is PEM and never leaves this
// package.
type gcpServiceAccount struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// parseServiceAccount decodes and validates a service-account key JSON.
// Errors name the missing field only, never any part of the key.
func parseServiceAccount(raw string) (*gcpServiceAccount, *rsa.PrivateKey, error) {
	var sa gcpServiceAccount
	if err := json.Unmarshal([]byte(raw), &sa); err != nil {
		return nil, nil, fmt.Errorf("parse service account key: not JSON")
	}
	if sa.Type != "" && sa.Type != "service_account" {
		return nil, nil, fmt.Errorf("parse service account key: type is %q, want service_account", sa.Type)
	}
	if sa.ClientEmail == "" {
		return nil, nil, fmt.Errorf("parse service account key: missing client_email")
	}
	if sa.PrivateKey == "" {
		return nil, nil, fmt.Errorf("parse service account key: missing private_key")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = googleTokenURL
	}
	key, err := parsePKCS8RSA(sa.PrivateKey)
	if err != nil {
		return nil, nil, err
	}
	return &sa, key, nil
}

// parsePKCS8RSA decodes the PEM private_key a service-account key
// carries. Google mints these as PKCS#8 RSA; PKCS#1 is accepted too so
// a manually re-encoded key still works.
func parsePKCS8RSA(pemKey string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, fmt.Errorf("parse service account key: private_key is not PEM")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("parse service account key: private_key is %T, want RSA", key)
		}
		return rsaKey, nil
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse service account key: private_key is not a usable RSA key")
	}
	return key, nil
}

// GCPBuilder returns the Builder for kind='gcp': Cloud Storage and
// BigQuery reached over their REST APIs with an access token minted
// from a service-account key (RFC 7523 JWT bearer flow).
// credential_ref is required: there is no anonymous mode.
func GCPBuilder(client *http.Client) Builder {
	if client == nil {
		client = &http.Client{}
	}
	return func(ctx context.Context, c Connector, resolve Resolve) (Source, error) {
		var cfg GCPConfig
		if len(c.Config) > 0 {
			if err := json.Unmarshal(c.Config, &cfg); err != nil {
				return nil, fmt.Errorf("gcp %s: config: %w", c.Name, err)
			}
		}
		if c.CredentialRef == "" {
			return nil, fmt.Errorf("gcp %s: credential_ref is required (a service-account key JSON)", c.Name)
		}
		raw, err := resolve(ctx, c.CredentialRef)
		if err != nil {
			return nil, fmt.Errorf("gcp %s: resolve credential_ref %q: %w", c.Name, c.CredentialRef, err)
		}
		sa, key, err := parseServiceAccount(raw)
		if err != nil {
			return nil, fmt.Errorf("gcp %s: credential_ref %q: %w", c.Name, c.CredentialRef, err)
		}
		project := cfg.ProjectID
		if project == "" {
			project = sa.ProjectID
		}
		if project == "" {
			return nil, fmt.Errorf("gcp %s: no project: set config.project_id (the key carries none)", c.Name)
		}
		return &gcpSource{
			name: c.Name, cfg: cfg, project: project,
			sa: sa, key: key, client: client,
			storageBase: gcpStorageBase, bigQueryBase: gcpBigQueryBase,
		}, nil
	}
}

// gcpSource is one service account's access to one GCP project. Tools
// are fixed at build time; the access token is minted on first use and
// reused until it nears expiry.
type gcpSource struct {
	name    string
	cfg     GCPConfig
	project string
	sa      *gcpServiceAccount
	key     *rsa.PrivateKey
	client  *http.Client

	// API bases default to Google's; tests point them at fakes.
	storageBase  string
	bigQueryBase string

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (s *gcpSource) Tools() []*tools.Tool {
	return []*tools.Tool{s.storageList(), s.storageRead(), s.bigQueryQuery()}
}

// Test mints an access token, the cheapest honest proof the key is
// valid and not revoked, without needing any particular IAM role.
func (s *gcpSource) Test(ctx context.Context) error {
	_, err := s.accessToken(ctx)
	return err
}

func (s *gcpSource) Close() error { return nil }

// AccountInfo reports the kind and the service account's own address,
// which is what an operator picks between when several gcp connectors
// serve the same tool.
func (s *gcpSource) AccountInfo() (kind, email string) { return "gcp", s.sa.ClientEmail }

// accessToken returns a live access token, minting a new one when the
// cached one is within refreshSkew of expiring.
func (s *gcpSource) accessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Now().Before(s.expires.Add(-refreshSkew)) {
		return s.token, nil
	}
	assertion, err := s.signAssertion(time.Now())
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.sa.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("mint gcp access token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", gcpTokenError(resp)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("mint gcp access token: decode response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("mint gcp access token: response carried no token")
	}
	s.token = out.AccessToken
	s.expires = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return s.token, nil
}

// signAssertion builds the RS256 JWT the token endpoint exchanges for
// an access token (RFC 7523 section 2.1). Hand-rolled rather than
// pulled from a dependency: it is a fixed three-field header, five-
// claim body, and one PKCS#1 v1.5 signature.
func (s *gcpSource) signAssertion(now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss":   s.sa.ClientEmail,
		"scope": gcpTokenScope,
		"aud":   s.sa.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(gcpTokenLifetime).Unix(),
	})
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign gcp assertion: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// gcpTokenError maps a non-200 token endpoint response to a human
// message carrying the status and Google's error code only: the
// request body was a signed assertion, so nothing from it is echoed.
func gcpTokenError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e googleOAuthErrorBody
	_ = json.Unmarshal(body, &e)
	if e.Error != "" {
		return fmt.Errorf("gcp rejected the service-account key (status %d, error %q); "+
			"check the key is current and the service account is not disabled", resp.StatusCode, e.Error)
	}
	return fmt.Errorf("gcp rejected the service-account key (status %d)", resp.StatusCode)
}

// gcpAPIError maps a non-2xx Storage/BigQuery response to a human
// message. 401/403 carries the IAM hint a bare status does not: a
// service-account key is almost always valid and simply under-granted.
func gcpAPIError(resp *http.Response, op string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	msg := strings.TrimSpace(e.Error.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%s: %s; %s", op, msg, gcpIAMHint)
	}
	return fmt.Errorf("%s: gcp api status %d: %s", op, resp.StatusCode, msg)
}

// gcpIAMHint tells the operator what a denial actually needs, mirroring
// awsIAMHint.
const gcpIAMHint = "the service account lacks the IAM role for this call; grant it on the project or resource (roles/storage.objectViewer, roles/bigquery.jobUser + roles/bigquery.dataViewer to start)"

// api performs one authenticated GCP API call and decodes the JSON
// response into out (nil out discards the body). op prefixes errors.
func (s *gcpSource) api(ctx context.Context, method, apiURL, op string, body, out any) error {
	resp, err := s.rawAPI(ctx, method, apiURL, op, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode response: %w", op, err)
	}
	return nil
}

// rawAPI performs one authenticated GCP API call and returns the raw
// response for callers needing non-JSON bytes (object download).
// Callers must close the response body.
func (s *gcpSource) rawAPI(ctx context.Context, method, apiURL, op string, body any) (*http.Response, error) {
	token, err := s.accessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, apiURL, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, gcpAPIError(resp, op)
	}
	return resp, nil
}
