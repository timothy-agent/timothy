package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// ErrOutputNotFound reports a ref that does not exist or has been
// garbage-collected past its retention window.
var ErrOutputNotFound = errors.New("tool output not found")

// Output is one offloaded tool result.
type Output struct {
	Tool      string
	Content   string
	CreatedAt time.Time
}

// Outputs persists oversized tool results (D-019): the loop stores the
// full content here and hands the model a digest plus the row id;
// retrieve_output returns the content on demand until GC removes it.
type Outputs struct {
	db *pgpool.Pool
}

func NewOutputs(db *pgpool.Pool) *Outputs {
	return &Outputs{db: db}
}

func (o *Outputs) Put(ctx context.Context, sessionID, tool, content string) (string, error) {
	db, err := o.db.Get()
	if err != nil {
		return "", fmt.Errorf("tools: outputs put: %w", err)
	}
	var id string
	if err := db.QueryRow(ctx,
		"INSERT INTO tool_outputs (session_id, tool, content) VALUES ($1, $2, $3) RETURNING id",
		sessionID, tool, content,
	).Scan(&id); err != nil {
		return "", fmt.Errorf("tools: outputs put: %w", err)
	}
	return id, nil
}

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (o *Outputs) Get(ctx context.Context, id string) (Output, error) {
	if !uuidShape.MatchString(id) {
		return Output{}, ErrOutputNotFound
	}
	db, err := o.db.Get()
	if err != nil {
		return Output{}, fmt.Errorf("tools: outputs get: %w", err)
	}
	var out Output
	err = db.QueryRow(ctx,
		"SELECT tool, content, created_at FROM tool_outputs WHERE id = $1", id,
	).Scan(&out.Tool, &out.Content, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Output{}, ErrOutputNotFound
	}
	if err != nil {
		return Output{}, fmt.Errorf("tools: outputs get: %w", err)
	}
	return out, nil
}

// GC deletes outputs older than the retention window and reports how
// many rows went.
func (o *Outputs) GC(ctx context.Context, retention time.Duration) (int64, error) {
	db, err := o.db.Get()
	if err != nil {
		return 0, fmt.Errorf("tools: outputs gc: %w", err)
	}
	tag, err := db.Exec(ctx,
		"DELETE FROM tool_outputs WHERE created_at < now() - $1::interval",
		fmt.Sprintf("%d seconds", int64(retention.Seconds())),
	)
	if err != nil {
		return 0, fmt.Errorf("tools: outputs gc: %w", err)
	}
	return tag.RowsAffected(), nil
}
