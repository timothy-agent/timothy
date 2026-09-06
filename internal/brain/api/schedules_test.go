package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/attachments"
	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// fakeDestinationLookup is a destinationLookup fake keyed by id ->
// enabled, so validateDestinationIDs' create/patch tests don't need a
// real *destinations.Store/Postgres.
type fakeDestinationLookup map[string]bool

func (f fakeDestinationLookup) EnabledByID(_ context.Context, id string) (bool, error) {
	ok, exists := f[id]
	return exists && ok, nil
}

type erroringDestinationLookup struct{}

func (erroringDestinationLookup) EnabledByID(context.Context, string) (bool, error) {
	return false, errors.New("lookup failed")
}

func TestSchedulesEndpointsUnmountedWhenStoreNil(t *testing.T) {
	t.Parallel()
	a, _, _ := testAPI(t, "tok", nil)
	m := mux(a)
	a.registerSchedules(m.Handle, nil, nil, nil)

	for _, req := range []struct{ method, path string }{
		{"GET", "/v1/schedules"},
		{"POST", "/v1/schedules"},
		{"PATCH", "/v1/schedules/abc"},
		{"DELETE", "/v1/schedules/abc"},
	} {
		httpReq := httptest.NewRequest(req.method, req.path, nil)
		httpReq.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httpReq)
		if w.Code != 404 {
			t.Fatalf("%s %s with a nil schedules store = %d, want 404 (unmounted)", req.method, req.path, w.Code)
		}
	}
}

// TestDecorateNextRun is a pure test of the GET-decoration helper: no
// store, no HTTP round trip, just the anchor/cron math.
func TestDecorateNextRun(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sc := missions.Schedule{Cron: "0 9 * * *", CreatedAt: created}
	view := decorate(sc)
	if view.NextRun == nil {
		t.Fatal("decorate did not compute a next_run")
	}
	want := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	if !view.NextRun.Equal(want) {
		t.Fatalf("NextRun = %v, want %v", *view.NextRun, want)
	}

	// last_run anchors ahead of created_at once the schedule has fired.
	lastRun := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	sc.LastRun = &lastRun
	view = decorate(sc)
	want = time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC)
	if view.NextRun == nil || !view.NextRun.Equal(want) {
		t.Fatalf("NextRun with last_run set = %v, want %v", view.NextRun, want)
	}

	// An invalid cron (shouldn't happen past CreateSchedule's own
	// validation, but decorate must degrade gracefully) omits next_run
	// rather than panicking or reporting a fabricated zero time.
	sc = missions.Schedule{Cron: "garbage", CreatedAt: created}
	view = decorate(sc)
	if view.NextRun != nil {
		t.Fatalf("NextRun for invalid cron = %v, want nil", view.NextRun)
	}
}

func TestScheduleValidateDestinationIDs(t *testing.T) {
	t.Parallel()

	t.Run("empty ids always pass, even with destinations disabled", func(t *testing.T) {
		t.Parallel()
		h := &scheduleAPI{}
		if err := h.validateDestinationIDs(t.Context(), nil); err != nil {
			t.Fatalf("validateDestinationIDs(nil) = %v, want nil", err)
		}
	})

	t.Run("nil destinations rejects any non-empty ids", func(t *testing.T) {
		t.Parallel()
		h := &scheduleAPI{}
		err := h.validateDestinationIDs(t.Context(), []string{"d1"})
		if err == nil {
			t.Fatal("validateDestinationIDs with destinations disabled = nil, want error")
		}
	})

	t.Run("unknown or disabled id rejected, naming it", func(t *testing.T) {
		t.Parallel()
		h := &scheduleAPI{destinations: fakeDestinationLookup{"d1": true, "d2": false}}
		err := h.validateDestinationIDs(t.Context(), []string{"d1", "d2", "d3"})
		if err == nil {
			t.Fatal("validateDestinationIDs = nil, want error naming d2 and d3")
		}
		msg := err.Error()
		if !bytes.Contains([]byte(msg), []byte("d2")) || !bytes.Contains([]byte(msg), []byte("d3")) {
			t.Fatalf("error %q does not name both invalid ids", msg)
		}
	})

	t.Run("all enabled ids pass", func(t *testing.T) {
		t.Parallel()
		h := &scheduleAPI{destinations: fakeDestinationLookup{"d1": true, "d2": true}}
		if err := h.validateDestinationIDs(t.Context(), []string{"d1", "d2"}); err != nil {
			t.Fatalf("validateDestinationIDs = %v, want nil", err)
		}
	})

	t.Run("lookup error propagates", func(t *testing.T) {
		t.Parallel()
		h := &scheduleAPI{destinations: erroringDestinationLookup{}}
		if err := h.validateDestinationIDs(t.Context(), []string{"d1"}); err == nil {
			t.Fatal("validateDestinationIDs = nil, want propagated lookup error")
		}
	})
}

// TestScheduleCreateRejectsInvalidDestinationIDs exercises the HTTP
// handler end to end for the rejection path only — validation runs
// before h.store is ever touched, so a nil store is fine here (the
// success path needs a real Postgres-backed store and is covered by
// the integration suite instead).
func TestScheduleCreateRejectsInvalidDestinationIDs(t *testing.T) {
	t.Parallel()
	h := &scheduleAPI{destinations: fakeDestinationLookup{"d1": true}}
	body, _ := json.Marshal(createScheduleRequest{
		Name: "daily", Cron: "0 7 * * *",
		MissionTemplate: missions.MissionTemplate{Goal: "g", Kind: "general", DestinationIDs: []string{"d1", "unknown"}},
	})
	req := httptest.NewRequest("POST", "/v1/schedules", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.create(w, req)
	if w.Code != 400 {
		t.Fatalf("create with an unknown destination id = %d, want 400", w.Code)
	}
}

// TestSchedulePatchRejectsInvalidDestinationIDs mirrors the create
// test for PATCH — an omitted mission_template must skip validation
// entirely (nil pointer means "leave unchanged"), only a present one
// is checked.
func TestSchedulePatchRejectsInvalidDestinationIDs(t *testing.T) {
	t.Parallel()
	h := &scheduleAPI{destinations: fakeDestinationLookup{"d1": false}}
	body, _ := json.Marshal(patchScheduleRequest{
		MissionTemplate: &missions.MissionTemplate{Goal: "g", Kind: "general", DestinationIDs: []string{"d1"}},
	})
	req := httptest.NewRequest("PATCH", "/v1/schedules/abc", bytes.NewReader(body))
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.patch(w, req)
	if w.Code != 400 {
		t.Fatalf("patch with a disabled destination id = %d, want 400", w.Code)
	}
}

// TestResolveTemplateAttachments covers resolveTemplateAttachments'
// reuse-vs-convert logic (issue #359): a new id is converted through
// the resolver, an id already present in existing with non-empty
// markdown is reused as-is (no reconversion), and an id missing from
// want is simply absent from the result (a patch that drops it).
func TestResolveTemplateAttachments(t *testing.T) {
	t.Parallel()

	t.Run("new id converted", func(t *testing.T) {
		t.Parallel()
		fa := &fakeMissionAttachments{byID: map[string]attachments.Attachment{
			"a1": {ID: "a1", Mime: "text/plain"},
		}, data: map[string][]byte{"a1": []byte("hello")}}
		h := &scheduleAPI{attachments: &attachmentResolver{store: fa}}
		out, err := h.resolveTemplateAttachments(t.Context(), []missions.SourceEntry{{ID: "a1", Name: "notes.txt"}}, nil)
		if err != nil {
			t.Fatalf("resolveTemplateAttachments: %v", err)
		}
		if len(out) != 1 || out[0].Markdown != "hello" {
			t.Fatalf("out = %+v, want one converted entry", out)
		}
	})

	t.Run("existing id with markdown reused without reconversion", func(t *testing.T) {
		t.Parallel()
		// A resolver whose store always errors on Get: if this were
		// called for a1, the test would fail, confirming reuse skipped
		// conversion.
		h := &scheduleAPI{attachments: &attachmentResolver{store: &fakeMissionAttachments{}}}
		existing := []missions.SourceEntry{{Source: "pdf", ID: "a1", Mime: "text/plain", Markdown: "already converted"}}
		out, err := h.resolveTemplateAttachments(t.Context(), []missions.SourceEntry{{ID: "a1"}}, existing)
		if err != nil {
			t.Fatalf("resolveTemplateAttachments: %v", err)
		}
		if len(out) != 1 || out[0].Markdown != "already converted" {
			t.Fatalf("out = %+v, want the existing entry reused verbatim", out)
		}
	})

	t.Run("id missing from want is dropped", func(t *testing.T) {
		t.Parallel()
		h := &scheduleAPI{attachments: &attachmentResolver{store: &fakeMissionAttachments{}}}
		existing := []missions.SourceEntry{
			{Source: "pdf", ID: "a1", Markdown: "converted"},
			{Source: "pdf", ID: "a2", Markdown: "converted2"},
		}
		out, err := h.resolveTemplateAttachments(t.Context(), []missions.SourceEntry{{ID: "a2"}}, existing)
		if err != nil {
			t.Fatalf("resolveTemplateAttachments: %v", err)
		}
		if len(out) != 1 || out[0].ID != "a2" {
			t.Fatalf("out = %+v, want only a2", out)
		}
	})

	t.Run("empty want returns nil", func(t *testing.T) {
		t.Parallel()
		h := &scheduleAPI{}
		out, err := h.resolveTemplateAttachments(t.Context(), nil, nil)
		if err != nil || out != nil {
			t.Fatalf("resolveTemplateAttachments(nil) = %v, %v, want nil, nil", out, err)
		}
	})

	t.Run("cap exceeded", func(t *testing.T) {
		t.Parallel()
		h := &scheduleAPI{attachments: &attachmentResolver{store: &fakeMissionAttachments{}}}
		want := make([]missions.SourceEntry, maxMissionAttachments+1)
		_, err := h.resolveTemplateAttachments(t.Context(), want, nil)
		if err == nil || !strings.Contains(err.Error(), "too many attachments") {
			t.Fatalf("resolveTemplateAttachments over cap = %v, want too-many-attachments error", err)
		}
	})
}

// TestStripTemplateAttachmentMarkdown confirms a schedule response
// never carries a resolved attachment's markdown, the same rationale
// as sanitizeMission's strip for a mission response.
func TestStripTemplateAttachmentMarkdown(t *testing.T) {
	t.Parallel()
	sc := missions.Schedule{MissionTemplate: missions.MissionTemplate{Attachments: []missions.SourceEntry{
		{ID: "a1", Mime: "text/plain", Name: "notes.txt", Markdown: "secret body"},
	}}}
	stripped := stripTemplateAttachmentMarkdown(sc)
	if stripped.MissionTemplate.Attachments[0].Markdown != "" {
		t.Fatal("stripTemplateAttachmentMarkdown left markdown on the response")
	}
	if stripped.MissionTemplate.Attachments[0].ID != "a1" {
		t.Fatal("stripTemplateAttachmentMarkdown dropped the id/mime/name")
	}
}
