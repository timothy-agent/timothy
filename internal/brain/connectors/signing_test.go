package connectors

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakeSigningStore is an in-memory signingKeyStore for tests.
type fakeSigningStore struct {
	values map[string]string
}

func newFakeSigningStore() *fakeSigningStore {
	return &fakeSigningStore{values: map[string]string{}}
}

func (f *fakeSigningStore) Resolve(_ context.Context, refName string) (string, error) {
	v, ok := f.values[refName]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}

func (f *fakeSigningStore) Set(_ context.Context, refName, value string) error {
	f.values[refName] = value
	return nil
}

// TestGenerateSigningKeypairRoundTrip proves the private key marshals
// to a parseable OpenSSH PEM and the public key derived from it matches
// the authorized_keys line generateSigningKeypair also returns.
func TestGenerateSigningKeypairRoundTrip(t *testing.T) {
	privatePEM, publicLine, err := generateSigningKeypair()
	if err != nil {
		t.Fatalf("generateSigningKeypair: %v", err)
	}
	if privatePEM == "" || publicLine == "" {
		t.Fatalf("generateSigningKeypair returned empty output: private=%q public=%q", privatePEM, publicLine)
	}

	signer, err := ssh.ParsePrivateKey([]byte(privatePEM))
	if err != nil {
		t.Fatalf("ssh.ParsePrivateKey: %v", err)
	}
	parsedPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicLine))
	if err != nil {
		t.Fatalf("ssh.ParseAuthorizedKey: %v", err)
	}
	if signer.PublicKey().Type() != parsedPub.Type() {
		t.Fatalf("key type mismatch: private-derived %q, public line %q", signer.PublicKey().Type(), parsedPub.Type())
	}
	if string(signer.PublicKey().Marshal()) != string(parsedPub.Marshal()) {
		t.Fatal("public key derived from private key does not match the returned authorized_keys line")
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("key type = %q, want %q", signer.PublicKey().Type(), ssh.KeyAlgoED25519)
	}
}

// TestEnsureSigningKeyGeneratesOnFirstEnable proves enabling
// sign_commits with no existing key generates one, stores the private
// half under the derived ref, and returns the public half in cfg.
func TestEnsureSigningKeyGeneratesOnFirstEnable(t *testing.T) {
	store := newFakeSigningStore()
	cfg := GitKeyConfig{SignCommits: true}

	got, err := EnsureSigningKey(context.Background(), store, "MYCONN_PAT", cfg)
	if err != nil {
		t.Fatalf("EnsureSigningKey: %v", err)
	}
	if got.SigningPublicKey == "" {
		t.Fatal("SigningPublicKey was not populated")
	}
	stored, err := store.Resolve(context.Background(), SigningKeyRefSuffix("MYCONN_PAT"))
	if err != nil {
		t.Fatalf("resolve stored private key: %v", err)
	}
	if _, err := ssh.ParsePrivateKey([]byte(stored)); err != nil {
		t.Fatalf("stored value is not a parseable private key: %v", err)
	}
}

// TestEnsureSigningKeyIdempotent proves a second call never regenerates
// the key — the operator may have already pasted the first public key
// into GitHub, so a silent regeneration would break verification.
func TestEnsureSigningKeyIdempotent(t *testing.T) {
	store := newFakeSigningStore()
	cfg := GitKeyConfig{SignCommits: true}

	first, err := EnsureSigningKey(context.Background(), store, "MYCONN_PAT", cfg)
	if err != nil {
		t.Fatalf("first EnsureSigningKey: %v", err)
	}

	// Simulate a later save (e.g. toggling another field) by calling
	// EnsureSigningKey again with the config it returned.
	second, err := EnsureSigningKey(context.Background(), store, "MYCONN_PAT", first)
	if err != nil {
		t.Fatalf("second EnsureSigningKey: %v", err)
	}
	if second.SigningPublicKey != first.SigningPublicKey {
		t.Fatalf("public key changed across idempotent calls: first %q, second %q", first.SigningPublicKey, second.SigningPublicKey)
	}
}

// TestEnsureSigningKeyNoopWhenDisabled proves a connector with
// sign_commits false is left untouched — no key generated, store
// untouched.
func TestEnsureSigningKeyNoopWhenDisabled(t *testing.T) {
	store := newFakeSigningStore()
	cfg := GitKeyConfig{SignCommits: false}

	got, err := EnsureSigningKey(context.Background(), store, "MYCONN_PAT", cfg)
	if err != nil {
		t.Fatalf("EnsureSigningKey: %v", err)
	}
	if got.SigningPublicKey != "" {
		t.Fatalf("SigningPublicKey = %q, want empty", got.SigningPublicKey)
	}
	if len(store.values) != 0 {
		t.Fatalf("store was written to despite sign_commits=false: %v", store.values)
	}
}

// TestEnsureSigningKeyErrorsOnOrphanedKey proves a config whose public
// key was lost while a store-side private key still exists errors
// rather than silently regenerating (which would orphan the pasted
// GitHub key).
func TestEnsureSigningKeyErrorsOnOrphanedKey(t *testing.T) {
	store := newFakeSigningStore()
	ref := SigningKeyRefSuffix("MYCONN_PAT")
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key for fixture: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal fixture key: %v", err)
	}
	_ = store.Set(context.Background(), ref, string(block.Bytes))

	cfg := GitKeyConfig{SignCommits: true}
	if _, err := EnsureSigningKey(context.Background(), store, "MYCONN_PAT", cfg); err == nil {
		t.Fatal("EnsureSigningKey: want error when a store key exists but config has no public key, got nil")
	}
}

// TestMergeSigningPublicKey proves the write-back keeps every other
// config key — the bug that would have dropped a bitbucket connector's
// workspace the first time signing was enabled on it.
func TestMergeSigningPublicKey(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]any
	}{
		{
			name: "github config with only the signing flag",
			raw:  `{"sign_commits":true}`,
			want: map[string]any{"sign_commits": true, "signing_public_key": "ssh-ed25519 AAAA"},
		},
		{
			name: "bitbucket workspace survives the write-back",
			raw:  `{"sign_commits":true,"workspace":"acme"}`,
			want: map[string]any{"sign_commits": true, "workspace": "acme", "signing_public_key": "ssh-ed25519 AAAA"},
		},
		{
			name: "unknown keys survive too",
			raw:  `{"sign_commits":true,"future_field":"keep"}`,
			want: map[string]any{"sign_commits": true, "future_field": "keep", "signing_public_key": "ssh-ed25519 AAAA"},
		},
		{
			name: "an existing public key is overwritten",
			raw:  `{"sign_commits":true,"signing_public_key":"ssh-ed25519 OLD"}`,
			want: map[string]any{"sign_commits": true, "signing_public_key": "ssh-ed25519 AAAA"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merged, err := MergeSigningPublicKey(json.RawMessage(tt.raw), "ssh-ed25519 AAAA")
			if err != nil {
				t.Fatalf("MergeSigningPublicKey: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(merged, &got); err != nil {
				t.Fatalf("unmarshal merged config: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("merged config = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestMergeSigningPublicKeyRejectsNonObject proves a config that is not
// a JSON object surfaces as an error rather than being replaced.
func TestMergeSigningPublicKeyRejectsNonObject(t *testing.T) {
	if _, err := MergeSigningPublicKey(json.RawMessage(`["not","an","object"]`), "ssh-ed25519 AAAA"); err == nil {
		t.Fatal("MergeSigningPublicKey: want error for a non-object config, got nil")
	}
}

// TestGitConfigsCarrySigningFields proves every git kind's own config
// struct round-trips the signing fields, so a kind that forgets to
// embed GitKeyConfig fails here instead of silently losing signing.
func TestGitConfigsCarrySigningFields(t *testing.T) {
	raw := []byte(`{"sign_commits":true,"signing_public_key":"ssh-ed25519 AAAA","workspace":"acme"}`)
	t.Run("github", func(t *testing.T) {
		var cfg GitHubConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !cfg.SignCommits || cfg.SigningPublicKey != "ssh-ed25519 AAAA" {
			t.Fatalf("GitHubConfig = %+v, want the signing fields decoded", cfg)
		}
	})
	t.Run("bitbucket", func(t *testing.T) {
		var cfg BitbucketConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !cfg.SignCommits || cfg.SigningPublicKey != "ssh-ed25519 AAAA" {
			t.Fatalf("BitbucketConfig = %+v, want the signing fields decoded", cfg)
		}
		if cfg.Workspace != "acme" {
			t.Fatalf("BitbucketConfig.Workspace = %q, want %q", cfg.Workspace, "acme")
		}
	})
}

// TestEnsureSSHKeyGeneratesOnFirstEnable proves enabling ssh_transport
// with no existing key generates one, stores the private half under
// the SSH ref (never the signing ref), and returns the public half.
func TestEnsureSSHKeyGeneratesOnFirstEnable(t *testing.T) {
	store := newFakeSigningStore()
	got, err := EnsureSSHKey(context.Background(), store, "MYCONN_PAT", GitKeyConfig{SSHTransport: true})
	if err != nil {
		t.Fatalf("EnsureSSHKey: %v", err)
	}
	if got.SSHPublicKey == "" {
		t.Fatal("EnsureSSHKey returned no public key")
	}
	stored, ok := store.values[SSHKeyRefSuffix("MYCONN_PAT")]
	if !ok || stored == "" {
		t.Fatalf("no private key stored under %q", SSHKeyRefSuffix("MYCONN_PAT"))
	}
	if _, err := ssh.ParsePrivateKey([]byte(stored)); err != nil {
		t.Fatalf("stored key is not a parseable OpenSSH PEM: %v", err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(got.SSHPublicKey)); err != nil {
		t.Fatalf("public key is not an authorized_keys line: %v", err)
	}
}

// The two keys must never share a ref: revoking transport access has to
// leave historic signatures verifiable.
func TestSSHAndSigningKeysAreSeparate(t *testing.T) {
	store := newFakeSigningStore()
	cfg := GitKeyConfig{SignCommits: true, SSHTransport: true}
	cfg, err := EnsureSigningKey(context.Background(), store, "MYCONN_PAT", cfg)
	if err != nil {
		t.Fatalf("EnsureSigningKey: %v", err)
	}
	cfg, err = EnsureSSHKey(context.Background(), store, "MYCONN_PAT", cfg)
	if err != nil {
		t.Fatalf("EnsureSSHKey: %v", err)
	}
	signingRef, sshRef := SigningKeyRefSuffix("MYCONN_PAT"), SSHKeyRefSuffix("MYCONN_PAT")
	if signingRef == sshRef {
		t.Fatalf("the two refs collide: %q", signingRef)
	}
	if store.values[signingRef] == store.values[sshRef] {
		t.Fatal("the signing and transport keys are the same key")
	}
	if cfg.SigningPublicKey == cfg.SSHPublicKey {
		t.Fatal("the two public keys are identical")
	}
}

func TestEnsureSSHKeyIdempotent(t *testing.T) {
	store := newFakeSigningStore()
	first, err := EnsureSSHKey(context.Background(), store, "MYCONN_PAT", GitKeyConfig{SSHTransport: true})
	if err != nil {
		t.Fatalf("first EnsureSSHKey: %v", err)
	}
	second, err := EnsureSSHKey(context.Background(), store, "MYCONN_PAT", first)
	if err != nil {
		t.Fatalf("second EnsureSSHKey: %v", err)
	}
	if second.SSHPublicKey != first.SSHPublicKey {
		t.Fatal("a second call regenerated the key, invalidating the one already registered with the host")
	}
}

func TestEnsureSSHKeyNoopWhenDisabled(t *testing.T) {
	store := newFakeSigningStore()
	got, err := EnsureSSHKey(context.Background(), store, "MYCONN_PAT", GitKeyConfig{SSHTransport: false})
	if err != nil {
		t.Fatalf("EnsureSSHKey: %v", err)
	}
	if got.SSHPublicKey != "" || len(store.values) != 0 {
		t.Fatalf("a disabled connector got a key: cfg=%+v store=%v", got, store.values)
	}
}

// A store key with no public key in config must never be regenerated
// over, same contract as the signing key's (see EnsureSigningKey).
func TestEnsureSSHKeyErrorsOnOrphanedKey(t *testing.T) {
	store := newFakeSigningStore()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	store.values[SSHKeyRefSuffix("MYCONN_PAT")] = string(block.Bytes)
	if _, err := EnsureSSHKey(context.Background(), store, "MYCONN_PAT", GitKeyConfig{SSHTransport: true}); err == nil {
		t.Fatal("EnsureSSHKey: want an error when a store key exists but config has no public key")
	}
}

// MergePublicKey must leave every other config key intact, the reason
// it merges into raw JSON instead of re-marshaling a decoded struct.
func TestMergePublicKeyPreservesOtherKeys(t *testing.T) {
	raw := json.RawMessage(`{"workspace":"acme-team","sign_commits":true,"signing_public_key":"ssh-ed25519 SIGN"}`)
	merged, err := MergePublicKey(raw, "ssh_public_key", "ssh-ed25519 TRANSPORT")
	if err != nil {
		t.Fatalf("MergePublicKey: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(merged, &out); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	for key, want := range map[string]any{
		"workspace":          "acme-team",
		"sign_commits":       true,
		"signing_public_key": "ssh-ed25519 SIGN",
		"ssh_public_key":     "ssh-ed25519 TRANSPORT",
	} {
		if !reflect.DeepEqual(out[key], want) {
			t.Fatalf("merged[%q] = %v, want %v", key, out[key], want)
		}
	}
}
