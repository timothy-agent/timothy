package auth

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrNotFound           = errors.New("user not found")
	ErrUserExists         = errors.New("user with this username or email already exists")
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidRecovery    = errors.New("invalid or expired recovery token")
	ErrInvalidInput       = errors.New("invalid input")
)

// User represents a local user account.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"` // "admin" | "user"
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ValidateUsername ensures username is 3..32 chars, alphanumeric plus underscores or hyphens.
func ValidateUsername(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: username is required", ErrInvalidInput)
	}
	if utf8.RuneCountInString(name) < 3 || utf8.RuneCountInString(name) > 32 {
		return "", fmt.Errorf("%w: username must be between 3 and 32 characters", ErrInvalidInput)
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' {
			return "", fmt.Errorf("%w: username may only contain letters, numbers, hyphens, dots, and underscores", ErrInvalidInput)
		}
	}
	return name, nil
}

// ValidateEmail checks email format and normalizes to lowercase.
func ValidateEmail(email string) (string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return "", fmt.Errorf("%w: email is required", ErrInvalidInput)
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "", fmt.Errorf("%w: invalid email address format", ErrInvalidInput)
	}
	return email, nil
}

// ValidatePassword ensures password meets minimum complexity (at least 8 chars).
func ValidatePassword(pass string) error {
	if len(pass) < 8 {
		return fmt.Errorf("%w: password must be at least 8 characters", ErrInvalidInput)
	}
	if len(pass) > 128 {
		return fmt.Errorf("%w: password must not exceed 128 characters", ErrInvalidInput)
	}
	return nil
}
