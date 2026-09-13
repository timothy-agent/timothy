package connectors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/SumonMSelim/timothy/internal/platform/awscreds"
)

// awsService is the SigV4 service name the managed AWS MCP Server signs
// requests under.
const awsService = "aws-mcp"

// awsConfig is the connectors.config shape for kind='aws'. Endpoint is
// the managed AWS MCP Server's streamable-HTTP URL; region is optional
// when the endpoint host already names it.
type awsConfig struct {
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
}

// awsEndpointHostRegion matches the managed endpoint's host shape
// aws-mcp.<region>.api.aws, the fallback when config.region is unset.
var awsEndpointHostRegion = regexp.MustCompile(`^aws-mcp\.([a-z0-9-]+)\.api\.aws$`)

// AWSBuilder returns the Builder for kind='aws': the managed AWS MCP
// Server reached over the shared MCP client, with every request signed
// SigV4 from static IAM keys in the secret store. credential_ref is
// required: the server has no anonymous mode. deferral is applied the
// same way MCPBuilder applies it, since the AWS server serves many
// tools.
func AWSBuilder(client *http.Client, deferral MCPDeferral) Builder {
	if client == nil {
		client = &http.Client{}
	}
	return func(ctx context.Context, c Connector, resolve Resolve) (Source, error) {
		var cfg awsConfig
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			return nil, fmt.Errorf("aws %s: config: %w", c.Name, err)
		}
		region, err := awsResolveConfig(&cfg)
		if err != nil {
			return nil, fmt.Errorf("aws %s: %w", c.Name, err)
		}
		if c.CredentialRef == "" {
			return nil, fmt.Errorf("aws %s: credential_ref is required (static IAM keys)", c.Name)
		}
		raw, err := resolve(ctx, c.CredentialRef)
		if err != nil {
			return nil, fmt.Errorf("aws %s: resolve credential_ref %q: %w", c.Name, c.CredentialRef, err)
		}
		creds, err := awscreds.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("aws %s: credential_ref %q: %w", c.Name, c.CredentialRef, err)
		}
		if creds.Region != "" && cfg.Region == "" {
			region = creds.Region
		}

		base := client.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		signed := &http.Client{
			Timeout: client.Timeout,
			Transport: &sigv4Transport{
				base:   base,
				creds:  *creds,
				region: region,
				signer: v4.NewSigner(),
			},
		}

		src := &mcpSource{name: c.Name, cfg: mcpConfig{Endpoint: cfg.Endpoint}, client: signed}
		if err := src.connect(ctx); err != nil {
			return nil, fmt.Errorf("aws %s: %w", c.Name, err)
		}
		if deferral.Threshold != nil && deferral.OnLoad != nil {
			if n := deferral.Threshold(ctx); n > 0 && len(src.toolList) > n {
				src.indexed = true
				src.onLoad = deferral.OnLoad
			}
		}
		return &awsSource{mcpSource: src}, nil
	}
}

// awsResolveConfig validates the endpoint and reports the signing
// region: config.region when set, otherwise derived from the managed
// endpoint's host.
func awsResolveConfig(cfg *awsConfig) (string, error) {
	if cfg.Endpoint == "" {
		return "", fmt.Errorf("config.endpoint is required")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return "", fmt.Errorf("config.endpoint: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("config.endpoint must be https, got %q", u.Scheme)
	}
	if cfg.Region != "" {
		return cfg.Region, nil
	}
	if m := awsEndpointHostRegion.FindStringSubmatch(u.Hostname()); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("config.region is required: cannot derive it from endpoint host %q", u.Hostname())
}

// awsSource is the managed AWS MCP Server: an mcpSource whose Test adds
// the IAM hint a bare 403 does not carry.
type awsSource struct {
	*mcpSource
}

// awsIAMHint tells the operator what a denied handshake actually needs.
const awsIAMHint = "IAM denies the AWS MCP Server for these keys; grant the principal access (ReadOnlyAccess to start)"

// Test wraps a denial with the IAM hint. The error carries status text
// only, never key material.
func (s *awsSource) Test(ctx context.Context) error {
	err := s.mcpSource.Test(ctx)
	if err != nil && awsDenied(err) {
		return fmt.Errorf("%w: %s", err, awsIAMHint)
	}
	return err
}

// awsDenied reports whether an rpc error came back as a 401 or 403
// transport status (see mcpStatusError's "status %d" prefix).
func awsDenied(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "status 401") || strings.Contains(msg, "status 403")
}

// sigv4Transport signs every outbound request with SigV4 for the
// aws-mcp service. Bodies are small JSON-RPC payloads, so reading one
// fully to hash it costs nothing.
type sigv4Transport struct {
	base   http.RoundTripper
	creds  awscreds.StaticCredentials
	region string
	signer *v4.Signer
}

func (t *sigv4Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("sigv4: read body: %w", err)
		}
	}
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])

	// RoundTrippers must not mutate the caller's request.
	signable := req.Clone(req.Context())
	signable.Body = io.NopCloser(bytes.NewReader(body))
	signable.ContentLength = int64(len(body))
	signable.Header.Set("X-Amz-Content-Sha256", payloadHash)
	// Connection is hop-by-hop; signing it breaks the signature at any
	// proxy that rewrites it.
	signable.Header.Del("Connection")

	err := t.signer.SignHTTP(signable.Context(), aws.Credentials{
		AccessKeyID:     t.creds.AccessKeyID,
		SecretAccessKey: t.creds.SecretAccessKey,
		SessionToken:    t.creds.SessionToken,
	}, signable, payloadHash, awsService, t.region, time.Now())
	if err != nil {
		return nil, fmt.Errorf("sigv4: sign: %w", err)
	}
	signable.Body = io.NopCloser(bytes.NewReader(body))
	return t.base.RoundTrip(signable)
}
