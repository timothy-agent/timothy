package auth

import (
	"crypto/ed25519"
	"testing"
	"time"
)

func TestDeriveKeyPair(t *testing.T) {
	masterKey := []byte("01234567890123456789012345678901") // 32 bytes

	kp1, err := DeriveKeyPair(masterKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kp2, err := DeriveKeyPair(masterKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must be deterministic across invocations
	if !ed25519.PublicKey(kp1.PublicKey).Equal(kp2.PublicKey) {
		t.Fatalf("derived public keys do not match across invocations")
	}
	if string(kp1.PQKey) != string(kp2.PQKey) {
		t.Fatalf("derived PQ keys do not match across invocations")
	}
	if len(kp1.PQKey) != 32 {
		t.Fatalf("expected 32-byte PQKey, got %d", len(kp1.PQKey))
	}
	if string(kp1.PQSeed) != string(kp2.PQSeed) {
		t.Fatalf("derived PQ seeds do not match across invocations")
	}
	if len(kp1.PQSeed) != 32 {
		t.Fatalf("expected 32-byte PQSeed, got %d", len(kp1.PQSeed))
	}

	// Short key error
	if _, err := DeriveKeyPair([]byte("short")); err == nil {
		t.Fatalf("expected error for short master key, got nil")
	}
}

func TestSignAndVerifyJWT(t *testing.T) {
	masterKey := []byte("01234567890123456789012345678901")
	kp, err := DeriveKeyPair(masterKey)
	if err != nil {
		t.Fatalf("derive key pair: %v", err)
	}

	claims := Claims{
		Subject:   "11111111-2222-3333-4444-555555555555",
		Username:  "admin",
		Email:     "admin@example.com",
		Role:      "admin",
		ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
	}

	token, err := Sign(claims, kp.PrivateKey)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	if token == "" {
		t.Fatalf("expected non-empty token string")
	}

	// Verify valid token
	got, err := Verify(token, kp.PublicKey)
	if err != nil {
		t.Fatalf("verify valid token: %v", err)
	}
	if got.Subject != claims.Subject || got.Username != claims.Username || got.Email != claims.Email || got.Role != claims.Role {
		t.Fatalf("claims mismatch: got %+v, want %+v", got, claims)
	}

	// Verify using KeyPair.Verify method
	gotKP, err := kp.Verify(token)
	if err != nil {
		t.Fatalf("verify with keypair: %v", err)
	}
	if gotKP.Subject != claims.Subject {
		t.Fatalf("claims mismatch with keypair: got %+v", gotKP)
	}

	// Expired token
	expiredClaims := claims
	expiredClaims.ExpiresAt = time.Now().Add(-1 * time.Hour).Unix()
	expiredToken, err := Sign(expiredClaims, kp.PrivateKey)
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}
	if _, err := Verify(expiredToken, kp.PublicKey); err != ErrExpiredToken {
		t.Fatalf("expected ErrExpiredToken, got %v", err)
	}

	// Tampered token
	tampered := token[:len(token)-5] + "AAAAA"
	if _, err := Verify(tampered, kp.PublicKey); err == nil {
		t.Fatalf("expected error for tampered token, got nil")
	}

	// Wrong public key
	otherKey := []byte("other-key-0123456789012345678901")
	otherKp, _ := DeriveKeyPair(otherKey)
	if _, err := Verify(token, otherKp.PublicKey); err == nil {
		t.Fatalf("expected signature verification failure with different key, got nil")
	}
}

func TestSignAndVerifyPQJWT(t *testing.T) {
	masterKey := []byte("01234567890123456789012345678901")
	kp, err := DeriveKeyPair(masterKey)
	if err != nil {
		t.Fatalf("derive key pair: %v", err)
	}

	claims := Claims{
		Subject:   "22222222-3333-4444-5555-666666666666",
		Username:  "pq-admin",
		Email:     "pq@example.com",
		Role:      "admin",
		ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
	}

	// Sign hybrid PQ token
	token, err := SignPQ(claims, kp)
	if err != nil {
		t.Fatalf("sign pq jwt: %v", err)
	}
	if token == "" {
		t.Fatalf("expected non-empty token string")
	}

	// Verify hybrid PQ token with KeyPair
	got, err := kp.Verify(token)
	if err != nil {
		t.Fatalf("verify valid pq token: %v", err)
	}
	if got.Subject != claims.Subject || got.Username != claims.Username || got.Email != claims.Email {
		t.Fatalf("claims mismatch: got %+v, want %+v", got, claims)
	}

	// Verifying with public key only fails because it lacks the quantum-resistant key
	if _, err := Verify(token, kp.PublicKey); err == nil {
		t.Fatalf("expected error verifying PQ token without quantum key, got nil")
	}

	// Tampered token signature fails
	tampered := token[:len(token)-5] + "AAAAA"
	if _, err := kp.Verify(tampered); err == nil {
		t.Fatalf("expected error for tampered PQ token, got nil")
	}

	// Wrong key fails
	otherKey := []byte("other-key-0123456789012345678901")
	otherKp, _ := DeriveKeyPair(otherKey)
	if _, err := otherKp.Verify(token); err == nil {
		t.Fatalf("expected error for wrong key on PQ token, got nil")
	}

	// Unsupported algorithm fails
	unsupportedToken, err := SignWithAlg(claims, kp, "UNSUPPORTED-ALG")
	if err == nil {
		t.Fatalf("expected error signing with unsupported algorithm")
	}
	_ = unsupportedToken
}
