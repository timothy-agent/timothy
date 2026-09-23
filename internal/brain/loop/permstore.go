package loop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// PGPermStore is PermStore over the pending_permissions table.
type PGPermStore struct {
	db *pgpool.Pool
}

func NewPGPermStore(db *pgpool.Pool) *PGPermStore {
	return &PGPermStore{db: db}
}

func (s *PGPermStore) Insert(ctx context.Context, p PendingPermission) error {
	db, err := s.db.Get()
	if err != nil {
		return fmt.Errorf("loop: pending permission insert: %w", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO pending_permissions
			(id, session_id, mission_id, tool, args, danger, rationale, origin_kind)
		VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5::jsonb, $6, $7, $8)`,
		p.ID, p.SessionID, p.MissionID, p.Tool, string(p.Args), p.Danger, p.Rationale, p.OriginKind); err != nil {
		return fmt.Errorf("loop: pending permission insert: %w", err)
	}
	return nil
}

func (s *PGPermStore) Redeem(ctx context.Context, p PendingPermission, within time.Duration) (string, string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", "", fmt.Errorf("loop: pending permission redeem: %w", err)
	}
	var id, decision string
	err = db.QueryRow(ctx, `UPDATE pending_permissions SET carry_over = false
		WHERE carry_over AND id = (
			SELECT id FROM pending_permissions
			WHERE carry_over
			  AND session_id IS NOT DISTINCT FROM NULLIF($1, '')::uuid
			  AND mission_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid
			  AND tool = $3 AND args = $4::jsonb
			  AND resolved_at > now() - $5::interval
			ORDER BY resolved_at LIMIT 1
			FOR UPDATE SKIP LOCKED)
		RETURNING id, decision`,
		p.SessionID, p.MissionID, p.Tool, string(p.Args), fmt.Sprintf("%d seconds", int64(within.Seconds()))).Scan(&id, &decision)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("loop: pending permission redeem: %w", err)
	}
	return id, decision, nil
}

func (s *PGPermStore) Adopt(ctx context.Context, p PendingPermission, live []string) (string, error) {
	db, err := s.db.Get()
	if err != nil {
		return "", fmt.Errorf("loop: pending permission adopt: %w", err)
	}
	var id string
	err = db.QueryRow(ctx, `SELECT id FROM pending_permissions
		WHERE resolved_at IS NULL
		  AND session_id IS NOT DISTINCT FROM NULLIF($1, '')::uuid
		  AND mission_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid
		  AND tool = $3 AND args = $4::jsonb
		  AND NOT (id = ANY($5))
		ORDER BY created_at LIMIT 1`,
		p.SessionID, p.MissionID, p.Tool, string(p.Args), live).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("loop: pending permission adopt: %w", err)
	}
	return id, nil
}

func (s *PGPermStore) Resolve(ctx context.Context, id, decision string, carry bool) (PendingPermission, bool, error) {
	db, err := s.db.Get()
	if err != nil {
		return PendingPermission{}, false, fmt.Errorf("loop: pending permission resolve: %w", err)
	}
	var p PendingPermission
	err = db.QueryRow(ctx, `UPDATE pending_permissions
		SET resolved_at = now(), decision = $2, carry_over = $3
		WHERE id = $1 AND resolved_at IS NULL
		RETURNING id, COALESCE(session_id::text, ''), COALESCE(mission_id::text, ''), tool, origin_kind`,
		id, decision, carry).Scan(&p.ID, &p.SessionID, &p.MissionID, &p.Tool, &p.OriginKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingPermission{}, false, nil
	}
	if err != nil {
		return PendingPermission{}, false, fmt.Errorf("loop: pending permission resolve: %w", err)
	}
	return p, true, nil
}

func (s *PGPermStore) Expire(ctx context.Context, live []string, chatTimeout time.Duration) (int64, error) {
	db, err := s.db.Get()
	if err != nil {
		return 0, fmt.Errorf("loop: pending permission expire: %w", err)
	}
	tag, err := db.Exec(ctx, `UPDATE pending_permissions p
		SET resolved_at = now(), decision = 'timeout'
		WHERE p.resolved_at IS NULL AND NOT (p.id = ANY($1))
		  AND ((p.mission_id IS NULL AND p.created_at < now() - $2::interval)
		    OR (p.mission_id IS NOT NULL AND NOT EXISTS (
		        SELECT 1 FROM missions m
		        WHERE m.id = p.mission_id AND m.pending_permission->>'id' = p.id)))`,
		live, fmt.Sprintf("%d seconds", int64(chatTimeout.Seconds())))
	if err != nil {
		return 0, fmt.Errorf("loop: pending permission expire: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *PGPermStore) Pending(ctx context.Context) ([]PendingPermission, error) {
	db, err := s.db.Get()
	if err != nil {
		return nil, fmt.Errorf("loop: pending permissions: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT p.id, COALESCE(p.session_id::text, ''), COALESCE(s.title, ''),
			COALESCE(p.mission_id::text, ''), p.tool, p.args, p.danger, p.rationale, p.origin_kind, p.created_at
		FROM pending_permissions p
		LEFT JOIN sessions s ON s.id = p.session_id
		WHERE p.resolved_at IS NULL
		ORDER BY p.created_at`)
	if err != nil {
		return nil, fmt.Errorf("loop: pending permissions: %w", err)
	}
	defer rows.Close()
	out := []PendingPermission{}
	for rows.Next() {
		var p PendingPermission
		var args []byte
		if err := rows.Scan(&p.ID, &p.SessionID, &p.SessionTitle, &p.MissionID, &p.Tool, &args,
			&p.Danger, &p.Rationale, &p.OriginKind, &p.RequestedAt); err != nil {
			return nil, fmt.Errorf("loop: pending permissions scan: %w", err)
		}
		p.Args = args
		out = append(out, p)
	}
	return out, rows.Err()
}
