package secretstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/jackc/pgx/v5"
)

// VaultConfig connects the vault backend to a HashiCorp Vault KV v2
// mount. TokenRef names a db-backed secret holding the Vault token —
// the token never sits in this table or in env.
type VaultConfig struct {
	Address  string `json:"address"`
	Mount    string `json:"mount"`
	TokenRef string `json:"token_ref"`
}

// ASMConfig connects the asm backend to AWS Secrets Manager. Auth
// comes from the SDK's default credential chain (like Bedrock);
// Region empty defers to the chain's own region resolution.
type ASMConfig struct {
	Region string `json:"region"`
}

// asmAPI is the slice of the Secrets Manager client the store uses;
// an interface so tests can stub AWS without live credentials.
type asmAPI interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput,
		opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
	ListSecrets(ctx context.Context, in *secretsmanager.ListSecretsInput,
		opts ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error)
}

const (
	defaultVaultMount    = "secret"
	defaultVaultTokenRef = "VAULT_TOKEN"
	backendHTTPTimeout   = 10 * time.Second
)

// backendRefPattern bounds what a backend_ref may look like (vault
// paths, ASM names/ARNs, an optional #field suffix). Like
// credential_ref it must be a name, never a value: no spaces, quotes,
// or long opaque blobs.
var backendRefPattern = regexp.MustCompile(`^[A-Za-z0-9_.:/#=+@-]{1,256}$`)

// vaultHTTP never follows redirects: Go strips only Authorization and
// Cookie on cross-host redirects, so a malicious or compromised
// endpoint could otherwise bounce the X-Vault-Token header elsewhere.
var vaultHTTP = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// GetBackendConfig returns the stored config JSON for a backend, or
// "{}" when none has been saved yet.
func (s *Store) GetBackendConfig(ctx context.Context, backend string) (json.RawMessage, error) {
	if err := validExternalBackend(backend); err != nil {
		return nil, err
	}
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("secretstore: %w", err)
	}
	var cfg json.RawMessage
	err = db.QueryRow(ctx,
		`SELECT config FROM secret_backend_config WHERE backend = $1`, backend).Scan(&cfg)
	if errors.Is(err, pgx.ErrNoRows) {
		return json.RawMessage("{}"), nil
	}
	if err != nil {
		return nil, fmt.Errorf("secretstore: read backend config %s: %w", backend, err)
	}
	return cfg, nil
}

// SetBackendConfig upserts a backend's connection config and returns
// the normalized form it stored — callers audit that, never the raw
// request body, so a mistakenly pasted credential can't land in the
// audit log. Unknown fields are dropped.
func (s *Store) SetBackendConfig(ctx context.Context, backend string, cfg json.RawMessage) (json.RawMessage, error) {
	if err := validExternalBackend(backend); err != nil {
		return nil, err
	}
	normalized, err := normalizeBackendConfig(backend, cfg)
	if err != nil {
		return nil, err
	}
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("secretstore: %w", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO secret_backend_config (backend, config, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (backend) DO UPDATE SET config = $2, updated_at = now()`,
		backend, normalized)
	if err != nil {
		return nil, fmt.Errorf("secretstore: set backend config %s: %w", backend, err)
	}
	if backend == "asm" {
		s.asmMu.Lock()
		s.asm = nil
		s.asmMu.Unlock()
	}
	return normalized, nil
}

// SetExternal points refName at a secret held in an external backend.
// backendRef is the path/name in that system (vault: "path[#field]"
// within the configured mount, field default "value"; asm:
// "name-or-arn[#json_key]").
func (s *Store) SetExternal(ctx context.Context, refName, backend, backendRef string) error {
	if err := validExternalBackend(backend); err != nil {
		return err
	}
	if err := validBackendRef(backendRef); err != nil {
		return err
	}
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("secretstore: %w", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO secrets (ref_name, backend, backend_ref, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (ref_name) DO UPDATE
			SET backend = $2, backend_ref = $3, ciphertext = NULL, nonce = NULL, updated_at = now()`,
		refName, backend, backendRef)
	if err != nil {
		return fmt.Errorf("secretstore: set external %s: %w", refName, err)
	}
	return nil
}

// TestBackend checks a backend's connectivity and auth without
// touching any stored secret: vault does a token lookup-self, asm
// lists at most one secret.
func (s *Store) TestBackend(ctx context.Context, backend string) error {
	switch backend {
	case "vault":
		cfg, err := s.vaultConfig(ctx)
		if err != nil {
			return err
		}
		token, err := s.vaultToken(ctx, cfg)
		if err != nil {
			return err
		}
		return vaultRequest(ctx, cfg.Address, "/v1/auth/token/lookup-self", token, nil)
	case "asm":
		client, err := s.asmClient(ctx)
		if err != nil {
			return err
		}
		one := int32(1)
		_, err = client.ListSecrets(ctx, &secretsmanager.ListSecretsInput{MaxResults: &one})
		if err != nil {
			return fmt.Errorf("secretstore: asm test (needs secretsmanager:ListSecrets; resolution itself only needs GetSecretValue): %w", err)
		}
		return nil
	default:
		return validExternalBackend(backend)
	}
}

func validExternalBackend(backend string) error {
	if backend != "vault" && backend != "asm" {
		return fmt.Errorf("secretstore: unknown backend %q", backend)
	}
	return nil
}

// validBackendRef rejects anything that isn't a plausible path or
// name, and any ".." segment that could escape the vault mount when
// spliced into the request path.
func validBackendRef(ref string) error {
	if !backendRefPattern.MatchString(ref) || strings.Contains(ref, "..") {
		return fmt.Errorf("secretstore: backend_ref must be a path or name (vault path, ASM name/ARN), never a secret value")
	}
	return nil
}

// normalizeBackendConfig re-marshals cfg through the backend's typed
// struct, dropping unknown fields and filling vault defaults.
func normalizeBackendConfig(backend string, cfg json.RawMessage) (json.RawMessage, error) {
	switch backend {
	case "vault":
		var v VaultConfig
		if err := json.Unmarshal(cfg, &v); err != nil {
			return nil, fmt.Errorf("secretstore: vault config: %w", err)
		}
		u, err := url.Parse(v.Address)
		if err != nil {
			return nil, fmt.Errorf("secretstore: vault config: bad address: %w", err)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("secretstore: vault config: address must be an http(s) URL")
		}
		if v.Mount == "" {
			v.Mount = defaultVaultMount
		}
		if v.TokenRef == "" {
			v.TokenRef = defaultVaultTokenRef
		}
		return json.Marshal(v)
	case "asm":
		var a ASMConfig
		if err := json.Unmarshal(cfg, &a); err != nil {
			return nil, fmt.Errorf("secretstore: asm config: %w", err)
		}
		return json.Marshal(a)
	default:
		return nil, validExternalBackend(backend)
	}
}

func (s *Store) vaultConfig(ctx context.Context) (VaultConfig, error) {
	raw, err := s.GetBackendConfig(ctx, "vault")
	if err != nil {
		return VaultConfig{}, err
	}
	var cfg VaultConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return VaultConfig{}, fmt.Errorf("secretstore: vault config: %w", err)
	}
	if cfg.Address == "" {
		return VaultConfig{}, fmt.Errorf("secretstore: vault backend not configured (set its address in Settings)")
	}
	if cfg.Mount == "" {
		cfg.Mount = defaultVaultMount
	}
	if cfg.TokenRef == "" {
		cfg.TokenRef = defaultVaultTokenRef
	}
	return cfg, nil
}

// vaultToken decrypts the db-backed token named by token_ref directly
// — never via Resolve — so a misconfigured token ref can't recurse
// back into the vault backend.
func (s *Store) vaultToken(ctx context.Context, cfg VaultConfig) (string, error) {
	r, err := s.row(ctx, cfg.TokenRef)
	if errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("secretstore: vault token %q not stored (paste it in Settings)", cfg.TokenRef)
	}
	if err != nil {
		return "", err
	}
	if r.backend != "db" {
		return "", fmt.Errorf("secretstore: vault token ref %q must be a db-backed secret, got %q", cfg.TokenRef, r.backend)
	}
	return s.cipher.open(r.ciphertext, r.nonce)
}

// resolveVault reads a KV v2 secret. backendRef is "path[#field]"
// inside the configured mount; field defaults to "value".
func (s *Store) resolveVault(ctx context.Context, backendRef string) (string, error) {
	cfg, err := s.vaultConfig(ctx)
	if err != nil {
		return "", err
	}
	token, err := s.vaultToken(ctx, cfg)
	if err != nil {
		return "", err
	}
	path, field := splitRef(backendRef, "value")

	var body struct {
		Data struct {
			Data map[string]json.RawMessage `json:"data"`
		} `json:"data"`
	}
	endpoint := "/v1/" + cfg.Mount + "/data/" + strings.TrimPrefix(path, "/")
	if err := vaultRequest(ctx, cfg.Address, endpoint, token, &body); err != nil {
		return "", err
	}
	val, err := stringField(body.Data.Data, field)
	if err != nil {
		return "", fmt.Errorf("secretstore: vault secret %s: %w", path, err)
	}
	return val, nil
}

func vaultRequest(ctx context.Context, address, endpoint, token string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, backendHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(address, "/")+endpoint, nil)
	if err != nil {
		return fmt.Errorf("secretstore: vault: %w", err)
	}
	req.Header.Set("X-Vault-Token", token)
	resp, err := vaultHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("secretstore: vault: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("secretstore: vault %s: status %d: %s", endpoint, resp.StatusCode, snippet)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("secretstore: vault %s: decode: %w", endpoint, err)
	}
	return nil
}

// resolveASM reads an AWS Secrets Manager secret. backendRef is
// "name-or-arn[#json_key]"; with a key, the secret string is parsed
// as JSON and that key returned, otherwise the whole string.
func (s *Store) resolveASM(ctx context.Context, backendRef string) (string, error) {
	client, err := s.asmClient(ctx)
	if err != nil {
		return "", err
	}
	id, key := splitRef(backendRef, "")
	out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &id})
	if err != nil {
		return "", fmt.Errorf("secretstore: asm %s: %w", id, err)
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("secretstore: asm %s: no string value", id)
	}
	if key == "" {
		return *out.SecretString, nil
	}
	val, err := pluckJSONField(*out.SecretString, key)
	if err != nil {
		return "", fmt.Errorf("secretstore: asm %s: %w", id, err)
	}
	return val, nil
}

func (s *Store) asmClient(ctx context.Context) (asmAPI, error) {
	s.asmMu.Lock()
	defer s.asmMu.Unlock()
	if s.asm != nil {
		return s.asm, nil
	}
	raw, err := s.GetBackendConfig(ctx, "asm")
	if err != nil {
		return nil, err
	}
	var cfg ASMConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("secretstore: asm config: %w", err)
	}
	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("secretstore: asm: load aws config: %w", err)
	}
	s.asm = secretsmanager.NewFromConfig(awsCfg)
	return s.asm, nil
}

// splitRef splits "ref#field" into (ref, field), falling back to
// defaultField when no #field suffix is present.
func splitRef(ref, defaultField string) (string, string) {
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, defaultField
}

func pluckJSONField(secret, key string) (string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(secret), &m); err != nil {
		return "", fmt.Errorf("secret is not a JSON object: %w", err)
	}
	return stringField(m, key)
}

// stringField extracts one string-typed key from a decoded JSON
// object; other keys may hold any type without breaking the lookup.
func stringField(m map[string]json.RawMessage, key string) (string, error) {
	raw, ok := m[key]
	if !ok {
		return "", fmt.Errorf("no field %q", key)
	}
	var val string
	if err := json.Unmarshal(raw, &val); err != nil {
		return "", fmt.Errorf("field %q is not a string", key)
	}
	return val, nil
}
