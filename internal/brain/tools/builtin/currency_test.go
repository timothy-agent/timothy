package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConvertCurrencyTableHit(t *testing.T) {
	t.Parallel()
	lookup := func(ctx context.Context, from, to string) (float64, string, bool, error) {
		if from != "EUR" || to != "USD" {
			t.Errorf("lookup(from=%s, to=%s), want EUR/USD", from, to)
		}
		return 1.17325, "2026-07-21", true, nil
	}
	// baseURL points nowhere reachable: a table hit must never fall
	// through to the live fetch.
	tool := newCurrencyConverter(lookup, http.DefaultClient, "http://unused.invalid")
	args, _ := json.Marshal(map[string]any{"amount": 112, "from": "EUR", "to": "USD"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"112.00 EUR", "131.40 USD", "2026-07-21"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestConvertCurrencyFallsBackWhenTableMisses(t *testing.T) {
	t.Parallel()
	called := false
	lookup := func(ctx context.Context, from, to string) (float64, string, bool, error) {
		called = true
		return 0, "", false, nil // no stored rate for this pair
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"success","base_code":"USD","rates":{"USD":1,"EUR":0.856,"USD_CHECK":0}}`))
	}))
	defer srv.Close()

	tool := newCurrencyConverter(lookup, srv.Client(), srv.URL)
	args, _ := json.Marshal(map[string]any{"amount": 100, "from": "USD", "to": "EUR"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !called {
		t.Fatal("expected the table lookup to be tried before falling back")
	}
	if !strings.Contains(out, "85.60 EUR") {
		t.Fatalf("output = %q, want the live-fetched rate applied", out)
	}
}

func TestConvertCurrencyNilLookupGoesStraightToLiveFetch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"success","base_code":"USD","rates":{"USD":1,"EUR":0.856}}`))
	}))
	defer srv.Close()

	// nil lookup: table access unwired (e.g. fxrates not built) — the
	// tool must still work via the live fetch.
	tool := newCurrencyConverter(nil, srv.Client(), srv.URL)
	args, _ := json.Marshal(map[string]any{"amount": 100, "from": "USD", "to": "EUR"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "85.60 EUR") {
		t.Fatalf("output = %q, want the live-fetched rate applied", out)
	}
}

func TestConvertCurrencyStaleTableFallsBackToLive(t *testing.T) {
	t.Parallel()
	// A lookup reporting ok=false models the store's own staleness
	// bound already having rejected an old row — same fallback path as
	// a flat miss, from this tool's point of view.
	lookup := func(ctx context.Context, from, to string) (float64, string, bool, error) {
		return 0, "", false, nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"success","base_code":"USD","rates":{"USD":1,"GBP":0.734}}`))
	}))
	defer srv.Close()

	tool := newCurrencyConverter(lookup, srv.Client(), srv.URL)
	args, _ := json.Marshal(map[string]any{"amount": 10, "from": "USD", "to": "GBP"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "7.34 GBP") {
		t.Fatalf("output = %q, want the live rate applied after the stale table miss", out)
	}
}

func TestConvertCurrencyBothUnavailableErrors(t *testing.T) {
	t.Parallel()
	lookup := func(ctx context.Context, from, to string) (float64, string, bool, error) {
		return 0, "", false, nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tool := newCurrencyConverter(lookup, srv.Client(), srv.URL)
	args, _ := json.Marshal(map[string]any{"amount": 10, "from": "EUR", "to": "USD"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "exchange rate lookup failed") {
		t.Fatalf("err = %v, want it to report the live fetch failure", err)
	}
}

func TestConvertCurrencyLowercasesCodes(t *testing.T) {
	t.Parallel()
	lookup := func(ctx context.Context, from, to string) (float64, string, bool, error) {
		if from != "GBP" || to != "JPY" {
			t.Errorf("lookup(from=%s, to=%s), want upper-cased GBP/JPY", from, to)
		}
		return 190.05, "2026-07-21", true, nil
	}
	tool := newCurrencyConverter(lookup, http.DefaultClient, "http://unused.invalid")
	args, _ := json.Marshal(map[string]any{"amount": 10, "from": "gbp", "to": "jpy"})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestConvertCurrencySameCurrencyShortCircuits(t *testing.T) {
	t.Parallel()
	tool := newCurrencyConverter(nil, http.DefaultClient, "http://unused.invalid")
	args, _ := json.Marshal(map[string]any{"amount": 50, "from": "usd", "to": "USD"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "same currency") {
		t.Fatalf("out = %q, want it to short-circuit on matching currencies", out)
	}
}

func TestConvertCurrencyRejectsBlankCodes(t *testing.T) {
	t.Parallel()
	tool := newCurrencyConverter(nil, http.DefaultClient, "http://unused.invalid")
	args, _ := json.Marshal(map[string]any{"amount": 10, "from": "", "to": "USD"})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected an error for a blank currency code")
	}
}

func TestConvertCurrencyRejectsInvalidArgs(t *testing.T) {
	t.Parallel()
	tool := newCurrencyConverter(nil, http.DefaultClient, "http://unused.invalid")
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{not json`)); err == nil {
		t.Fatal("expected an error for malformed arguments")
	}
}

func TestConvertCurrencySurfacesUnknownCode(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"success","base_code":"USD","rates":{"USD":1,"EUR":0.856}}`))
	}))
	defer srv.Close()

	tool := newCurrencyConverter(nil, srv.Client(), srv.URL)
	args, _ := json.Marshal(map[string]any{"amount": 10, "from": "EUR", "to": "ZZZ"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "unknown currency") {
		t.Fatalf("err = %v, want it to mention an unknown currency code", err)
	}
}

func TestConvertCurrencySurfacesBackendErrors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tool := newCurrencyConverter(nil, srv.Client(), srv.URL)
	args, _ := json.Marshal(map[string]any{"amount": 10, "from": "EUR", "to": "USD"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want it to mention http 503", err)
	}
}
