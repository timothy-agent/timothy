package secretstore

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return key
}

// sealLegacy produces a pre-D-105 ciphertext (no additional data), the
// format existing rows carry before their first read re-seals them.
func sealLegacy(t *testing.T, c *sealer, plaintext string) (ciphertext, nonce []byte) {
	t.Helper()
	nonce = make([]byte, c.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return c.gcm.Seal(nil, nonce, []byte(plaintext), nil), nonce
}

func TestCipherRoundTrip(t *testing.T) {
	c, err := newCipher(testKey(t))
	if err != nil {
		t.Fatalf("newCipher: %v", err)
	}
	ciphertext, nonce, err := c.seal("OPENAI_KEY", "sk-super-secret")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(ciphertext, []byte("sk-super-secret")) {
		t.Fatalf("ciphertext leaks plaintext")
	}
	got, err := c.open("OPENAI_KEY", ciphertext, nonce)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got != "sk-super-secret" {
		t.Fatalf("got %q, want sk-super-secret", got)
	}
}

// TestCipherOpenBindsRefName covers D-105: a ciphertext only opens
// under the ref_name it was sealed for, a legacy row only through
// openLegacy, and every failure is a plain error, never a panic.
func TestCipherOpenBindsRefName(t *testing.T) {
	c, err := newCipher(testKey(t))
	if err != nil {
		t.Fatalf("newCipher: %v", err)
	}
	cases := []struct {
		name    string
		seal    func() ([]byte, []byte)
		open    func(ct, nonce []byte) (string, error)
		wantErr bool
	}{
		{
			name: "same ref opens",
			seal: func() ([]byte, []byte) { ct, n, _ := c.seal("HIGH_VALUE", "v"); return ct, n },
			open: func(ct, n []byte) (string, error) { return c.open("HIGH_VALUE", ct, n) },
		},
		{
			name:    "swapped onto another ref fails",
			seal:    func() ([]byte, []byte) { ct, n, _ := c.seal("HIGH_VALUE", "v"); return ct, n },
			open:    func(ct, n []byte) (string, error) { return c.open("LOW_VALUE", ct, n) },
			wantErr: true,
		},
		{
			name:    "empty ref is not a wildcard",
			seal:    func() ([]byte, []byte) { ct, n, _ := c.seal("HIGH_VALUE", "v"); return ct, n },
			open:    func(ct, n []byte) (string, error) { return c.open("", ct, n) },
			wantErr: true,
		},
		{
			name:    "aad row does not open as legacy",
			seal:    func() ([]byte, []byte) { ct, n, _ := c.seal("HIGH_VALUE", "v"); return ct, n },
			open:    c.openLegacy,
			wantErr: true,
		},
		{
			name:    "legacy row does not open with aad",
			seal:    func() ([]byte, []byte) { return sealLegacy(t, c, "v") },
			open:    func(ct, n []byte) (string, error) { return c.open("HIGH_VALUE", ct, n) },
			wantErr: true,
		},
		{
			name: "legacy row opens as legacy",
			seal: func() ([]byte, []byte) { return sealLegacy(t, c, "v") },
			open: c.openLegacy,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ct, nonce := tc.seal()
			got, err := tc.open(ct, nonce)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("open succeeded with %q, want auth failure", got)
				}
				return
			}
			if err != nil || got != "v" {
				t.Fatalf("open = (%q, %v), want (v, nil)", got, err)
			}
		})
	}
}

func TestCipherWrongKeyFailsToDecrypt(t *testing.T) {
	c1, _ := newCipher(testKey(t))
	c2, _ := newCipher(testKey(t))
	ciphertext, nonce, err := c1.seal("REF", "value")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := c2.open("REF", ciphertext, nonce); err == nil {
		t.Fatalf("expected decrypt failure with wrong key")
	}
}

func TestNewCipherRejectsBadKeyLength(t *testing.T) {
	if _, err := newCipher([]byte("too-short")); err == nil {
		t.Fatalf("expected error for short key")
	}
}

func TestDecodeMasterKey(t *testing.T) {
	if _, err := DecodeMasterKey(""); err == nil {
		t.Fatalf("expected error for empty key")
	}
	if _, err := DecodeMasterKey("not-valid-base64!!!"); err == nil {
		t.Fatalf("expected error for invalid base64")
	}
	if _, err := DecodeMasterKey("c2hvcnQ="); err == nil {
		t.Fatalf("expected error for wrong decoded length")
	}

	key := testKey(t)
	encoded := base64.StdEncoding.EncodeToString(key)
	got, err := DecodeMasterKey(encoded)
	if err != nil {
		t.Fatalf("DecodeMasterKey: %v", err)
	}
	if !bytes.Equal(got, key) {
		t.Fatalf("round trip mismatch")
	}
}
