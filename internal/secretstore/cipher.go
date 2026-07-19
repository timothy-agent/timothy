package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

const keyLen = 32 // AES-256

// sealer does AES-256-GCM seal/open with a single master key. There is
// no per-secret key derivation (no DEK layer): the master key IS the
// encryption key, which is simple and sufficient since rotation means
// re-sealing every row with a new key, not re-wrapping a DEK.
type sealer struct {
	gcm cipher.AEAD
}

func newCipher(masterKey []byte) (*sealer, error) {
	if len(masterKey) != keyLen {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", keyLen, len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("build cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build gcm: %w", err)
	}
	return &sealer{gcm: gcm}, nil
}

func (s *sealer) seal(plaintext string) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext = s.gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return ciphertext, nonce, nil
}

func (s *sealer) open(ciphertext, nonce []byte) (string, error) {
	plaintext, err := s.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}
