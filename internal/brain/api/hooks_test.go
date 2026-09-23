package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/automations"
	"github.com/SumonMSelim/timothy/internal/brain/events"
)

// GitHub's documented vector and one computed with Python's hmac for
// the generic scheme.
const (
	githubVectorSecret = "It's a Secret to Everybody"
	githubVectorBody   = "Hello, World!"
	githubVectorSig    = "757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"

	genericVectorSecret = "s3cret"
	genericVectorTS     = "1700000000"
	genericVectorBody   = `{"note":"hi"}`
	genericVectorSig    = "661dc5d2fc283eb0e21f6f3482aef741acfde3b5b814b127de47c8b3c854d9ec"
)

func TestVerifyHook(t *testing.T) {
	t.Parallel()
	genericNow := time.Unix(1700000000, 0)
	gh := func(sig string) http.Header {
		h := http.Header{}
		if sig != "" {
			h.Set("X-Hub-Signature-256", sig)
		}
		return h
	}
	generic := func(ts, sig string) http.Header {
		h := http.Header{}
		if ts != "" {
			h.Set("X-Timothy-Timestamp", ts)
		}
		if sig != "" {
			h.Set("X-Timothy-Signature", sig)
		}
		return h
	}
	cases := []struct {
		name   string
		scheme string
		secret string
		header http.Header
		body   string
		now    time.Time
		want   bool
	}{
		{"github vector", "github", githubVectorSecret, gh("sha256=" + githubVectorSig), githubVectorBody, genericNow, true},
		{"github uppercase hex", "github", githubVectorSecret, gh("sha256=" + strings.ToUpper(githubVectorSig)), githubVectorBody, genericNow, true},
		{"github wrong secret", "github", "other", gh("sha256=" + githubVectorSig), githubVectorBody, genericNow, false},
		{"github tampered body", "github", githubVectorSecret, gh("sha256=" + githubVectorSig), githubVectorBody + " ", genericNow, false},
		{"github missing header", "github", githubVectorSecret, gh(""), githubVectorBody, genericNow, false},
		{"github missing prefix", "github", githubVectorSecret, gh(githubVectorSig), githubVectorBody, genericNow, false},
		{"github not hex", "github", githubVectorSecret, gh("sha256=zz"), githubVectorBody, genericNow, false},
		{"github empty signature", "github", githubVectorSecret, gh("sha256="), githubVectorBody, genericNow, false},
		{"github scheme with generic headers", "github", genericVectorSecret, generic(genericVectorTS, genericVectorSig), genericVectorBody, genericNow, false},
		{"generic vector", "generic", genericVectorSecret, generic(genericVectorTS, genericVectorSig), genericVectorBody, genericNow, true},
		{"generic skew 5m past", "generic", genericVectorSecret, generic(genericVectorTS, genericVectorSig), genericVectorBody, genericNow.Add(5 * time.Minute), true},
		{"generic skew 5m future", "generic", genericVectorSecret, generic(genericVectorTS, genericVectorSig), genericVectorBody, genericNow.Add(-5 * time.Minute), true},
		{"generic too old", "generic", genericVectorSecret, generic(genericVectorTS, genericVectorSig), genericVectorBody, genericNow.Add(5*time.Minute + time.Second), false},
		{"generic too new", "generic", genericVectorSecret, generic(genericVectorTS, genericVectorSig), genericVectorBody, genericNow.Add(-5*time.Minute - time.Second), false},
		{"generic missing timestamp", "generic", genericVectorSecret, generic("", genericVectorSig), genericVectorBody, genericNow, false},
		{"generic bad timestamp", "generic", genericVectorSecret, generic("soon", genericVectorSig), genericVectorBody, genericNow, false},
		{"generic missing signature", "generic", genericVectorSecret, generic(genericVectorTS, ""), genericVectorBody, genericNow, false},
		{"generic signature over body only", "generic", genericVectorSecret, generic(genericVectorTS, hmacHex(genericVectorSecret, genericVectorBody)), genericVectorBody, genericNow, false},
		{"generic scheme with github header", "generic", githubVectorSecret, gh("sha256=" + githubVectorSig), githubVectorBody, genericNow, false},
		{"unknown scheme", "slack", githubVectorSecret, gh("sha256=" + githubVectorSig), githubVectorBody, genericNow, false},
	}
	for _, tc := range cases {
		if got := verifyHook(tc.scheme, []byte(tc.secret), tc.header, []byte(tc.body), tc.now); got != tc.want {
			t.Errorf("%s: verifyHook = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestHookDelivery(t *testing.T) {
	t.Parallel()
	body := []byte(`{"a":1}`)
	sum := sha256.Sum256(body)
	bodyHash := hex.EncodeToString(sum[:])
	h := http.Header{}
	h.Set("X-GitHub-Delivery", "gh-1")
	h.Set("X-Timothy-Delivery", "tm-1")
	if got := hookDelivery("github", h, body); got != "gh-1" {
		t.Errorf("github delivery = %q", got)
	}
	if got := hookDelivery("generic", h, body); got != "tm-1" {
		t.Errorf("generic delivery = %q", got)
	}
	if got := hookDelivery("github", http.Header{"X-Timothy-Delivery": {"tm-1"}}, body); got != bodyHash {
		t.Errorf("github without its header = %q, want body hash", got)
	}
	if got := hookDelivery("generic", http.Header{}, body); got != bodyHash {
		t.Errorf("no header = %q, want body hash", got)
	}
}

func TestHookHeaders(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-GitHub-Event", "pull_request")
	h.Set("Authorization", "Bearer x")
	got := hookHeaders(h)
	if len(got) != 2 || got["content-type"] != "application/json" || got["x-github-event"] != "pull_request" {
		t.Fatalf("hookHeaders = %v", got)
	}
}

func TestHookLimiter(t *testing.T) {
	t.Parallel()
	l := newHookLimiter()
	now := time.Unix(1700000000, 0)
	for i := range hookBurst {
		if !l.allow("t1", now) {
			t.Fatalf("request %d refused under the burst", i+1)
		}
	}
	if l.allow("t1", now) {
		t.Fatal("request 31 in the same instant allowed")
	}
	if !l.allow("t2", now) {
		t.Fatal("another trigger shares the bucket")
	}
	// 30 per minute refills one token every 2 seconds.
	if l.allow("t1", now.Add(time.Second)) {
		t.Fatal("allowed after 1s, want refused")
	}
	if !l.allow("t1", now.Add(2*time.Second)) {
		t.Fatal("refused after 2s, want one token")
	}
	if l.allow("t1", now.Add(2*time.Second)) {
		t.Fatal("second request after 2s allowed")
	}
	// A clock step backwards never adds tokens.
	if l.allow("t1", now) {
		t.Fatal("allowed after the clock went backwards")
	}
	later := now.Add(time.Hour)
	for i := range hookBurst {
		if !l.allow("t1", later) {
			t.Fatalf("after an hour request %d refused, want a full bucket", i+1)
		}
	}
	if l.allow("t1", later) {
		t.Fatal("bucket refilled past its burst")
	}
}

func TestHooksUnmountedWithoutDeps(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	resolve := func(context.Context, string) (string, error) { return "k", nil }
	store, ev := automations.NewStore(nil), events.NewStore(nil)
	for name, register := range map[string]func(m *http.ServeMux){
		"no store":    func(m *http.ServeMux) { a.registerHooks(m.Handle, nil, ev, nil, resolve, nil) },
		"no events":   func(m *http.ServeMux) { a.registerHooks(m.Handle, store, nil, nil, resolve, nil) },
		"no resolver": func(m *http.ServeMux) { a.registerHooks(m.Handle, store, ev, nil, nil, nil) },
	} {
		m := http.NewServeMux()
		register(m)
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httptest.NewRequest("POST", "/hooks/0b8f2f4e-6f1c-4b8a-9d2e-3c4b5a6d7e8f", strings.NewReader("{}")))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: POST /hooks = %d, want 404 (unmounted)", name, w.Code)
		}
	}
}

// TestHookMalformedTriggerID: a non-UUID id answers the same 404 as an
// unknown trigger without touching the database, and without auth.
func TestHookMalformedTriggerID(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := http.NewServeMux()
	a.registerHooks(m.Handle, automations.NewStore(nil), events.NewStore(nil), nil,
		func(context.Context, string) (string, error) { return "k", nil }, nil)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("POST", "/hooks/not-a-uuid", strings.NewReader(`{"secret":"echo-me"}`)))
	if w.Code != http.StatusNotFound || strings.TrimSpace(w.Body.String()) != `{"error":"not_found"}` {
		t.Fatalf("malformed id = %d %s, want 404 not_found", w.Code, w.Body.String())
	}
}

func hmacHex(secret, msg string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}
