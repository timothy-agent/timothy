package api

import (
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

// TestRequiredVisionCapabilityDerivesFromMessages confirms the gateway
// derives the vision requirement purely from message content (D-045):
// no explicit flag on streamRequest, so a plain-text turn never adds
// the requirement and a turn with an image always does, regardless of
// which message in the list carries it.
func TestRequiredVisionCapabilityDerivesFromMessages(t *testing.T) {
	t.Parallel()

	textOnly := []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	if got := requiredVisionCapability(textOnly); got != nil {
		t.Fatalf("text-only messages required = %v, want nil", got)
	}

	withImage := []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "user", Content: "look", Images: []provider.ImageData{{MediaType: "image/png", Data: "AAAA"}}},
	}
	got := requiredVisionCapability(withImage)
	if len(got) != 1 || got[0] != provider.CapVision {
		t.Fatalf("required = %v, want [CapVision]", got)
	}
}
