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

func TestCipherRoundTrip(t *testing.T) {
	c, err := newCipher(testKey(t))
	if err != nil {
		t.Fatalf("newCipher: %v", err)
	}
	ciphertext, nonce, err := c.seal("sk-super-secret")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(ciphertext, []byte("sk-super-secret")) {
		t.Fatalf("ciphertext leaks plaintext")
	}
	got, err := c.open(ciphertext, nonce)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got != "sk-super-secret" {
		t.Fatalf("got %q, want sk-super-secret", got)
	}
}

func TestCipherWrongKeyFailsToDecrypt(t *testing.T) {
	c1, _ := newCipher(testKey(t))
	c2, _ := newCipher(testKey(t))
	ciphertext, nonce, err := c1.seal("value")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := c2.open(ciphertext, nonce); err == nil {
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
