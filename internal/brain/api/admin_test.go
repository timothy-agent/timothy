package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/settings"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

// degradedSettings builds a Store over a pool with no DSN, i.e.
// permanently degraded — All/AllValues fall back to their built-in
// defaults without ever touching a database, which is all these tests
// need from the settings switches.
func degradedSettings(t *testing.T) *settings.Store {
	t.Helper()
	pool := pgpool.New(t.Context(), "", discard())
	return settings.New(pool, discard())
}

func TestGetSettingsReportsTranscribeEnabled(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		whisperURL string
		want       bool
	}{
		{"configured", "http://whisper:9000", true},
		{"unset", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &API{token: "tok", log: discard(), flags: degradedSettings(t)}
			m := http.NewServeMux()
			a.registerSettings(m.Handle, a.flags, tc.whisperURL, false)

			req := httptest.NewRequest(http.MethodGet, "/v1/admin/settings", nil)
			req.Header.Set("Authorization", "Bearer tok")
			w := httptest.NewRecorder()
			m.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			var body struct {
				Settings map[string]bool `json:"settings"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := body.Settings["transcribe_enabled"]; got != tc.want {
				t.Fatalf("transcribe_enabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetSettingsReportsPDFExportEnabled(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"enabled", true},
		{"disabled", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &API{token: "tok", log: discard(), flags: degradedSettings(t)}
			m := http.NewServeMux()
			a.registerSettings(m.Handle, a.flags, "", tc.want)

			req := httptest.NewRequest(http.MethodGet, "/v1/admin/settings", nil)
			req.Header.Set("Authorization", "Bearer tok")
			w := httptest.NewRecorder()
			m.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			var body struct {
				Settings map[string]bool `json:"settings"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := body.Settings["pdf_export_enabled"]; got != tc.want {
				t.Fatalf("pdf_export_enabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPatchSettingsRejectsTranscribeEnabled(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard(), flags: degradedSettings(t)}
	m := http.NewServeMux()
	a.registerSettings(m.Handle, a.flags, "http://whisper:9000", false)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/settings", strings.NewReader(`{"transcribe_enabled": false}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	// The derived flag stays untouched by the rejected write.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/admin/settings", nil)
	getReq.Header.Set("Authorization", "Bearer tok")
	getW := httptest.NewRecorder()
	m.ServeHTTP(getW, getReq)
	var body struct {
		Settings map[string]bool `json:"settings"`
	}
	if err := json.Unmarshal(getW.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Settings["transcribe_enabled"] {
		t.Fatalf("transcribe_enabled = %v, want true after rejected patch", body.Settings["transcribe_enabled"])
	}
}

func TestPatchSettingsRejectsPDFExportEnabled(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard(), flags: degradedSettings(t)}
	m := http.NewServeMux()
	a.registerSettings(m.Handle, a.flags, "", true)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/settings", strings.NewReader(`{"pdf_export_enabled": false}`))
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
