package sandboxd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// imageCheckCache probes a registry for the sandbox image's manifest
// (the Kubernetes CheckImage; no local image list exists there) and
// remembers a success so the health poll does not hit the registry
// every few seconds.
type imageCheckCache struct {
	mu      sync.Mutex
	okUntil time.Time
	client  *http.Client // nil means http.DefaultClient
}

const imageCheckTTL = 10 * time.Minute

func (c *imageCheckCache) check(ctx context.Context, image string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.okUntil) {
		return nil
	}
	if err := probeManifest(ctx, c.httpClient(), image); err != nil {
		return err
	}
	c.okUntil = time.Now().Add(imageCheckTTL)
	return nil
}

func (c *imageCheckCache) httpClient() *http.Client {
	if c.client != nil {
		return c.client
	}
	return http.DefaultClient
}

// imageRef is an image reference split into registry endpoint,
// repository path and tag or digest, with Docker's defaults applied
// (docker.io, library/ namespace, latest).
type imageRef struct {
	registry, repo, reference string
}

func parseImageRef(image string) (imageRef, error) {
	if image == "" {
		return imageRef{}, fmt.Errorf("empty image reference")
	}
	ref := imageRef{reference: "latest"}
	rest := image
	if i := strings.Index(rest, "@"); i >= 0 {
		ref.reference = rest[i+1:]
		rest = rest[:i]
	} else if i := strings.LastIndex(rest, ":"); i >= 0 && !strings.Contains(rest[i+1:], "/") {
		ref.reference = rest[i+1:]
		rest = rest[:i]
	}
	first, after, hasSlash := strings.Cut(rest, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		ref.registry, ref.repo = first, after
	} else {
		ref.registry, ref.repo = "docker.io", rest
	}
	if ref.registry == "docker.io" {
		ref.registry = "registry-1.docker.io"
		if !strings.Contains(ref.repo, "/") {
			ref.repo = "library/" + ref.repo
		}
	}
	if ref.repo == "" || ref.reference == "" {
		return imageRef{}, fmt.Errorf("malformed image reference %q", image)
	}
	return ref, nil
}

// scheme is https except for a loopback registry (kind's local
// registry serves plain http).
func (r imageRef) scheme() string {
	host, _, _ := strings.Cut(r.registry, ":")
	if host == "localhost" || host == "127.0.0.1" {
		return "http"
	}
	return "https"
}

func (r imageRef) manifestURL() string {
	return fmt.Sprintf("%s://%s/v2/%s/manifests/%s", r.scheme(), r.registry, r.repo, r.reference)
}

// probeManifest HEADs the manifest, obtaining an anonymous bearer
// token when the registry asks for one (ghcr.io and Docker Hub do for
// public images). Any other 401 or a 404 is a failure the operator
// must act on.
func probeManifest(ctx context.Context, client *http.Client, image string) error {
	ref, err := parseImageRef(image)
	if err != nil {
		return err
	}
	url := ref.manifestURL()
	resp, err := manifestHead(ctx, client, url, "")
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		token, err := anonymousToken(ctx, client, resp.Header.Get("WWW-Authenticate"))
		if err != nil {
			return fmt.Errorf("%s: %w", image, err)
		}
		if resp, err = manifestHead(ctx, client, url, token); err != nil {
			return err
		}
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%s: manifest not found", image)
	default:
		return fmt.Errorf("%s: registry answered %s", image, resp.Status)
	}
}

func manifestHead(ctx context.Context, client *http.Client, url, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	_ = resp.Body.Close()
	return resp, nil
}

// anonymousToken follows a `Bearer realm=...,service=...,scope=...`
// challenge with no credentials.
func anonymousToken(ctx context.Context, client *http.Client, challenge string) (string, error) {
	scheme, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", fmt.Errorf("registry requires authentication (%s)", scheme)
	}
	fields := map[string]string{}
	for _, kv := range strings.Split(params, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if ok {
			fields[k] = strings.Trim(v, `"`)
		}
	}
	realm := fields["realm"]
	if realm == "" {
		return "", fmt.Errorf("registry challenge has no realm")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm, nil)
	if err != nil {
		return "", err
	}
	q := req.URL.Query()
	for _, k := range []string{"service", "scope"} {
		if v := fields[k]; v != "" {
			q.Set(k, v)
		}
	}
	req.URL.RawQuery = q.Encode()
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token: %s", resp.Status)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("registry token: %w", err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", fmt.Errorf("registry token: empty response")
}
