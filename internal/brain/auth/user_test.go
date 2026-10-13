package auth

import (
	"testing"
)

func TestValidation(t *testing.T) {
	// Valid username
	if _, err := ValidateUsername("admin"); err != nil {
		t.Fatalf("unexpected error for valid username: %v", err)
	}
	if _, err := ValidateUsername("user_1.test-name"); err != nil {
		t.Fatalf("unexpected error for valid username: %v", err)
	}

	// Invalid username
	if _, err := ValidateUsername("ab"); err == nil {
		t.Fatalf("expected error for too short username")
	}
	if _, err := ValidateUsername("invalid space"); err == nil {
		t.Fatalf("expected error for username with space")
	}

	// Valid email
	email, err := ValidateEmail("  Admin@Example.COM  ")
	if err != nil {
		t.Fatalf("unexpected error for valid email: %v", err)
	}
	if email != "admin@example.com" {
		t.Fatalf("expected normalized lowercase email, got %s", email)
	}

	// Invalid email
	if _, err := ValidateEmail("not-an-email"); err == nil {
		t.Fatalf("expected error for invalid email")
	}

	// Password validation
	if err := ValidatePassword("validPassword123"); err != nil {
		t.Fatalf("unexpected error for valid password: %v", err)
	}
	if err := ValidatePassword("short"); err == nil {
		t.Fatalf("expected error for short password")
	}
}
