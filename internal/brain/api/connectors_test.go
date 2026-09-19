package api

import (
	"context"
	"encoding/json"
	"testing"
)

// TestEnsureRepoSigningKeyNilSecretsPassesThrough covers the degrade
// path when no master key is configured: sign_commits can be set, but
// no keypair is ever generated, and the raw config passes through
// unchanged (not even re-marshaled) regardless of connector kind.
func TestEnsureRepoSigningKeyNilSecretsPassesThrough(t *testing.T) {
	t.Parallel()
	h := &connectorAPI{secrets: nil}
	raw := json.RawMessage(`{"workspace":"acme-team","sign_commits":true}`)
	got, err := h.ensureRepoSigningKey(context.Background(), "ref1", raw)
	if err != nil {
		t.Fatalf("ensureRepoSigningKey with nil secrets: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("nil secrets must pass raw through unchanged: got %s, want %s", got, raw)
	}
}

// TestEnsureRepoSigningKeyEmptyRawPassesThrough covers the other early
// return: an empty config body (nothing to decode) is not an error.
func TestEnsureRepoSigningKeyEmptyRawPassesThrough(t *testing.T) {
	t.Parallel()
	h := &connectorAPI{secrets: nil}
	got, err := h.ensureRepoSigningKey(context.Background(), "ref1", nil)
	if err != nil {
		t.Fatalf("ensureRepoSigningKey with empty raw: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty raw must pass through as empty, got %q", got)
	}
}

// TestEnsureRepoSigningKeyNilSecretsIgnoresConfigShape asserts the
// nil-secrets early return fires regardless of sign_commits or kind,
// including malformed JSON that would otherwise fail to decode: the
// degrade path never even looks at the config body.
func TestEnsureRepoSigningKeyNilSecretsIgnoresConfigShape(t *testing.T) {
	t.Parallel()
	h := &connectorAPI{secrets: nil}
	for _, raw := range []string{
		`{"workspace":"acme-team"}`,
		`{"sign_commits":false}`,
		`{"sign_commits": "not-a-bool"}`,
	} {
		got, err := h.ensureRepoSigningKey(context.Background(), "ref1", json.RawMessage(raw))
		if err != nil {
			t.Fatalf("ensureRepoSigningKey(%s): %v", raw, err)
		}
		if string(got) != raw {
			t.Fatalf("ensureRepoSigningKey(%s) = %s, want unchanged", raw, got)
		}
	}
}
