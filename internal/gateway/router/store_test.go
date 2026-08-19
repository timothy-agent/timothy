package router

import (
	"strings"
	"testing"
	"time"
)

func TestApplyProviderOptionsRequestTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		options string
		want    time.Duration
		wantErr string
	}{
		{name: "parses a valid duration", options: `{"request_timeout": "20m"}`, want: 20 * time.Minute},
		{name: "parses seconds", options: `{"request_timeout": "300s"}`, want: 300 * time.Second},
		{name: "absent leaves zero", options: `{}`, want: 0},
		{name: "empty string leaves zero", options: `{"request_timeout": ""}`, want: 0},
		{name: "invalid duration errors", options: `{"request_timeout": "banana"}`, wantErr: "request_timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var row ProviderRow
			err := applyProviderOptions(&row, []byte(tt.options))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyProviderOptions: %v", err)
			}
			if row.Timeout != tt.want {
				t.Fatalf("Timeout = %v, want %v", row.Timeout, tt.want)
			}
		})
	}
}

func TestApplyProviderOptionsReasoningEffort(t *testing.T) {
	t.Parallel()
	var row ProviderRow
	if err := applyProviderOptions(&row, []byte(`{"reasoning_effort": "none"}`)); err != nil {
		t.Fatalf("applyProviderOptions: %v", err)
	}
	if row.ReasoningEffort != "none" {
		t.Fatalf("ReasoningEffort = %q, want none", row.ReasoningEffort)
	}
}

// TestApplyProviderOptionsOpenAIResponses covers the tri-state
// openai_responses flag (D-051 follow-up): absent must leave
// OpenAIResponses nil (unknown, never guessed), "true"/"false" set a
// definite *bool, and anything else fails the load outright — the only
// writers (Admin.Test/Patch) never write anything but those two literals.
func TestApplyProviderOptionsOpenAIResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		options string
		want    *bool
		wantErr string
	}{
		{name: "absent leaves nil", options: `{}`, want: nil},
		{name: "true sets true", options: `{"openai_responses": "true"}`, want: boolPtr(true)},
		{name: "false sets false", options: `{"openai_responses": "false"}`, want: boolPtr(false)},
		{name: "invalid value errors", options: `{"openai_responses": "yes"}`, wantErr: "openai_responses"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var row ProviderRow
			err := applyProviderOptions(&row, []byte(tt.options))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyProviderOptions: %v", err)
			}
			if tt.want == nil {
				if row.OpenAIResponses != nil {
					t.Fatalf("OpenAIResponses = %v, want nil", *row.OpenAIResponses)
				}
				return
			}
			if row.OpenAIResponses == nil || *row.OpenAIResponses != *tt.want {
				t.Fatalf("OpenAIResponses = %v, want %v", row.OpenAIResponses, *tt.want)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }
