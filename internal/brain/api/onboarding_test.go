package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/onboarding"
	"github.com/SumonMSelim/timothy/internal/brain/settings"
)

// fakeOnboardingStore keeps the onboarding document in memory.
type fakeOnboardingStore struct {
	raw     []byte
	readErr error
	saved   int
}

func (f *fakeOnboardingStore) JSON(_ context.Context, key string, dst any) (bool, error) {
	if key != settings.KeyOnboarding {
		return false, errors.New("unknown key")
	}
	if f.readErr != nil {
		return false, f.readErr
	}
	if f.raw == nil {
		return false, nil
	}
	return true, json.Unmarshal(f.raw, dst)
}

func (f *fakeOnboardingStore) SetJSON(_ context.Context, _ string, v any) error {
	raw, err := json.Marshal(v)
	f.raw, f.saved = raw, f.saved+1
	return err
}

func onboardingRequest(t *testing.T, h http.Handler, method, body string, authed bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/v1/admin/onboarding", strings.NewReader(body))
	if authed {
		req.Header.Set("Authorization", "Bearer tok")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestGetOnboardingDegraded(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	m := http.NewServeMux()
	a.registerOnboarding(m.Handle, degradedSettings(t), onboarding.Probes{})

	w := onboardingRequest(t, m, http.MethodGet, "", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	var body struct {
		Readiness map[string]any  `json:"readiness"`
		Progress  json.RawMessage `json:"progress"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(body.Progress) != "{}" {
		t.Fatalf("progress = %s, want {}", body.Progress)
	}
	for _, key := range []string{"gateway_ready", "chat_route", "summarize_route", "embedding_route", "vision_route", "sandbox", "first_chat", "first_mission", "automations_enabled"} {
		if v, ok := body.Readiness[key]; !ok || v != false {
			t.Errorf("readiness[%s] = %v, want false", key, v)
		}
	}
	for _, key := range []string{"connectors", "channels", "kb_collections", "automations"} {
		if v, ok := body.Readiness[key]; !ok || v != float64(0) {
			t.Errorf("readiness[%s] = %v, want 0", key, v)
		}
	}
}

func TestOnboardingNilSettingsUnmounted(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	m := http.NewServeMux()
	a.registerOnboarding(m.Handle, nil, onboarding.Probes{})
	if w := onboardingRequest(t, m, http.MethodGet, "", true); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestPatchOnboarding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		stored   string
		body     string
		authed   bool
		wantCode int
		want     string
	}{
		{"merge onto empty", "", `{"wizard":"done","visited":["chat"]}`, true, 200, `{"wizard":"done","visited":["chat"]}`},
		{"merge onto stored", `{"tours_seen":{"chat":2},"visited":["kb"]}`, `{"tours_seen":{"chat":1,"kb":1},"visited":["chat"],"checklist_dismissed":true}`, true, 200,
			`{"checklist_dismissed":true,"tours_seen":{"chat":2,"kb":1},"visited":["chat","kb"]}`},
		{"pending clears wizard", `{"wizard":"skipped"}`, `{"wizard":"pending"}`, true, 200, `{}`},
		{"readiness field rejected", "", `{"readiness":{"chat_route":true}}`, true, 400, ""},
		{"readiness key rejected", "", `{"chat_route":true}`, true, 400, ""},
		{"bad wizard rejected", "", `{"wizard":"later"}`, true, 400, ""},
		{"malformed body", "", `{`, true, 400, ""},
		{"no auth", "", `{"wizard":"done"}`, false, 401, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeOnboardingStore{}
			if tc.stored != "" {
				store.raw = []byte(tc.stored)
			}
			a := &API{token: "tok", log: discard()}
			m := http.NewServeMux()
			a.mountOnboarding(m.Handle, store, onboarding.Probes{})

			w := onboardingRequest(t, m, http.MethodPatch, tc.body, tc.authed)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantCode, w.Body)
			}
			if tc.wantCode != http.StatusOK {
				if store.saved != 0 {
					t.Fatal("a rejected patch was saved")
				}
				return
			}
			if got := strings.TrimSpace(w.Body.String()); got != tc.want {
				t.Fatalf("body = %s, want %s", got, tc.want)
			}
			if string(store.raw) != tc.want {
				t.Fatalf("stored = %s, want %s", store.raw, tc.want)
			}
		})
	}
}

func TestPatchOnboardingDegradedIs503(t *testing.T) {
	t.Parallel()
	a := &API{token: "tok", log: discard()}
	m := http.NewServeMux()
	a.registerOnboarding(m.Handle, degradedSettings(t), onboarding.Probes{})
	if w := onboardingRequest(t, m, http.MethodPatch, `{"wizard":"done"}`, true); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body)
	}
}
