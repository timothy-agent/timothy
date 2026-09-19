package connectors

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// GitKeyConfig is the commit-signing slice of a git-hosting
// connector's config, embedded by every kind in GitKinds. SignCommits
// opts the connector into SSH commit signing (D-058): mission commits
// cloned through it are signed with the connector's own ed25519 key.
// SigningPublicKey is the authorized_keys line for that key — public,
// so it lives in the config row (not the secret store) for the UI to
// re-display; the private half is stored in the secret store under a
// ref derived from CredentialRef.
type GitKeyConfig struct {
	SignCommits      bool   `json:"sign_commits,omitempty"`
	SigningPublicKey string `json:"signing_public_key,omitempty"`
}

// GitKinds are the connector kinds that clone, commit and push repos,
// and so carry a GitKeyConfig. Registered here so a new git kind that
// skips a signing call site fails the consistency test in
// secretrefs_test.go rather than silently losing signing.
var GitKinds = map[string]bool{"github": true, "bitbucket": true}

// IsGitKind reports whether kind is a git-hosting connector kind.
func IsGitKind(kind string) bool { return GitKinds[kind] }

// SigningKeyRefSuffix derives the secret-store ref a git-kind
// connector's SSH signing private key is stored under, from its own
// credential_ref — colocated with the token it signs commits alongside,
// never the token's own ref (that would overwrite it on Set).
func SigningKeyRefSuffix(credentialRef string) string {
	return credentialRef + "_SIGNING_KEY"
}

// signingKeyStore is the narrow secret-store slice EnsureSigningKey
// needs: resolve to check idempotency, set to persist a freshly
// generated key. *secretstore.Store satisfies it; kept as an
// interface so this package never imports the concrete store.
type signingKeyStore interface {
	Resolve(ctx context.Context, refName string) (string, error)
	Set(ctx context.Context, refName, value string) error
}

// EnsureSigningKey generates and persists an ed25519 SSH signing
// keypair for a git-kind connector the first time SignCommits is
// enabled, and is a no-op on every call after: idempotent, because
// regenerating would silently invalidate the public key the operator
// already pasted into GitHub. Returns cfg unchanged when SignCommits
// is false, or when a key already exists (SigningPublicKey set, or the
// secret ref already resolves — the belt-and-suspenders check covers a
// config row that lost its public key some other way without losing
// the stored private key). The private key never leaves this
// function's return path as anything but the ref name it was stored
// under; only the public half is ever written back to cfg.
func EnsureSigningKey(ctx context.Context, store signingKeyStore, credentialRef string, cfg GitKeyConfig) (GitKeyConfig, error) {
	if !cfg.SignCommits || cfg.SigningPublicKey != "" {
		return cfg, nil
	}
	ref := SigningKeyRefSuffix(credentialRef)
	if _, err := store.Resolve(ctx, ref); err == nil {
		// A key already exists in the store but cfg lost its public half
		// somehow; never regenerate over it, but there is also no way to
		// re-derive the public key from here without the private key in
		// hand, so surface this as an error rather than guess.
		return cfg, fmt.Errorf("signing: connector %s already has a signing key in the store but no public key in config; refusing to regenerate over it", credentialRef)
	}
	privatePEM, publicLine, err := generateSigningKeypair()
	if err != nil {
		return cfg, fmt.Errorf("signing: generate keypair: %w", err)
	}
	if err := store.Set(ctx, ref, privatePEM); err != nil {
		return cfg, fmt.Errorf("signing: store private key: %w", err)
	}
	cfg.SigningPublicKey = publicLine
	return cfg, nil
}

// MergeSigningPublicKey writes publicKey into raw's signing_public_key
// without decoding raw through any one kind's config struct: every
// other key the row carries (bitbucket's workspace, say) survives the
// round-trip, which re-marshaling a decoded struct would silently drop.
func MergeSigningPublicKey(raw json.RawMessage, publicKey string) (json.RawMessage, error) {
	merged := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &merged); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(publicKey)
	if err != nil {
		return nil, err
	}
	merged["signing_public_key"] = encoded
	return json.Marshal(merged)
}

// generateSigningKeypair creates a fresh ed25519 keypair and returns
// the private half as an OpenSSH PEM (the format ssh-keygen/git expect
// on disk) and the public half as a single authorized_keys line (the
// format GitHub's "SSH and GPG keys -> New SSH key (Signing Key)" form
// expects pasted in).
func generateSigningKeypair() (privatePEM, publicLine string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", "", fmt.Errorf("marshal private key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("derive public key: %w", err)
	}
	return string(pem.EncodeToMemory(block)), string(ssh.MarshalAuthorizedKey(sshPub)), nil
}
