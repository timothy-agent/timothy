package fxrates

import (
	"testing"
	"time"
)

func TestConvert(t *testing.T) {
	t.Parallel()
	eurAsOf := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	gbpAsOf := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	rates := map[string]Rate{
		"EUR": {Value: 0.86, AsOf: eurAsOf}, // 1 USD = 0.86 EUR
		"GBP": {Value: 0.74, AsOf: gbpAsOf}, // 1 USD = 0.74 GBP
	}

	tests := []struct {
		name       string
		amount     float64
		from, to   string
		rates      map[string]Rate
		wantResult float64
		wantAsOf   time.Time
		wantOK     bool
	}{
		{
			name: "same currency short-circuits without a rate table",
			amount: 42, from: "EUR", to: "EUR", rates: nil,
			wantResult: 42, wantOK: true,
		},
		{
			name: "USD to quote currency", amount: 100, from: "USD", to: "EUR", rates: rates,
			wantResult: 86, wantAsOf: eurAsOf, wantOK: true,
		},
		{
			name: "quote currency to USD", amount: 86, from: "EUR", to: "USD", rates: rates,
			wantResult: 100, wantAsOf: eurAsOf, wantOK: true,
		},
		{
			name: "cross via USD between two quote currencies", amount: 100, from: "EUR", to: "GBP", rates: rates,
			// 100 EUR -> 100/0.86 USD -> *0.74 GBP
			wantResult: 100 / 0.86 * 0.74, wantAsOf: eurAsOf, wantOK: true,
		},
		{
			name: "missing from-rate", amount: 10, from: "JPY", to: "USD", rates: rates,
			wantOK: false,
		},
		{
			name: "missing to-rate", amount: 10, from: "USD", to: "JPY", rates: rates,
			wantOK: false,
		},
		{
			name: "empty table missing rate", amount: 10, from: "EUR", to: "USD", rates: map[string]Rate{},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotResult, gotAsOf, gotOK := Convert(tt.amount, tt.from, tt.to, tt.rates)
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if diff := gotResult - tt.wantResult; diff > 1e-6 || diff < -1e-6 {
				t.Errorf("result = %v, want %v", gotResult, tt.wantResult)
			}
			if !tt.wantAsOf.IsZero() && !gotAsOf.AsOf.Equal(tt.wantAsOf) {
				t.Errorf("asOf = %v, want %v", gotAsOf.AsOf, tt.wantAsOf)
			}
		})
	}
}

func TestConvertCrossUsesOlderLegDate(t *testing.T) {
	t.Parallel()
	older := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	rates := map[string]Rate{
		"EUR": {Value: 0.86, AsOf: newer},
		"GBP": {Value: 0.74, AsOf: older},
	}
	_, asOf, ok := Convert(100, "EUR", "GBP", rates)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !asOf.AsOf.Equal(older) {
		t.Errorf("asOf = %v, want the older leg %v", asOf.AsOf, older)
	}
}
