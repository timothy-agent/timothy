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

func TestValidateLitellmProvider(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		opts    map[string]string
		wantErr string
	}{
		{name: "absent is valid", opts: map[string]string{}},
		{name: "nil map is valid", opts: nil},
		{name: "empty string is valid (unset)", opts: map[string]string{"litellm_provider": ""}},
		{name: "a bare token is valid", opts: map[string]string{"litellm_provider": "xai"}},
		{name: "spaces are rejected", opts: map[string]string{"litellm_provider": "not a token"}, wantErr: "litellm_provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateLitellmProvider(tt.opts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateLitellmProvider: %v", err)
			}
		})
	}
}

func TestValidateProviderRejectsInvalidLitellmProvider(t *testing.T) {
	t.Parallel()
	p := Provider{Name: "p", Kind: "api", Driver: "openaicompat", Options: map[string]string{"litellm_provider": "not a token"}}
	if err := validateProvider(p); err == nil || !strings.Contains(err.Error(), "litellm_provider") {
		t.Fatalf("validateProvider error = %v, want containing litellm_provider", err)
	}
}

// TestValidateProviderCLIKind covers D-051's kind='cli' branch: a
// mission-only executor provider validates driver name only, never a
// chat-serving base_url or wire-format compatibility — it's inherently
// wire-compatible, spawning its own CLI against the vendor's default
// endpoint under subscription/oauth credentials (bugfix: the wire
// check that requires anthropic_base_url only applies to a kind='api'
// row repurposed as an executor entry, never to kind='cli' itself, or
// no valid subscription/oauth config could ever pass validation).
func TestValidateProviderCLIKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		p       Provider
		wantErr string
	}{
		{
			name: "claude-cli driver with no anthropic_base_url passes (subscription/oauth, own default endpoint)",
			p:    Provider{Name: "p", Kind: "cli", Driver: "claude-cli"},
		},
		{
			name: "claude-cli driver with anthropic_base_url still passes",
			p: Provider{Name: "p", Kind: "cli", Driver: "claude-cli",
				Options: map[string]string{"anthropic_base_url": "http://localhost:9999"}},
		},
		{
			name:    "unknown cli driver rejected",
			p:       Provider{Name: "p", Kind: "cli", Driver: "made-up"},
			wantErr: "unknown cli driver",
		},
		{
			name:    "unknown kind rejected",
			p:       Provider{Name: "p", Kind: "made-up", Driver: "anthropic"},
			wantErr: "kind must be api or cli",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateProvider(tt.p)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateProvider: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestValidateHarnessWireFormat mirrors router.executorUsable's wire
// check (D-051) — admin must reject exactly what the resolve endpoint
// would later mark wire-incompatible, never something looser.
func TestValidateHarnessWireFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		harness string
		driver  string
		opts    map[string]string
		wantErr string
	}{
		{name: "anthropic driver satisfies claude-cli", harness: "claude-cli", driver: "anthropic"},
		{name: "anthropic_base_url satisfies claude-cli", harness: "claude-cli", driver: "claude-cli",
			opts: map[string]string{"anthropic_base_url": "http://localhost:9999"}},
		{name: "neither rejected", harness: "claude-cli", driver: "claude-cli", wantErr: "requires driver"},
		{name: "openaicompat without base_url rejected for claude-cli", harness: "claude-cli", driver: "openaicompat", wantErr: "requires driver"},
		{name: "anthropic driver satisfies pi", harness: "pi", driver: "anthropic"},
		{name: "openaicompat driver satisfies pi (dual-wire)", harness: "pi", driver: "openaicompat"},
		{name: "anthropic_base_url satisfies pi too", harness: "pi", driver: "made-up",
			opts: map[string]string{"anthropic_base_url": "http://localhost:9999"}},
		{name: "neither satisfies pi", harness: "pi", driver: "made-up", wantErr: "requires driver"},
		{name: "openaicompat driver satisfies codex-cli", harness: "codex-cli", driver: "openaicompat"},
		{name: "anthropic driver rejected for codex-cli", harness: "codex-cli", driver: "anthropic", wantErr: "requires driver"},
		{name: "anthropic_base_url does not satisfy codex-cli", harness: "codex-cli", driver: "made-up",
			opts: map[string]string{"anthropic_base_url": "http://localhost:9999"}, wantErr: "requires driver"},
		{name: "unknown harness has no rule", harness: "nonexistent-cli", driver: "openaicompat"},
		{name: "empty harness has no rule", harness: "", driver: "openaicompat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateHarnessWireFormat(tt.harness, tt.driver, tt.opts)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateHarnessWireFormat: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
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
