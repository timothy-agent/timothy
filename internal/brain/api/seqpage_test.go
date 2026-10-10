package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/session"
)

func TestParseSeqPage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		query   string
		want    seqPage
		paged   bool
		wantErr bool
	}{
		{query: "", want: seqPage{Limit: 200, AfterSeq: -1}},
		{query: "other=1", want: seqPage{Limit: 200, AfterSeq: -1}},
		{query: "limit=50", want: seqPage{Limit: 50, AfterSeq: -1}, paged: true},
		{query: "limit=500", want: seqPage{Limit: 500, AfterSeq: -1}, paged: true},
		{query: "limit=1", want: seqPage{Limit: 1, AfterSeq: -1}, paged: true},
		{query: "before_seq=10", want: seqPage{Limit: 200, BeforeSeq: 10, AfterSeq: -1}, paged: true},
		{query: "after_seq=0", want: seqPage{Limit: 200, AfterSeq: 0}, paged: true},
		{query: "after_seq=42&limit=3", want: seqPage{Limit: 3, AfterSeq: 42}, paged: true},
		{query: "limit=501", wantErr: true},
		{query: "limit=0", wantErr: true},
		{query: "limit=-1", wantErr: true},
		{query: "limit=abc", wantErr: true},
		{query: "limit=", wantErr: true},
		{query: "before_seq=x", wantErr: true},
		{query: "before_seq=0", wantErr: true},
		{query: "before_seq=1.5", wantErr: true},
		{query: "after_seq=-1", wantErr: true},
		{query: "after_seq=9223372036854775808", wantErr: true},
		{query: "before_seq=5&after_seq=1", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			q, err := url.ParseQuery(c.query)
			if err != nil {
				t.Fatalf("ParseQuery: %v", err)
			}
			got, paged, err := parseSeqPage(q)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseSeqPage(%q) = %+v, want error", c.query, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSeqPage(%q): %v", c.query, err)
			}
			if got != c.want || paged != c.paged {
				t.Fatalf("parseSeqPage(%q) = %+v paged=%v, want %+v paged=%v", c.query, got, paged, c.want, c.paged)
			}
		})
	}
}

type pagedTranscript struct {
	Items               []session.TranscriptItem `json:"items"`
	HasMore             *bool                    `json:"has_more"`
	FirstSeq            int64                    `json:"first_seq"`
	LastSeq             int64                    `json:"last_seq"`
	LivePendingSeq      *int64                   `json:"live_pending_seq"`
	ResolvedPermissions []string                 `json:"resolved_permissions"`
}

func getTranscriptPage(t *testing.T, a *API, path string) pagedTranscript {
	t.Helper()
	w := doMux(a, http.MethodGet, path, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	var resp pagedTranscript
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestTranscriptEndpointPaging(t *testing.T) {
	t.Parallel()
	a, dir, _ := testAPI(t, "tok", nil)
	ctx := t.Context()
	id, _ := dir.Create(ctx, "t") // seq 1: session_started
	for i := 2; i <= 6; i++ {
		_, _ = dir.Append(ctx, id, session.KindUserMessage, session.UserMessage{Text: fmt.Sprintf("m%d", i)})
	}
	_, _ = dir.Append(ctx, id, session.KindPermissionRequest, session.PermissionRequest{ID: "p1"})   // 7
	_, _ = dir.Append(ctx, id, session.KindPendingState, session.PendingState{Partial: "half"})      // 8
	_, _ = dir.Append(ctx, id, session.KindPermissionResolved, session.PermissionResolved{ID: "p1"}) // 9
	base := "/v1/sessions/" + id

	full := getTranscriptPage(t, a, base)
	if full.HasMore != nil || full.FirstSeq != 0 || full.LivePendingSeq != nil {
		t.Fatalf("unpaged response grew paging fields: %+v", full)
	}
	if len(full.Items) != 6 {
		t.Fatalf("unpaged items = %+v", full.Items)
	}

	latest := getTranscriptPage(t, a, base+"?limit=3")
	if len(latest.Items) != 1 || latest.Items[0].Seq != 8 || latest.FirstSeq != 7 || latest.LastSeq != 9 ||
		latest.HasMore == nil || !*latest.HasMore {
		t.Fatalf("latest page = %+v", latest)
	}
	if latest.LivePendingSeq == nil || *latest.LivePendingSeq != 8 || len(latest.ResolvedPermissions) != 1 {
		t.Fatalf("latest reconcile fields = %+v", latest)
	}

	older := getTranscriptPage(t, a, base+"?before_seq=7&limit=3")
	if len(older.Items) != 3 || older.Items[0].Text != "m4" || older.Items[2].Text != "m6" || !*older.HasMore {
		t.Fatalf("older page = %+v", older)
	}
	oldest := getTranscriptPage(t, a, base+"?before_seq=4")
	if len(oldest.Items) != 2 || oldest.FirstSeq != 1 || *oldest.HasMore {
		t.Fatalf("oldest page = %+v", oldest)
	}

	newer := getTranscriptPage(t, a, base+"?after_seq=6&limit=2")
	if len(newer.Items) != 1 || newer.LastSeq != 8 || !*newer.HasMore || newer.ResolvedPermissions != nil {
		t.Fatalf("newer page = %+v", newer)
	}
	empty := getTranscriptPage(t, a, base+"?after_seq=9")
	if len(empty.Items) != 0 || empty.FirstSeq != 0 || *empty.HasMore {
		t.Fatalf("empty page = %+v", empty)
	}

	for _, q := range []string{"?limit=501", "?before_seq=a", "?before_seq=2&after_seq=1", "?after_seq=-3"} {
		if w := doMux(a, http.MethodGet, base+q, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("GET %s: %d, want 400", q, w.Code)
		}
	}
}
