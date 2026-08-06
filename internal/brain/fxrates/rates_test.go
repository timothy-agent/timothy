package fxrates

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// canned open.er-api.com /v6/latest/USD response, trimmed to a few
// currencies — verified shape (2026-07).
const cannedResponse = `{
	"result": "success",
	"provider": "https://www.exchangerate-api.com",
	"base_code": "USD",
	"time_last_update_utc": "Mon, 21 Jul 2026 00:02:31 +0000",
	"rates": {
		"USD": 1,
		"EUR": 0.856,
		"GBP": 0.734,
		"BDT": 121.5,
		"JPY": 149.2
	}
}`

func TestParseLatest(t *testing.T) {
	t.Parallel()
	want := map[string]bool{"USD": true, "EUR": true, "GBP": true, "BDT": true}
	rates, asOf, err := ParseLatest([]byte(cannedResponse), want)
	if err != nil {
		t.Fatalf("ParseLatest: %v", err)
	}
	if asOf.IsZero() {
		t.Error("asOf should not be zero")
	}
	if _, ok := rates["USD"]; ok {
		t.Error("USD (the base) must never be stored as a rate")
	}
	if rates["EUR"] != 0.856 {
		t.Errorf("EUR rate = %v, want 0.856", rates["EUR"])
	}
	if rates["GBP"] != 0.734 {
		t.Errorf("GBP rate = %v, want 0.734", rates["GBP"])
	}
	if rates["BDT"] != 121.5 {
		t.Errorf("BDT rate = %v, want 121.5 (this is why fxrates does not use frankfurter/ECB, which lacks BDT)", rates["BDT"])
	}
	if _, ok := rates["JPY"]; ok {
		t.Error("JPY was not in `want`; must not be stored")
	}
}

func TestParseLatestRejectsNonSuccess(t *testing.T) {
	t.Parallel()
	body := `{"result":"error","error-type":"invalid-key"}`
	if _, _, err := ParseLatest([]byte(body), map[string]bool{"EUR": true}); err == nil {
		t.Fatal("expected an error for a non-success result")
	}
}

func TestParseLatestRejectsMalformed(t *testing.T) {
	t.Parallel()
	if _, _, err := ParseLatest([]byte(`{not json`), map[string]bool{"EUR": true}); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestParseLatestRejectsEmptyRates(t *testing.T) {
	t.Parallel()
	body := `{"result":"success","base_code":"USD","rates":{}}`
	if _, _, err := ParseLatest([]byte(body), map[string]bool{"EUR": true}); err == nil {
		t.Fatal("expected an error when the response carries no rates")
	}
}

func TestFetchLatest(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cannedResponse))
	}))
	defer srv.Close()

	rates, asOf, err := FetchLatest(t.Context(), srv.Client(), srv.URL, map[string]bool{"EUR": true, "BDT": true})
	if err != nil {
		t.Fatalf("FetchLatest: %v", err)
	}
	if len(rates) != 2 {
		t.Fatalf("rates = %v, want 2 entries", rates)
	}
	if time.Since(asOf) > 24*time.Hour {
		t.Errorf("asOf = %v, want approximately now", asOf)
	}
}

func TestFetchLatestSurfacesHTTPErrors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, _, err := FetchLatest(t.Context(), srv.Client(), srv.URL, map[string]bool{"EUR": true}); err == nil {
		t.Fatal("expected an error for a non-2xx response")
	}
}
