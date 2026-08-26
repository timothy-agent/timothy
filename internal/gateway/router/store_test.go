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

func TestApplyProviderOptionsReasoningEffortByModel(t *testing.T) {
	t.Parallel()
	var row ProviderRow
	optsJSON := `{"reasoning_effort": "low", "reasoning_effort_by_model": "{\"gpt-5.6-luna\": \"none\"}"}`
	if err := applyProviderOptions(&row, []byte(optsJSON)); err != nil {
		t.Fatalf("applyProviderOptions: %v", err)
	}
	if row.ReasoningEffort != "low" {
		t.Fatalf("ReasoningEffort = %q, want low", row.ReasoningEffort)
	}
	if row.ReasoningEffortByModel["gpt-5.6-luna"] != "none" {
		t.Fatalf("ReasoningEffortByModel[gpt-5.6-luna] = %q, want none", row.ReasoningEffortByModel["gpt-5.6-luna"])
	}
}

func TestApplyProviderOptionsReasoningEffortByModelBadJSONFails(t *testing.T) {
	t.Parallel()
	var row ProviderRow
	err := applyProviderOptions(&row, []byte(`{"reasoning_effort_by_model": "not json"}`))
	if err == nil {
		t.Fatal("applyProviderOptions: want error for malformed reasoning_effort_by_model, got nil")
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

// TestApplyProviderOptionsPricesByModel covers operator-declared prices
// (D-079): the value is a JSON-encoded object string like its
// reasoning_effort_by_model sibling, and every rate is validated on
// load because these numbers are recorded as real spend. Zero stays
// legal — genuinely free models exist.
func TestApplyProviderOptionsPricesByModel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		options string
		wantErr bool
		check   func(*testing.T, ProviderRow)
	}{
		{
			name:    "full price set decodes",
			options: `{"prices_by_model": "{\"glm-5.3\":{\"input_per_mtok\":1.4,\"output_per_mtok\":4.4,\"cache_read_per_mtok\":0.26}}"}`,
			check: func(t *testing.T, row ProviderRow) {
				p, ok := row.PricesByModel["glm-5.3"]
				if !ok {
					t.Fatal("glm-5.3 missing from PricesByModel")
				}
				if p.InputPerMTok != 1.4 || p.OutputPerMTok != 4.4 || p.CacheReadPerMTok != 0.26 {
					t.Fatalf("prices = %+v, want 1.4/4.4/0.26", p)
				}
			},
		},
		{
			name:    "zero is a legal price",
			options: `{"prices_by_model": "{\"glm-4.7-flash\":{\"input_per_mtok\":0,\"output_per_mtok\":0}}"}`,
			check: func(t *testing.T, row ProviderRow) {
				if _, ok := row.PricesByModel["glm-4.7-flash"]; !ok {
					t.Fatal("glm-4.7-flash missing from PricesByModel")
				}
			},
		},
		{
			name:    "absent leaves the map nil",
			options: `{"reasoning_effort": "low"}`,
			check: func(t *testing.T, row ProviderRow) {
				if row.PricesByModel != nil {
					t.Fatalf("PricesByModel = %v, want nil when unset", row.PricesByModel)
				}
			},
		},
		{name: "malformed json fails", options: `{"prices_by_model": "not json"}`, wantErr: true},
		{
			name:    "negative price fails",
			options: `{"prices_by_model": "{\"m\":{\"input_per_mtok\":-1}}"}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var row ProviderRow
			err := applyProviderOptions(&row, []byte(tt.options))
			if tt.wantErr {
				if err == nil {
					t.Fatal("applyProviderOptions: want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("applyProviderOptions: %v", err)
			}
			tt.check(t, row)
		})
	}
}
