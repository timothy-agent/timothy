package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// Store handles database persistence for users and recovery credentials.
type Store struct {
	db  *pgpool.Pool
	log *slog.Logger
}

// NewStore creates a new auth store.
func NewStore(db *pgpool.Pool, log *slog.Logger) *Store {
	return &Store{db: db, log: log}
}

// HasUsers checks if at least one user account exists in the database.
func (s *Store) HasUsers(ctx context.Context) (bool, error) {
	db, err := s.db.Get()
	if err != nil {
		return false, fmt.Errorf("auth: %w", err)
	}
	var exists bool
	err = db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users)").Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check users existence: %w", err)
	}
	return exists, nil
}

// CreateUser creates a new user account with hashed password.
func (s *Store) CreateUser(ctx context.Context, username, email, password, role string) (*User, error) {
	validName, err := ValidateUsername(username)
	if err != nil {
		return nil, err
	}
	validEmail, err := ValidateEmail(email)
	if err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	if role == "" {
		role = "admin"
	}
	if role != "admin" && role != "user" {
		return nil, fmt.Errorf("%w: role must be 'admin' or 'user'", ErrInvalidInput)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}

	const q = `
		INSERT INTO users (username, email, password_hash, role)
		VALUES ($1, $2, $3, $4)
		RETURNING id, username, email, password_hash, role, created_at, updated_at
	`
	row := db.QueryRow(ctx, q, validName, validEmail, string(hash), role)
	u, err := scanUser(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return nil, ErrUserExists
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}

	return u, nil
}

// Authenticate checks user credentials against stored bcrypt hash.
func (s *Store) Authenticate(ctx context.Context, identifier, password string) (*User, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" || password == "" {
		return nil, ErrInvalidCredentials
	}

	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}

	const q = `
		SELECT id, username, email, password_hash, role, created_at, updated_at
		FROM users
		WHERE lower(username) = lower($1) OR lower(email) = lower($1)
		LIMIT 1
	`
	row := db.QueryRow(ctx, q, identifier)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("query user for auth: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	return u, nil
}

// GetUser retrieves a user by ID.
func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}

	const q = `
		SELECT id, username, email, password_hash, role, created_at, updated_at
		FROM users
		WHERE id = $1
	`
	row := db.QueryRow(ctx, q, id)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get user %s: %w", id, err)
	}
	return u, nil
}

// GenerateRecoveryToken creates a secure single-use token valid for 15 minutes.
func (s *Store) GenerateRecoveryToken(ctx context.Context, identifier string) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	plainToken := hex.EncodeToString(tokenBytes)
	tokenHash := sha256Hex(plainToken)

	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("auth: %w", err)
	}

	const q = `
		UPDATE users
		SET recovery_token = $1, recovery_exp = now() + INTERVAL '15 minutes', updated_at = now()
		WHERE lower(username) = lower($2) OR lower(email) = lower($2)
	`
	tag, err := db.Exec(ctx, q, tokenHash, strings.TrimSpace(identifier))
	if err != nil {
		return "", fmt.Errorf("update recovery token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}

	return plainToken, nil
}

// ResetPasswordWithToken consumes a valid recovery token and sets a new password.
func (s *Store) ResetPasswordWithToken(ctx context.Context, plainToken, newPassword string) error {
	plainToken = strings.TrimSpace(plainToken)
	if plainToken == "" {
		return ErrInvalidRecovery
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}

	tokenHash := sha256Hex(plainToken)
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash new password: %w", err)
	}

	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	const q = `
		UPDATE users
		SET password_hash = $1, recovery_token = NULL, recovery_exp = NULL, updated_at = now()
		WHERE recovery_token = $2 AND recovery_exp > now()
	`
	tag, err := db.Exec(ctx, q, string(newHash), tokenHash)
	if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidRecovery
	}

	return nil
}

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
