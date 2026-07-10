package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixedClock(t *testing.T, rfc3339 string) Clock {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatalf("parse fixture time: %v", err)
	}
	return func() time.Time { return ts }
}

func TestCurrentTime(t *testing.T) {
	t.Parallel()
	tool := CurrentTime(fixedClock(t, "2026-07-11T03:15:00Z"))

	tests := []struct {
		name    string
		args    string
		want    string
		wantErr string
	}{
		{name: "defaults to UTC", args: `{}`, want: "2026-07-11T03:15:00Z"},
		{name: "IANA zone", args: `{"timezone":"Asia/Dhaka"}`, want: "2026-07-11T09:15:00+06:00"},
		{name: "weekday present", args: `{"timezone":"Asia/Dhaka"}`, want: "Saturday"},
		{name: "unknown zone", args: `{"timezone":"EST5"}`, wantErr: "unknown timezone"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tool.Execute(context.Background(), json.RawMessage(tc.args))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("result %q does not contain %q", got, tc.want)
			}
		})
	}
}

func TestConvertTime(t *testing.T) {
	t.Parallel()
	tool := ConvertTime()

	tests := []struct {
		name    string
		args    string
		want    string
		wantErr string
	}{
		{
			name: "converts across zones",
			args: `{"time":"2026-07-11T15:00:00-04:00","to_timezone":"Asia/Dhaka"}`,
			want: "2026-07-12T01:00:00+06:00",
		},
		{
			name:    "rejects bare local time",
			args:    `{"time":"2026-07-11 15:00","to_timezone":"UTC"}`,
			wantErr: "RFC 3339",
		},
		{
			name:    "rejects unknown target zone",
			args:    `{"time":"2026-07-11T15:00:00Z","to_timezone":"Mars/Olympus"}`,
			wantErr: "unknown timezone",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tool.Execute(context.Background(), json.RawMessage(tc.args))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("result %q does not contain %q", got, tc.want)
			}
		})
	}
}
