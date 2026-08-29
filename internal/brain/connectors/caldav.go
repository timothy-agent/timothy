package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// CalDAVConfig is the connectors.config shape for kind='caldav'. URL is
// the calendar collection URL itself, no discovery. The password lives
// in the connector's credential_ref (secret store), never here.
// AccountEmail defaults to Username when unset.
type CalDAVConfig struct {
	URL          string `json:"url"`
	Username     string `json:"username"`
	AccountEmail string `json:"account_email,omitempty"`
}

// email returns the connected account's email: AccountEmail when set,
// else Username.
func (c CalDAVConfig) email() string {
	if c.AccountEmail != "" {
		return c.AccountEmail
	}
	return c.Username
}

// caldavConfig decodes connectors.config for kind='caldav'.
func caldavConfig(c Connector) (CalDAVConfig, error) {
	var cfg CalDAVConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return cfg, fmt.Errorf("caldav %s: config: %w", c.Name, err)
	}
	if cfg.URL == "" || cfg.Username == "" {
		return cfg, fmt.Errorf("caldav %s: config.url and config.username are required", c.Name)
	}
	if !isSecureCalDAVURL(cfg.URL) {
		return cfg, fmt.Errorf("caldav %s: config.url must be https (basic auth over cleartext)", c.Name)
	}
	return cfg, nil
}

// isSecureCalDAVURL reports whether url is safe to send basic auth
// credentials to: https always, or plain http only when the host is a
// loopback address (127.0.0.0/8, ::1, "localhost"): cleartext basic
// auth never leaves the machine there, and it keeps httptest fixtures
// working without weakening the check for a real deployment.
func isSecureCalDAVURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CalDAVBuilder returns the Builder for kind='caldav'. client is used
// for the collection's HTTP requests; a nil client defaults to
// &http.Client{}.
func CalDAVBuilder(client *http.Client) Builder {
	if client == nil {
		client = &http.Client{}
	}
	return func(_ context.Context, c Connector, resolve Resolve) (Source, error) {
		cfg, err := caldavConfig(c)
		if err != nil {
			return nil, err
		}
		if c.CredentialRef == "" {
			return nil, fmt.Errorf("caldav %s: credential_ref is required (the calendar password)", c.Name)
		}
		return &caldavSource{
			name:          c.Name,
			cfg:           cfg,
			credentialRef: c.CredentialRef,
			resolve:       resolve,
			client:        client,
		}, nil
	}
}

// caldavSource is a built caldav-kind connector: basic-auth CalDAV
// against one calendar collection.
type caldavSource struct {
	name          string
	cfg           CalDAVConfig
	credentialRef string
	resolve       Resolve
	client        *http.Client
}

// Tools returns list_calendar_events and create_calendar_event, always
// (caldav has no scope concept to gate on).
func (s *caldavSource) Tools() []*tools.Tool {
	return []*tools.Tool{s.calendarListEvents(), s.calendarCreateEvent()}
}

// Test performs a real PROPFIND against the collection, the cheapest
// honest signal the stored password still authenticates.
func (s *caldavSource) Test(ctx context.Context) error {
	_, err := s.propfind(ctx)
	return err
}

func (s *caldavSource) Close() error { return nil }

// AccountInfo reports this source's kind and connected account email
// (the accountInfo capability, see manager.go's aggregation).
func (s *caldavSource) AccountInfo() (kind, email string) {
	return "caldav", s.cfg.email()
}

// Identity proves the credential works via PROPFIND and reports it.
// Reuses GitHubIdentity's shape rather than adding a parallel one.
func (s *caldavSource) Identity(ctx context.Context) (GitHubIdentity, error) {
	if err := s.Test(ctx); err != nil {
		return GitHubIdentity{}, err
	}
	return GitHubIdentity{Login: s.cfg.Username, Email: s.cfg.email(), Scopes: "caldav"}, nil
}

// password resolves the connector's credential_ref.
func (s *caldavSource) password(ctx context.Context) (string, error) {
	pw, err := s.resolve(ctx, s.credentialRef)
	if err != nil {
		return "", fmt.Errorf("resolve credential_ref %q: %w", s.credentialRef, err)
	}
	return pw, nil
}

// request issues one authenticated request against the collection URL
// (or path, when non-empty, resolved relative to it), reads the whole
// body, and returns it alongside the status code. Never leaves a body
// unclosed.
func (s *caldavSource) request(ctx context.Context, method, path string, headers map[string]string, body []byte) (int, []byte, error) {
	pw, err := s.password(ctx)
	if err != nil {
		return 0, nil, err
	}
	target := s.cfg.URL
	if path != "" {
		target = strings.TrimRight(s.cfg.URL, "/") + "/" + strings.TrimLeft(path, "/")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(s.cfg.Username, pw)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("caldav request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("caldav request: read response: %w", err)
	}
	return resp.StatusCode, respBody, nil
}

// caldavPropfindBody asks for displayname and supported-calendar-
// component-set, Depth 0, the minimal request proving the collection
// exists and the credential can read it.
const caldavPropfindBody = `<?xml version="1.0" encoding="utf-8" ?>
<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <D:displayname/>
    <C:supported-calendar-component-set/>
  </D:prop>
</D:propfind>`

// propfind performs the Depth:0 identity/connectivity check.
func (s *caldavSource) propfind(ctx context.Context) ([]byte, error) {
	status, body, err := s.request(ctx, "PROPFIND", "", map[string]string{
		"Depth":        "0",
		"Content-Type": "application/xml; charset=utf-8",
	}, []byte(caldavPropfindBody))
	if err != nil {
		return nil, err
	}
	if status != http.StatusMultiStatus && status != http.StatusOK {
		return nil, caldavStatusError(status, body)
	}
	return body, nil
}

// caldavStatusError maps a non-2xx CalDAV response to a human message:
// 401/403 (bad or revoked credentials) gets a reconnect-oriented
// message, other statuses keep the status code plus a body snippet.
func caldavStatusError(status int, body []byte) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("CalDAV credentials invalid or expired, check the username and password")
	}
	snippet := body
	if len(snippet) > 512 {
		snippet = snippet[:512]
	}
	return fmt.Errorf("caldav status %d: %s", status, snippet)
}
