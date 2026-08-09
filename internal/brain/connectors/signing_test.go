package connectors

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
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
	cfg := GitHubConfig{SignCommits: true}

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
	cfg := GitHubConfig{SignCommits: true}

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
	cfg := GitHubConfig{SignCommits: false}

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

	cfg := GitHubConfig{SignCommits: true}
	if _, err := EnsureSigningKey(context.Background(), store, "MYCONN_PAT", cfg); err == nil {
		t.Fatal("EnsureSigningKey: want error when a store key exists but config has no public key, got nil")
	}
}
