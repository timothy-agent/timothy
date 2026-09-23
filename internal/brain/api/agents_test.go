package api

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
)

// TestFailAgentMapsScheduleInUseTo409 covers the shape agents.Delete
// returns when schedule templates still reference the agent (#815).
func TestFailAgentMapsScheduleInUseTo409(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	failAgent(w, fmt.Errorf("agent is used by schedule(s) daily-digest, weekly-report: %w", agents.ErrInUse))
	if w.Code != 409 {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	assertErrorBody(t, w, "in_use", "daily-digest, weekly-report")
}
