package secretstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

func TestSplitRef(t *testing.T) {
	cases := []struct {
		ref, defField, wantRef, wantField string
	}{
		{"timothy/anthropic", "value", "timothy/anthropic", "value"},
		{"timothy/anthropic#api_key", "value", "timothy/anthropic", "api_key"},
		{"arn:aws:secretsmanager:eu-west-1:1:secret:x", "", "arn:aws:secretsmanager:eu-west-1:1:secret:x", ""},
		{"name#key", "", "name", "key"},
	}
	for _, c := range cases {
		ref, field := splitRef(c.ref, c.defField)
		if ref != c.wantRef || field != c.wantField {
			t.Errorf("splitRef(%q, %q) = (%q, %q), want (%q, %q)",
				c.ref, c.defField, ref, field, c.wantRef, c.wantField)
		}
	}
}

func TestPluckJSONField(t *testing.T) {
	val, err := pluckJSONField(`{"api_key":"sk-123","other":"x"}`, "api_key")
	if err != nil || val != "sk-123" {
		t.Fatalf("pluckJSONField = (%q, %v), want (sk-123, nil)", val, err)
	}
	if _, err := pluckJSONField(`{"a":"b"}`, "missing"); err == nil {
		t.Fatal("missing key: want error")
	}
	if _, err := pluckJSONField(`not json`, "a"); err == nil {
		t.Fatal("non-JSON secret: want error")
	}
}

func TestNormalizeBackendConfig(t *testing.T) {
	got, err := normalizeBackendConfig("vault", json.RawMessage(`{"address":"http://vault:8200"}`))
	if err != nil {
		t.Fatal(err)
	}
	var cfg VaultConfig
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Mount != defaultVaultMount || cfg.TokenRef != defaultVaultTokenRef {
		t.Errorf("defaults not filled: %+v", cfg)
	}

	if _, err := normalizeBackendConfig("vault", json.RawMessage(`{}`)); err == nil {
		t.Fatal("vault without address: want error")
	}
	if _, err := normalizeBackendConfig("vault", json.RawMessage(`{"address":"ftp://vault:8200"}`)); err == nil {
		t.Fatal("vault with non-http scheme: want error")
	}
	if _, err := normalizeBackendConfig("db", json.RawMessage(`{}`)); err == nil {
		t.Fatal("backend db: want error (only vault/asm have config)")
	}
	if _, err := normalizeBackendConfig("asm", json.RawMessage(`{"region":"eu-west-1"}`)); err != nil {
		t.Fatalf("asm config: %v", err)
	}
}

func TestNormalizeBackendConfigAuthMethods(t *testing.T) {
	// Vault AppRole: role_id required, secret_id_ref defaulted.
	got, err := normalizeBackendConfig("vault",
		json.RawMessage(`{"address":"http://v:8200","auth":"approle","role_id":"r-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var v VaultConfig
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatal(err)
	}
	if v.SecretIDRef != defaultVaultSecretIDRef {
		t.Errorf("secret_id_ref = %q, want default", v.SecretIDRef)
	}
	if _, err := normalizeBackendConfig("vault",
		json.RawMessage(`{"address":"http://v:8200","auth":"approle"}`)); err == nil {
		t.Fatal("approle without role_id: want error")
	}
	if _, err := normalizeBackendConfig("vault",
		json.RawMessage(`{"address":"http://v:8200","auth":"ldap"}`)); err == nil {
		t.Fatal("unknown vault auth: want error")
	}

	// ASM: profile needs a name, keys need access_key_id + defaulted ref.
	if _, err := normalizeBackendConfig("asm", json.RawMessage(`{"auth":"profile"}`)); err == nil {
		t.Fatal("asm profile auth without profile: want error")
	}
	got, err = normalizeBackendConfig("asm",
		json.RawMessage(`{"auth":"keys","access_key_id":"AKIA123"}`))
	if err != nil {
		t.Fatal(err)
	}
	var a ASMConfig
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if a.SecretKeyRef != defaultASMSecretKeyRef {
		t.Errorf("secret_key_ref = %q, want default", a.SecretKeyRef)
	}
	if _, err := normalizeBackendConfig("asm", json.RawMessage(`{"auth":"keys"}`)); err == nil {
		t.Fatal("asm keys auth without access_key_id: want error")
	}
	if _, err := normalizeBackendConfig("asm", json.RawMessage(`{"auth":"oauth"}`)); err == nil {
		t.Fatal("unknown asm auth: want error")
	}
}

func TestValidExternalBackend(t *testing.T) {
	for _, ok := range []string{"vault", "asm"} {
		if err := validExternalBackend(ok); err != nil {
			t.Errorf("validExternalBackend(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"file", "db", "nope"} {
		if err := validExternalBackend(bad); err == nil {
			t.Errorf("validExternalBackend(%q) = nil, want error", bad)
		}
	}
}

func TestValidExternalRefName(t *testing.T) {
	for _, ok := range []string{"OPENAI_API_KEY", "zai.api-key", "a"} {
		if err := validExternalRefName(ok); err != nil {
			t.Errorf("validExternalRefName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "a/b", "../admin", "a..b", "a b", "a#b", strings.Repeat("x", 129)} {
		if err := validExternalRefName(bad); err == nil {
			t.Errorf("validExternalRefName(%q) = nil, want error", bad)
		}
	}
}

// stubASM fakes the Secrets Manager slice the store uses. secrets maps
// name -> current SecretString, mutated by Create/Put/Delete so tests
// can assert write-through behavior, not just reads.
type stubASM struct {
	secrets map[string]string
	err     error
}

func newStubASM(seed map[string]string) *stubASM {
	if seed == nil {
		seed = map[string]string{}
	}
	return &stubASM{secrets: seed}
}

func (s *stubASM) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput,
	_ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	val, ok := s.secrets[*in.SecretId]
	if !ok {
		return nil, &types.ResourceNotFoundException{}
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: &val}, nil
}

func (s *stubASM) ListSecrets(context.Context, *secretsmanager.ListSecretsInput,
	...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
	return &secretsmanager.ListSecretsOutput{}, s.err
}

func (s *stubASM) CreateSecret(_ context.Context, in *secretsmanager.CreateSecretInput,
	_ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	if _, exists := s.secrets[*in.Name]; exists {
		return nil, &types.ResourceExistsException{}
	}
	s.secrets[*in.Name] = *in.SecretString
	return &secretsmanager.CreateSecretOutput{Name: in.Name}, nil
}

func (s *stubASM) PutSecretValue(_ context.Context, in *secretsmanager.PutSecretValueInput,
	_ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.secrets[*in.SecretId] = *in.SecretString
	return &secretsmanager.PutSecretValueOutput{Name: in.SecretId}, nil
}

func (s *stubASM) DeleteSecret(_ context.Context, in *secretsmanager.DeleteSecretInput,
	_ ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	delete(s.secrets, *in.SecretId)
	return &secretsmanager.DeleteSecretOutput{Name: in.SecretId}, nil
}

func TestResolveASMWithStub(t *testing.T) {
	//nolint:gosec // G101: fake stub value, not a credential.
	s := &Store{asm: newStubASM(map[string]string{"prod/key": `{"api_key":"sk-9","port":443}`})}

	got, err := s.resolveASM(context.Background(), "prod/key#api_key")
	if err != nil || got != "sk-9" {
		t.Fatalf("resolveASM = (%q, %v), want (sk-9, nil)", got, err)
	}

	// No #key: whole secret string comes back verbatim.
	got, err = s.resolveASM(context.Background(), "prod/key")
	if err != nil || got != `{"api_key":"sk-9","port":443}` {
		t.Fatalf("resolveASM whole = (%q, %v)", got, err)
	}

	// Non-string field fails that field only, with a clear error.
	if _, err := s.resolveASM(context.Background(), "prod/key#port"); err == nil {
		t.Fatal("non-string field: want error")
	}
}

// TestWriteASMCreatesThenFallsBackToPut pins write-through: a fresh
// name goes through CreateSecret, a name that already exists falls
// back to PutSecretValue (ResourceExistsException) rather than
// erroring.
func TestWriteASMCreatesThenFallsBackToPut(t *testing.T) {
	stub := newStubASM(nil)
	s := &Store{asm: stub}

	if err := s.writeASM(context.Background(), "timothy/new", "sk-1"); err != nil {
		t.Fatalf("writeASM (create): %v", err)
	}
	if stub.secrets["timothy/new"] != "sk-1" {
		t.Fatalf("secrets[timothy/new] = %q, want sk-1", stub.secrets["timothy/new"])
	}

	if err := s.writeASM(context.Background(), "timothy/new", "sk-2"); err != nil {
		t.Fatalf("writeASM (put fallback): %v", err)
	}
	if stub.secrets["timothy/new"] != "sk-2" {
		t.Fatalf("secrets[timothy/new] = %q, want sk-2 after overwrite", stub.secrets["timothy/new"])
	}
}

func TestDeleteASM(t *testing.T) {
	stub := newStubASM(map[string]string{"timothy/gone": "sk-x"})
	s := &Store{asm: stub}

	if err := s.deleteASM(context.Background(), "timothy/gone"); err != nil {
		t.Fatalf("deleteASM: %v", err)
	}
	if _, ok := stub.secrets["timothy/gone"]; ok {
		t.Fatal("deleteASM left the secret behind")
	}
}

func TestTestBackendASMWriteProbe(t *testing.T) {
	stub := newStubASM(nil)
	s := &Store{asm: stub}

	if err := s.TestBackend(context.Background(), "asm"); err != nil {
		t.Fatalf("TestBackend(asm): %v", err)
	}
	if _, ok := stub.secrets[probeRef]; ok {
		t.Error("TestBackend left its write probe behind in asm")
	}
}

func TestVaultRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "tok-1" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path != "/v1/secret/data/timothy/anthropic" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"value":"sk-abc"}}}`))
	}))
	defer srv.Close()

	var body struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	err := vaultRequest(context.Background(), srv.URL+"/", "/v1/secret/data/timothy/anthropic", "tok-1", &body)
	if err != nil {
		t.Fatal(err)
	}
	if body.Data.Data["value"] != "sk-abc" {
		t.Errorf("value = %q, want sk-abc", body.Data.Data["value"])
	}

	err = vaultRequest(context.Background(), srv.URL, "/v1/secret/data/timothy/anthropic", "wrong", nil)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("bad token: want 403 error, got %v", err)
	}
}

// TestVaultRequestDoesNotFollowRedirects pins the exfiltration guard:
// a redirecting endpoint must surface as an error, never re-send the
// token elsewhere.
func TestVaultRequestDoesNotFollowRedirects(t *testing.T) {
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed = true
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	err := vaultRequest(context.Background(), srv.URL, "/v1/secret/data/x", "tok", nil)
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Errorf("redirect: want 302 error, got %v", err)
	}
	if followed {
		t.Error("redirect was followed; token would have been re-sent")
	}
}
