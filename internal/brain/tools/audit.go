package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// AuditEntry is one tool execution's accountability row.
type AuditEntry struct {
	SessionID  string
	Tool       string
	ArgsDigest string
	Status     string // ok | error | denied
	Duration   time.Duration
	Error      string
}

// Audit writes tool_audit rows. Failures are logged by the caller,
// never fatal — an audit miss must not break the turn.
type Audit struct {
	db *pgpool.Pool
}

func NewAudit(db *pgpool.Pool) *Audit {
	return &Audit{db: db}
}

func (a *Audit) Record(ctx context.Context, e AuditEntry) error {
	db, err := a.db.Get()
	if err != nil {
		return fmt.Errorf("tools: audit: %w", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO tool_audit (session_id, tool, args_digest, status, duration_ms, error)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))`,
		e.SessionID, e.Tool, e.ArgsDigest, e.Status, e.Duration.Milliseconds(), e.Error,
	); err != nil {
		return fmt.Errorf("tools: audit: %w", err)
	}
	return nil
}

// ArgsDigest fingerprints tool arguments for the audit trail without
// storing them raw (they may hold user content).
func ArgsDigest(args []byte) string {
	sum := sha256.Sum256(args)
	return hex.EncodeToString(sum[:8])
}
