package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// Domain separators for HKDF-SHA256 deterministic key derivation.
// HKDF with 256-bit symmetric secrets provides 128-bit quantum security against Grover's algorithm.
var (
	infoEd25519      = []byte("timothy-auth-jwt-ed25519")
	infoQuantumLayer = []byte("timothy-auth-jwt-pq-layer-v1")
	infoPQSeed       = []byte("timothy-auth-jwt-pq-seed-v1")
)

// KeyPair holds cryptographic keys for authentication. It supports classical
// Ed25519 signatures as well as post-quantum hybrid composite signatures.
type KeyPair struct {
	// Classical Ed25519 key pair (RFC 8037).
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey

	// PQKey is a 32-byte quantum-resistant symmetric authentication key derived
	// via HKDF-SHA256. In the Ed25519-PQ hybrid composite scheme, this protects
	// against forgery even if Shor's algorithm on a quantum computer breaks Ed25519.
	PQKey []byte

	// PQSeed is a 32-byte seed derived via HKDF-SHA256, reserved for deterministic
	// NIST FIPS 204 (ML-DSA) key generation.
	PQSeed []byte
}

// DeriveKeyPair deterministically derives cryptographic signing keys from
// the 32-byte master key using HKDF-SHA256 with domain separation.
// Keys survive restarts without requiring new configuration or disk state.
func DeriveKeyPair(masterKey []byte) (*KeyPair, error) {
	if len(masterKey) < 32 {
		return nil, fmt.Errorf("master key must be at least 32 bytes, got %d", len(masterKey))
	}

	// 1. Derive classical Ed25519 key pair
	rEd := hkdf.New(sha256.New, masterKey, nil, infoEd25519)
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(rEd, seed); err != nil {
		return nil, fmt.Errorf("derive ed25519 seed: %w", err)
	}
	privKey := ed25519.NewKeyFromSeed(seed)
	pubKey := privKey.Public().(ed25519.PublicKey)

	// 2. Derive post-quantum resistant symmetric layer key (for hybrid composite JWTs)
	rPQ := hkdf.New(sha256.New, masterKey, nil, infoQuantumLayer)
	pqKey := make([]byte, 32)
	if _, err := io.ReadFull(rPQ, pqKey); err != nil {
		return nil, fmt.Errorf("derive pq layer key: %w", err)
	}

	// 3. Derive post-quantum seed (for FIPS 204 ML-DSA key generation)
	rSeed := hkdf.New(sha256.New, masterKey, nil, infoPQSeed)
	pqSeed := make([]byte, 32)
	if _, err := io.ReadFull(rSeed, pqSeed); err != nil {
		return nil, fmt.Errorf("derive pq seed: %w", err)
	}

	return &KeyPair{
		PrivateKey: privKey,
		PublicKey:  pubKey,
		PQKey:      pqKey,
		PQSeed:     pqSeed,
	}, nil
}
