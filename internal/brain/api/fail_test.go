package api

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/kb"
	"github.com/SumonMSelim/timothy/internal/brain/workflows"
)

// errPGDriver is a wrapped Postgres error whose text must never reach
// a client.
var errPGDriver = fmt.Errorf("store: %w", &pgconn.PgError{Code: "23502", Message: `null value in column "x" violates not-null constraint`})

type failCase struct {
	name string
	err  error
	want int
	code string
}

// runFailCases drives a fail* helper through cases and checks status,
// error code, that driver text never leaks, and that a 500 logs the
// underlying error.
func runFailCases(t *testing.T, fail func(http.ResponseWriter, *slog.Logger, error), cases []failCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			w := httptest.NewRecorder()
			fail(w, log, tt.err)
			body := w.Body.String()
			if w.Code != tt.want || !strings.Contains(body, `"error":"`+tt.code+`"`) {
				t.Fatalf("fail(%v) = %d %s, want %d %s", tt.err, w.Code, body, tt.want, tt.code)
			}
			if strings.Contains(body, "null value") || strings.Contains(body, "SQLSTATE") || strings.Contains(body, "db down") {
				t.Fatalf("fail(%v) body leaked internal text: %s", tt.err, body)
			}
			if tt.want == http.StatusInternalServerError {
				if !strings.Contains(body, `"message":"internal error"`) {
					t.Fatalf("fail(%v) body = %s, want the generic message", tt.err, body)
				}
				if !strings.Contains(buf.String(), "request failed") {
					t.Fatalf("fail(%v) did not log the error: %s", tt.err, buf.String())
				}
			}
		})
	}
}

func TestFailInternal(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := httptest.NewRecorder()
	failInternal(w, slog.New(slog.NewTextHandler(&buf, nil)), "thing", errPGDriver)
	if w.Code != 500 || !strings.Contains(w.Body.String(), `"error":"internal_error"`) {
		t.Fatalf("failInternal = %d %s, want 500 internal_error", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "null value") {
		t.Fatalf("failInternal body leaked driver text: %s", w.Body.String())
	}
	if !strings.Contains(buf.String(), "thing request failed") || !strings.Contains(buf.String(), "23502") {
		t.Fatalf("failInternal log = %q, want the prefix and the underlying error", buf.String())
	}

	// A nil logger falls back to slog.Default() instead of panicking.
	w = httptest.NewRecorder()
	failInternal(w, nil, "thing", errors.New("boom"))
	if w.Code != 500 {
		t.Fatalf("failInternal(nil log) = %d, want 500", w.Code)
	}
}

func TestFailAttachment(t *testing.T) {
	t.Parallel()
	runFailCases(t, failAttachment, []failCase{
		{"not found", fmt.Errorf("x: %w", attachments.ErrNotFound), 404, "not_found"},
		{"unsupported mime", attachments.ErrUnsupportedMIME, 400, "unsupported_mime"},
		{"too large", attachments.ErrTooLarge, 400, "too_large"},
		{"driver error", errPGDriver, 500, "internal_error"},
		{"other", errors.New("db down"), 500, "internal_error"},
	})
}

func TestFailKB(t *testing.T) {
	t.Parallel()
	runFailCases(t, failKB, []failCase{
		{"not found", fmt.Errorf("collection x: %w", kb.ErrNotFound), 404, "not_found"},
		{"in use", kb.ErrInUse, 409, "in_use"},
		{"driver error", errPGDriver, 500, "internal_error"},
		{"other", errors.New("db down"), 500, "internal_error"},
	})
}

func TestFailConnector(t *testing.T) {
	t.Parallel()
	runFailCases(t, failConnector, []failCase{
		{"not found", fmt.Errorf("connector x: %w", connectors.ErrNotFound), 404, "not_found"},
		{"unsupported", connectors.ErrUnsupported, 422, "unsupported"},
		{"name conflict", connectors.ErrNameConflict, 409, "name_conflict"},
		{"invalid", fmt.Errorf("%w: name is required", connectors.ErrInvalid), 400, "bad_request"},
		{"driver error", errPGDriver, 500, "internal_error"},
		{"other", errors.New("db down"), 500, "internal_error"},
	})
}

func TestFailUpstream(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		err  error
		want int
		code string
	}{
		{"provider error", errors.New("upstream 500"), 502, "upstream_error"},
		{"not found keeps its mapping", fmt.Errorf("x: %w", connectors.ErrNotFound), 404, "not_found"},
		{"unsupported keeps its mapping", connectors.ErrUnsupported, 422, "unsupported"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			failUpstream(w, discard(), tt.err)
			if w.Code != tt.want || !strings.Contains(w.Body.String(), `"error":"`+tt.code+`"`) {
				t.Fatalf("failUpstream(%v) = %d %s, want %d %s", tt.err, w.Code, w.Body.String(), tt.want, tt.code)
			}
		})
	}
}

func TestFailWorkflow(t *testing.T) {
	t.Parallel()
	runFailCases(t, failWorkflow, []failCase{
		{"not found", fmt.Errorf("workflow x: %w", workflows.ErrNotFound), 404, "not_found"},
		{"duplicate", workflows.ErrDuplicate, 409, "duplicate"},
		{"invalid definition", fmt.Errorf("%w: entry is required", workflows.ErrInvalidDefinition), 400, "bad_request"},
		{"disabled", fmt.Errorf("workflow x: %w", workflows.ErrDisabled), 409, "disabled"},
		{"driver error", errPGDriver, 500, "internal_error"},
		{"other", errors.New("db down"), 500, "internal_error"},
	})
}
