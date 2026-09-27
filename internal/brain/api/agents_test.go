package api

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
)

// TestFailAgentMapsAutomationInUseTo409 covers the shape agents.Delete
// returns when automations still run as the agent (#815, #821).
func TestFailAgentMapsAutomationInUseTo409(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	failAgent(w, discard(), fmt.Errorf("agent is used by automation(s) daily-digest, weekly-report: %w", agents.ErrInUse))
	if w.Code != 409 {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	assertErrorBody(t, w, "in_use", "daily-digest, weekly-report")
}

// TestFailAgentHidesSQLState covers issue #846: an unrecognized store
// error that wraps a raw pg error (e.g. an FK violation the store
// didn't already name) never leaks its SQLSTATE or table internals to
// the API response; ErrInUse still maps to 409 as before.
func TestFailAgentHidesSQLState(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	pgErr := &pgconn.PgError{Code: "23503", TableName: "missions"}
	failAgent(w, discard(), fmt.Errorf("agent delete: %w", pgErr))
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "SQLSTATE") || strings.Contains(body, "23503") {
		t.Fatalf("body leaked SQLSTATE: %s", body)
	}
	assertErrorBody(t, w, "internal_error", "internal error")

	w = httptest.NewRecorder()
	failAgent(w, discard(), fmt.Errorf("agent is used by automation(s) x: %w", agents.ErrInUse))
	if w.Code != 409 {
		t.Fatalf("ErrInUse status = %d, want 409", w.Code)
	}
}
