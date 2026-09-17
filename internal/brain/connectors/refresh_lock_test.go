package connectors

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// TestTokenRefreshIsSerializedPerRef pins D-112: concurrent callers on
// one connector must produce exactly one refresh-grant exchange, so the
// rotated refresh token Microsoft hands back is never raced away by a
// second exchange of the already-invalidated old one.
func TestTokenRefreshIsSerializedPerRef(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// refresh runs one token() call against a freshly built engine
		// whose stored bundle at ref has already expired.
		refresh func(t *testing.T, callers int) (exchanges int, tokens []string)
	}{
		{
			name: "microsoft",
			refresh: func(t *testing.T, callers int) (int, []string) {
				t.Helper()
				f := &fakeMicrosoft{}
				m, secrets := testMicrosoft(t, f, microsoftRow(microsoftMailScopes))
				seedExpiredBundle(t, secrets, "OUTLOOK_MSFT_OAUTH", "rt-old")
				cfg, err := microsoftConfig(microsoftRow(microsoftMailScopes))
				if err != nil {
					t.Fatalf("microsoftConfig: %v", err)
				}
				tokens := runConcurrently(callers, func() (string, error) {
					return m.token(context.Background(), cfg, "OUTLOOK_MSFT_OAUTH")
				})
				return countRefreshGrants(f), tokens
			},
		},
		{
			name: "google",
			refresh: func(t *testing.T, callers int) (int, []string) {
				t.Helper()
				f := &fakeGoogle{}
				g, secrets := testGoogle(t, f, googleRow(bothScopes))
				seedExpiredBundle(t, secrets, "PERSONAL_GOOGLE_OAUTH", "rt-old")
				cfg, err := googleConfig(googleRow(bothScopes))
				if err != nil {
					t.Fatalf("googleConfig: %v", err)
				}
				tokens := runConcurrently(callers, func() (string, error) {
					return g.token(context.Background(), cfg, "PERSONAL_GOOGLE_OAUTH")
				})
				return countGoogleRefreshGrants(f), tokens
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const callers = 8
			exchanges, tokens := tc.refresh(t, callers)
			if exchanges != 1 {
				t.Fatalf("refresh_token exchanges = %d, want exactly 1 across %d concurrent callers", exchanges, callers)
			}
			for i, got := range tokens {
				if got == "" {
					t.Fatalf("caller %d got an empty access token", i)
				}
				if got != tokens[0] {
					t.Fatalf("caller %d token = %q, want every caller to see %q", i, got, tokens[0])
				}
			}
		})
	}
}

// TestRefreshLocksAreIndependentPerRef pins that two different
// credential_refs never queue behind each other: one Google/Microsoft
// instance serves every connector of its kind, so a slow refresh on one
// account must not stall another's.
func TestRefreshLocksAreIndependentPerRef(t *testing.T) {
	t.Parallel()

	var l refreshLocks
	held := l.lock("account-a")
	done := make(chan struct{})
	go func() {
		l.lock("account-b").Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a lock on account-b blocked behind account-a's")
	}
	held.Unlock()

	// The same ref does queue, and hands back the same mutex.
	first := l.lock("account-a")
	first.Unlock()
	second := l.lock("account-a")
	second.Unlock()
	if first != second {
		t.Fatal("refreshLocks handed out two mutexes for one ref")
	}
}

func seedExpiredBundle(t *testing.T, secrets *fakeSecrets, ref, refreshToken string) {
	t.Helper()
	//nolint:gosec // G117: fake token fixture.
	raw, err := json.Marshal(tokenBundle{
		AccessToken: "stale", RefreshToken: refreshToken, Expiry: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	if err := secrets.Set(t.Context(), ref, string(raw)); err != nil {
		t.Fatalf("seed bundle: %v", err)
	}
}

// runConcurrently fires n copies of call at once and returns what each
// one got, failing nothing: the caller asserts on the results.
func runConcurrently(n int, call func() (string, error)) []string {
	out := make([]string, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			token, err := call()
			if err == nil {
				out[i] = token
			}
		}()
	}
	close(start)
	wg.Wait()
	return out
}

func countRefreshGrants(f *fakeMicrosoft) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, form := range f.tokenForms {
		if form.Get("grant_type") == "refresh_token" {
			n++
		}
	}
	return n
}

func countGoogleRefreshGrants(f *fakeGoogle) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, form := range f.tokenForms {
		if form.Get("grant_type") == "refresh_token" {
			n++
		}
	}
	return n
}
