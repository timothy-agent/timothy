//go:build integration

package provider

import (
	"os"
	"testing"
)

// TestLayaLiveRoundTrip runs one decision of each kind against a real
// laya sidecar named by LAYA_URL (e.g. http://127.0.0.1:18000 for a
// locally built laya-svc image); skipped when unset.
func TestLayaLiveRoundTrip(t *testing.T) {
	base := os.Getenv("LAYA_URL")
	if base == "" {
		t.Skip("LAYA_URL not set; skipping laya live test")
	}
	l := NewLaya(LayaConfig{Name: "laya", BaseURL: base})

	models, err := l.ListModels(t.Context())
	if err != nil || len(models) == 0 {
		t.Fatalf("ListModels = %v, %v", models, err)
	}

	res, usage, err := l.Decide(t.Context(), "typed-decisions", DecideRequest{
		Kind: DecideChoice, Question: "Which department should handle this request?",
		Context: "I was charged twice for my subscription. Please refund the duplicate charge.",
		Options: []string{"billing", "technical", "sales"},
		Descriptions: map[string]string{
			"billing": "invoices, payments, refunds", "technical": "bugs, outages, system errors", "sales": "pricing, new contracts",
		},
	})
	if err != nil {
		t.Fatalf("choice: %v", err)
	}
	if res.Choice != "billing" || res.Scores["billing"] <= res.Scores["sales"] {
		t.Fatalf("choice res = %+v, want billing on top", res)
	}
	if usage == nil || usage.InputTokens == 0 {
		t.Fatalf("usage = %+v", usage)
	}

	res, _, err = l.Decide(t.Context(), "typed-decisions", DecideRequest{
		Kind: DecideYesNo, Question: "Does the user explicitly request a refund?",
		Context: "I was charged twice for my subscription. Please refund the duplicate charge.",
		Options: []string{"yes", "no"},
	})
	if err != nil || res.Choice != "yes" {
		t.Fatalf("yesno res = %+v err = %v", res, err)
	}

	res, _, err = l.Decide(t.Context(), "typed-decisions", DecideRequest{
		Kind: DecideScore, Question: "The user is asking about the weather.",
		Context: "I was charged twice for my subscription. Please refund the duplicate charge.",
	})
	if err != nil || res.Score == nil || *res.Score > 0.5 {
		t.Fatalf("score res = %+v err = %v", res, err)
	}
}
