package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConvertCurrencyParsesRate(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("from") != "EUR" || r.URL.Query().Get("to") != "USD" {
			t.Errorf("query = %v, want from=EUR&to=USD", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"amount":112.0,"base":"EUR","date":"2026-07-21","rates":{"USD":131.4}}`))
	}))
	defer srv.Close()

	tool := newCurrencyConverter(srv.Client(), srv.URL)
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

func TestConvertCurrencyLowercasesCodes(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("from") != "GBP" || r.URL.Query().Get("to") != "JPY" {
			t.Errorf("query = %v, want upper-cased from=GBP&to=JPY", r.URL.Query())
		}
		_, _ = w.Write([]byte(`{"amount":10.0,"base":"GBP","date":"2026-07-21","rates":{"JPY":1900.5}}`))
	}))
	defer srv.Close()

	tool := newCurrencyConverter(srv.Client(), srv.URL)
	args, _ := json.Marshal(map[string]any{"amount": 10, "from": "gbp", "to": "jpy"})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestConvertCurrencySameCurrencyShortCircuits(t *testing.T) {
	t.Parallel()
	tool := newCurrencyConverter(http.DefaultClient, "http://unused.invalid")
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
	tool := newCurrencyConverter(http.DefaultClient, "http://unused.invalid")
	args, _ := json.Marshal(map[string]any{"amount": 10, "from": "", "to": "USD"})
	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("expected an error for a blank currency code")
	}
}

func TestConvertCurrencyRejectsInvalidArgs(t *testing.T) {
	t.Parallel()
	tool := newCurrencyConverter(http.DefaultClient, "http://unused.invalid")
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{not json`)); err == nil {
		t.Fatal("expected an error for malformed arguments")
	}
}

func TestConvertCurrencySurfacesUnknownCode(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	}))
	defer srv.Close()

	tool := newCurrencyConverter(srv.Client(), srv.URL)
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

	tool := newCurrencyConverter(srv.Client(), srv.URL)
	args, _ := json.Marshal(map[string]any{"amount": 10, "from": "EUR", "to": "USD"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want it to mention http 503", err)
	}
}
