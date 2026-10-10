package api

import (
	"net/url"
	"testing"
	"time"
)

func TestParseKeyset(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC)
	tests := []struct {
		name    string
		query   string
		want    keyset
		wantErr bool
	}{
		{name: "default", query: "", want: keyset{Limit: 50}},
		{name: "explicit limit", query: "limit=8", want: keyset{Limit: 8}},
		{name: "limit at max", query: "limit=200", want: keyset{Limit: 200}},
		{name: "limit over max", query: "limit=201", wantErr: true},
		{name: "limit zero", query: "limit=0", wantErr: true},
		{name: "limit negative", query: "limit=-1", wantErr: true},
		{name: "limit not a number", query: "limit=ten", wantErr: true},
		{
			name:  "full cursor",
			query: "before=2026-10-01T12:00:00.123456Z&before_id=abc",
			want:  keyset{Before: ts, BeforeID: "abc", Limit: 50},
		},
		{name: "before without before_id", query: "before=2026-10-01T12:00:00Z", wantErr: true},
		{name: "before_id without before", query: "before_id=abc", wantErr: true},
		{name: "bad timestamp", query: "before=2026-10-01&before_id=abc", wantErr: true},
		{name: "bad limit with good cursor", query: "limit=999&before=2026-10-01T12:00:00Z&before_id=abc", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatalf("ParseQuery: %v", err)
			}
			got, err := parseKeyset(q, 50, 200)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseKeyset(%q) = %+v, want error", tc.query, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseKeyset(%q): %v", tc.query, err)
			}
			if !got.Before.Equal(tc.want.Before) || got.BeforeID != tc.want.BeforeID || got.Limit != tc.want.Limit {
				t.Fatalf("parseKeyset(%q) = %+v, want %+v", tc.query, got, tc.want)
			}
		})
	}
}
