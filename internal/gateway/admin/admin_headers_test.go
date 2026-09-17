package admin

import (
	"reflect"
	"strings"
	"testing"
)

// TestRedactHeaders covers issue #759: header values are secrets, so
// the API read path (List) must hand out the placeholder, keep the
// keys, and leave the live struct alone.
func TestRedactHeaders(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		headers map[string]string
		want    map[string]string
	}{
		{name: "nil headers", headers: nil, want: nil},
		{name: "empty headers", headers: map[string]string{}, want: map[string]string{}},
		{name: "every value redacted, keys kept",
			headers: map[string]string{"Authorization": "Bearer sk-live", "x-api-key": "k2"},
			want:    map[string]string{"Authorization": redactedHeaderValue, "x-api-key": redactedHeaderValue}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := Provider{Name: "p", Headers: tc.headers}
			got := redactHeaders(p)
			if !reflect.DeepEqual(got.Headers, tc.want) {
				t.Fatalf("redactHeaders = %v, want %v", got.Headers, tc.want)
			}
			if !reflect.DeepEqual(p.Headers, tc.headers) {
				t.Fatalf("redactHeaders mutated the input: %v", p.Headers)
			}
		})
	}
}

// TestMergeRedactedHeaders pins the save round-trip: a client that
// echoes a listed row back must not overwrite the stored secret with
// the placeholder, while real edits and removals still apply.
func TestMergeRedactedHeaders(t *testing.T) {
	t.Parallel()
	stored := map[string]string{"Authorization": "Bearer sk-live", "X-Title": "timothy"}
	cases := []struct {
		name    string
		patch   map[string]string
		want    map[string]string
		wantErr string
	}{
		{name: "placeholder keeps stored value",
			patch: map[string]string{"Authorization": redactedHeaderValue, "X-Title": redactedHeaderValue},
			want:  stored},
		{name: "new value replaces stored",
			patch: map[string]string{"Authorization": "Bearer sk-rotated", "X-Title": redactedHeaderValue},
			want:  map[string]string{"Authorization": "Bearer sk-rotated", "X-Title": "timothy"}},
		{name: "key absent from patch is dropped",
			patch: map[string]string{"Authorization": redactedHeaderValue},
			want:  map[string]string{"Authorization": "Bearer sk-live"}},
		{name: "empty patch clears all",
			patch: map[string]string{},
			want:  map[string]string{}},
		{name: "new key with real value",
			patch: map[string]string{"x-api-key": "k3"},
			want:  map[string]string{"x-api-key": "k3"}},
		{name: "placeholder for unknown key is an error",
			patch:   map[string]string{"x-api-key": redactedHeaderValue},
			wantErr: `header "x-api-key"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := mergeRedactedHeaders(stored, tc.patch)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("mergeRedactedHeaders: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("merged = %v, want %v", got, tc.want)
			}
		})
	}
}

// Create and Validate take a whole Provider with no stored row to fall
// back on, so the placeholder can only mean a client echoed a listed
// row and must be refused rather than stored as a literal.
func TestValidateProviderRejectsRedactedHeader(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		headers map[string]string
		wantErr bool
	}{
		{name: "real value accepted", headers: map[string]string{"x-api-key": "k1"}},
		{name: "no headers accepted", headers: nil},
		{name: "placeholder refused", headers: map[string]string{"x-api-key": redactedHeaderValue}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateProvider(Provider{
				Name: "p", Kind: "api", Driver: "openaicompat", Headers: tc.headers,
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateProvider err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
