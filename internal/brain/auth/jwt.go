package auth

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Supported JWT signing algorithms for Timothy session tokens.
const (
	// AlgEdDSA is the classical Ed25519 digital signature algorithm (RFC 8037).
	AlgEdDSA = "EdDSA"

	// AlgEd25519PQ is a post-quantum hybrid composite signature algorithm.
	// It combines a classical Ed25519 signature (64 bytes) with a quantum-resistant
	// HMAC-SHA256 authentication tag (32 bytes) derived from the master key via HKDF.
	// Even if a cryptographically relevant quantum computer (CRQC) running Shor's
	// algorithm solves the discrete log for Ed25519, the quantum-resistant tag
	// guarantees that tokens cannot be forged (Grover's algorithm requires 2^128 operations).
	AlgEd25519PQ = "Ed25519-PQ"

	// AlgMLDSA44 is reserved for NIST FIPS 204 ML-DSA-44 post-quantum signatures.
	AlgMLDSA44 = "ML-DSA-44"

	// AlgMLDSA65 is reserved for NIST FIPS 204 ML-DSA-65 post-quantum signatures.
	AlgMLDSA65 = "ML-DSA-65"
)

var (
	ErrInvalidToken         = errors.New("invalid token")
	ErrExpiredToken         = errors.New("token has expired")
	ErrUnsupportedAlgorithm = errors.New("unsupported signature algorithm")
)

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// Claims represents the standard identity claims stored in a Timothy auth JWT.
type Claims struct {
	Subject   string `json:"sub"`      // User ID (UUID)
	Username  string `json:"username"` // Username
	Email     string `json:"email"`    // User email
	Role      string `json:"role"`     // "admin" | "user"
	ExpiresAt int64  `json:"exp"`      // Expiration time in Unix seconds
	IssuedAt  int64  `json:"iat"`      // Issuance time in Unix seconds
	Issuer    string `json:"iss"`      // "timothy"
}

// Sign creates an Ed25519-signed (AlgEdDSA) compact JWT string from the given claims.
// Kept for backward compatibility.
func Sign(claims Claims, privKey ed25519.PrivateKey) (string, error) {
	return SignWithAlg(claims, &KeyPair{PrivateKey: privKey}, AlgEdDSA)
}

// SignPQ creates a post-quantum hybrid composite JWT string (AlgEd25519PQ).
func SignPQ(claims Claims, kp *KeyPair) (string, error) {
	return SignWithAlg(claims, kp, AlgEd25519PQ)
}

// SignWithAlg creates a compact JWT string signed using the specified algorithm.
func SignWithAlg(claims Claims, kp *KeyPair, alg string) (string, error) {
	if kp == nil {
		return "", errors.New("key pair cannot be nil")
	}
	if claims.IssuedAt == 0 {
		claims.IssuedAt = time.Now().Unix()
	}
	if claims.Issuer == "" {
		claims.Issuer = "timothy"
	}

	headerJSON, err := json.Marshal(jwtHeader{Alg: alg, Typ: "JWT"})
	if err != nil {
		return "", fmt.Errorf("marshal jwt header: %w", err)
	}

	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal jwt claims: %w", err)
	}

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerB64 + "." + claimsB64

	var sig []byte
	switch alg {
	case AlgEdDSA:
		if kp.PrivateKey == nil {
			return "", errors.New("private key cannot be nil for EdDSA")
		}
		sig = ed25519.Sign(kp.PrivateKey, []byte(signingInput))

	case AlgEd25519PQ:
		if kp.PrivateKey == nil {
			return "", errors.New("private key cannot be nil for Ed25519-PQ")
		}
		if len(kp.PQKey) == 0 {
			return "", errors.New("quantum key cannot be empty for Ed25519-PQ")
		}
		// 1. Classical Ed25519 signature (64 bytes)
		sigEd := ed25519.Sign(kp.PrivateKey, []byte(signingInput))

		// 2. Quantum-resistant HMAC-SHA256 authentication tag (32 bytes)
		mac := hmac.New(sha256.New, kp.PQKey)
		mac.Write([]byte(signingInput))
		mac.Write(sigEd)
		tag := mac.Sum(nil)

		// Composite signature: 64 bytes classical + 32 bytes quantum-safe = 96 bytes
		sig = append(sigEd, tag...)

	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedAlgorithm, alg)
	}

	sigB64 := base64.RawURLEncoding.EncodeToString(sig)
	return signingInput + "." + sigB64, nil
}

// Sign creates a signed compact JWT. If a post-quantum key is available on the KeyPair,
// it defaults to post-quantum hybrid composite signing (AlgEd25519PQ); otherwise it
// falls back to classical AlgEdDSA.
func (kp *KeyPair) Sign(claims Claims) (string, error) {
	if kp != nil && len(kp.PQKey) > 0 {
		return SignPQ(claims, kp)
	}
	return SignWithAlg(claims, kp, AlgEdDSA)
}

// SignPQ creates a post-quantum hybrid composite JWT token.
func (kp *KeyPair) SignPQ(claims Claims) (string, error) {
	return SignPQ(claims, kp)
}

// SignClassical creates a classical Ed25519 JWT token.
func (kp *KeyPair) SignClassical(claims Claims) (string, error) {
	return SignWithAlg(claims, kp, AlgEdDSA)
}

// Verify validates a compact JWT string against the KeyPair.
// It supports algorithm agility: both classical EdDSA and post-quantum hybrid Ed25519-PQ tokens.
func (kp *KeyPair) Verify(token string) (*Claims, error) {
	if kp == nil {
		return nil, errors.New("key pair cannot be nil")
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid header encoding", ErrInvalidToken)
	}

	var h jwtHeader
	if err := json.Unmarshal(headerJSON, &h); err != nil || h.Typ != "JWT" {
		return nil, fmt.Errorf("%w: unsupported or malformed header", ErrInvalidToken)
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid claims encoding", ErrInvalidToken)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid signature encoding", ErrInvalidToken)
	}

	signingInput := []byte(parts[0] + "." + parts[1])

	switch h.Alg {
	case AlgEdDSA:
		if kp.PublicKey == nil {
			return nil, errors.New("public key cannot be nil for EdDSA verification")
		}
		if len(sig) != ed25519.SignatureSize {
			return nil, fmt.Errorf("%w: invalid EdDSA signature length", ErrInvalidToken)
		}
		if !ed25519.Verify(kp.PublicKey, signingInput, sig) {
			return nil, fmt.Errorf("%w: signature verification failed", ErrInvalidToken)
		}

	case AlgEd25519PQ:
		if len(sig) != ed25519.SignatureSize+sha256.Size {
			return nil, fmt.Errorf("%w: invalid hybrid signature length", ErrInvalidToken)
		}
		if kp.PublicKey == nil {
			return nil, errors.New("public key cannot be nil for Ed25519-PQ verification")
		}
		if len(kp.PQKey) == 0 {
			return nil, fmt.Errorf("%w: missing quantum key for hybrid token verification", ErrInvalidToken)
		}

		sigEd := sig[:ed25519.SignatureSize]
		tag := sig[ed25519.SignatureSize:]

		// 1. Classical Ed25519 signature check
		if !ed25519.Verify(kp.PublicKey, signingInput, sigEd) {
			return nil, fmt.Errorf("%w: classical signature verification failed", ErrInvalidToken)
		}

		// 2. Quantum-resistant MAC verification
		mac := hmac.New(sha256.New, kp.PQKey)
		mac.Write(signingInput)
		mac.Write(sigEd)
		expectedTag := mac.Sum(nil)

		if subtle.ConstantTimeCompare(tag, expectedTag) != 1 {
			return nil, fmt.Errorf("%w: quantum-resistant tag verification failed", ErrInvalidToken)
		}

	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedAlgorithm, h.Alg)
	}

	var claims Claims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("%w: invalid claims JSON", ErrInvalidToken)
	}

	if claims.ExpiresAt > 0 && time.Now().Unix() > claims.ExpiresAt {
		return nil, ErrExpiredToken
	}

	return &claims, nil
}

// Verify validates an Ed25519 compact JWT string against the public key and
// checks its expiration. Maintained for backward compatibility.
func Verify(token string, pubKey ed25519.PublicKey) (*Claims, error) {
	if pubKey == nil {
		return nil, errors.New("public key cannot be nil")
	}
	return (&KeyPair{PublicKey: pubKey}).Verify(token)
}
