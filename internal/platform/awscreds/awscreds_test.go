package awscreds

import (
	"strings"
	"testing"
)

// TestParse covers the secret-store JSON contract: valid input,
// optional fields, missing required fields, and malformed JSON. Errors
// must never echo the raw value.
func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		want    *StaticCredentials
	}{
		{
			name: "valid minimal",
			raw:  `{"access_key_id":"AKIA123","secret_access_key":"SKLEAK"}`,
			want: &StaticCredentials{AccessKeyID: "AKIA123", SecretAccessKey: "SKLEAK"},
		},
		{
			name: "valid with optional fields",
			raw:  `{"access_key_id":"AKIA123","secret_access_key":"SKLEAK","session_token":"tok","region":"us-west-2"}`,
			want: &StaticCredentials{AccessKeyID: "AKIA123", SecretAccessKey: "SKLEAK", SessionToken: "tok", Region: "us-west-2"},
		},
		{
			name: "unknown keys ignored",
			raw:  `{"access_key_id":"AKIA123","secret_access_key":"SKLEAK","bogus":"x"}`,
			want: &StaticCredentials{AccessKeyID: "AKIA123", SecretAccessKey: "SKLEAK"},
		},
		{name: "missing access_key_id", raw: `{"secret_access_key":"SKLEAK"}`, wantErr: true},
		{name: "missing secret_access_key", raw: `{"access_key_id":"AKIA123"}`, wantErr: true},
		{name: "empty object", raw: `{}`, wantErr: true},
		{name: "malformed json", raw: `{"access_key_id":"AKIALEAK"`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %#v", got)
				}
				if strings.Contains(err.Error(), "AKIALEAK") || strings.Contains(err.Error(), "SKLEAK") {
					t.Fatalf("error leaked key material: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if *got != *tc.want {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}
