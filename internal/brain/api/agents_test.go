package api

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/agents"
)

// TestFailAgentMapsAutomationInUseTo409 covers the shape agents.Delete
// returns when automations still run as the agent (#815, #821).
func TestFailAgentMapsAutomationInUseTo409(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	failAgent(w, fmt.Errorf("agent is used by automation(s) daily-digest, weekly-report: %w", agents.ErrInUse))
	if w.Code != 409 {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	assertErrorBody(t, w, "in_use", "daily-digest, weekly-report")
}
