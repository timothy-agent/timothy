package admin

import (
	"strings"
	"testing"
	"time"
)

func TestParseRequestTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		opts    map[string]string
		want    time.Duration
		wantErr string
	}{
		{name: "valid duration", opts: map[string]string{"request_timeout": "20m"}, want: 20 * time.Minute},
		{name: "absent leaves zero", opts: map[string]string{}, want: 0},
		{name: "nil map leaves zero", opts: nil, want: 0},
		{name: "invalid duration errors", opts: map[string]string{"request_timeout": "banana"}, wantErr: "request_timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseRequestTimeout(tt.opts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRequestTimeout: %v", err)
			}
			if got != tt.want {
				t.Fatalf("timeout = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateProviderRejectsInvalidRequestTimeout(t *testing.T) {
	t.Parallel()
	p := Provider{Name: "p", Kind: "api", Driver: "openaicompat", Options: map[string]string{"request_timeout": "banana"}}
	if err := validateProvider(p); err == nil || !strings.Contains(err.Error(), "request_timeout") {
		t.Fatalf("validateProvider error = %v, want containing request_timeout", err)
	}
}

func TestProbeTimeout(t *testing.T) {
	t.Parallel()
	if got := probeTimeout(nil); got != testTimeout {
		t.Fatalf("probeTimeout(nil) = %v, want default %v", got, testTimeout)
	}
	if got := probeTimeout(map[string]string{}); got != testTimeout {
		t.Fatalf("probeTimeout(empty) = %v, want default %v", got, testTimeout)
	}
	if got := probeTimeout(map[string]string{"request_timeout": "20m"}); got != 20*time.Minute {
		t.Fatalf("probeTimeout(20m) = %v, want 20m", got)
	}
	// An unparseable value falls back to the default rather than
	// killing the probe outright — validateProvider is the gate that
	// should have already rejected it on write.
	if got := probeTimeout(map[string]string{"request_timeout": "banana"}); got != testTimeout {
		t.Fatalf("probeTimeout(invalid) = %v, want default %v", got, testTimeout)
	}
}
