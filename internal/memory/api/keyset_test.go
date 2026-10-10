package api

import (
	"net/url"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/memory/store"
)

func TestParsePage(t *testing.T) {
	t.Parallel()
	const id = "11111111-1111-4111-8111-111111111111"
	ts := time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC)
	tests := []struct {
		name    string
		query   string
		want    store.Page
		wantErr bool
	}{
		{name: "default", query: "", want: store.Page{Limit: 50}},
		{name: "explicit limit", query: "limit=8", want: store.Page{Limit: 8}},
		{name: "limit at max", query: "limit=200", want: store.Page{Limit: 200}},
		{name: "limit over max", query: "limit=201", wantErr: true},
		{name: "limit zero", query: "limit=0", wantErr: true},
		{name: "limit negative", query: "limit=-1", wantErr: true},
		{name: "limit not a number", query: "limit=ten", wantErr: true},
		{
			name:  "full cursor",
			query: "before=2026-10-01T12:00:00.123456Z&before_id=" + id,
			want:  store.Page{Before: ts, BeforeID: id, Limit: 50},
		},
		{name: "before without before_id", query: "before=2026-10-01T12:00:00Z", wantErr: true},
		{name: "before_id without before", query: "before_id=" + id, wantErr: true},
		{name: "bad timestamp", query: "before=2026-10-01&before_id=" + id, wantErr: true},
		{name: "non-uuid before_id", query: "before=2026-10-01T12:00:00Z&before_id=abc", wantErr: true},
		{name: "bad limit with good cursor", query: "limit=999&before=2026-10-01T12:00:00Z&before_id=" + id, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatalf("ParseQuery: %v", err)
			}
			got, err := parsePage(q)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parsePage(%q) = %+v, want error", tc.query, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePage(%q): %v", tc.query, err)
			}
			if !got.Before.Equal(tc.want.Before) || got.BeforeID != tc.want.BeforeID || got.Limit != tc.want.Limit {
				t.Fatalf("parsePage(%q) = %+v, want %+v", tc.query, got, tc.want)
			}
		})
	}
}
