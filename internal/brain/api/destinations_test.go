package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/destinations"
)

func TestDestinationsEndpointsUnmountedWhenStoreNil(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerDestinations(m.Handle, nil, nil, nil, nil)

	for _, req := range []struct{ method, path string }{
		{"GET", "/v1/admin/destinations"},
		{"POST", "/v1/admin/destinations"},
		{"PATCH", "/v1/admin/destinations/abc"},
		{"DELETE", "/v1/admin/destinations/abc"},
		{"POST", "/v1/admin/destinations/abc/test"},
	} {
		httpReq := httptest.NewRequest(req.method, req.path, nil)
		httpReq.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httpReq)
		if w.Code != 404 {
			t.Fatalf("%s %s with a nil destinations store = %d, want 404 (unmounted)", req.method, req.path, w.Code)
		}
	}
}

func TestFailDestination(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"not found", destinations.ErrNotFound, 404},
		{"referenced", destinations.ErrReferenced, 409},
		{"other", errors.New("bad config"), 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			failDestination(w, tt.err)
			if w.Code != tt.want {
				t.Fatalf("failDestination(%v) = %d, want %d", tt.err, w.Code, tt.want)
			}
		})
	}
}

// fakeTester is a minimal destinationTester fake, exercising the test
// handler's success/failure/not-found branches without a real Store.
type fakeTester struct {
	err error
}

func (f fakeTester) Test(context.Context, string) error { return f.err }

func TestDestinationTestHandler(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)

	t.Run("nil tester 404s", func(t *testing.T) {
		a.registerDestinations(m.Handle, nil, nil, nil, nil)
		// store is nil so the whole surface is unmounted; nothing to
		// assert beyond the unmounted test above — covered there.
	})

	t.Run("success reports ok true", func(t *testing.T) {
		w := httptest.NewRecorder()
		h := &destinationAPI{tester: fakeTester{err: nil}}
		req := httptest.NewRequest("POST", "/v1/admin/destinations/d1/test", nil)
		req.SetPathValue("id", "d1")
		h.test(w, req)
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if body := w.Body.String(); body != `{"ok":true}`+"\n" {
			t.Fatalf("body = %q", body)
		}
	})

	t.Run("adapter failure reports ok false with error", func(t *testing.T) {
		w := httptest.NewRecorder()
		h := &destinationAPI{tester: fakeTester{err: errors.New("smtp timeout")}}
		req := httptest.NewRequest("POST", "/v1/admin/destinations/d1/test", nil)
		req.SetPathValue("id", "d1")
		h.test(w, req)
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if body := w.Body.String(); body != `{"error":"smtp timeout","ok":false}`+"\n" {
			t.Fatalf("body = %q", body)
		}
	})

	t.Run("unknown destination 404s", func(t *testing.T) {
		w := httptest.NewRecorder()
		h := &destinationAPI{tester: fakeTester{err: destinations.ErrNotFound}}
		req := httptest.NewRequest("POST", "/v1/admin/destinations/d1/test", nil)
		req.SetPathValue("id", "d1")
		h.test(w, req)
		if w.Code != 404 {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	})

	t.Run("no tester configured 404s", func(t *testing.T) {
		w := httptest.NewRecorder()
		h := &destinationAPI{}
		req := httptest.NewRequest("POST", "/v1/admin/destinations/d1/test", nil)
		req.SetPathValue("id", "d1")
		h.test(w, req)
		if w.Code != 404 {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	})
}
