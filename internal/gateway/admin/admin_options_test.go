package admin

import (
	"net/http"
	"net/http/httptest"
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

func TestValidateOpenAIResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		opts    map[string]string
		wantErr string
	}{
		{name: "absent is valid", opts: map[string]string{}},
		{name: "nil map is valid", opts: nil},
		{name: "empty string is valid (unset)", opts: map[string]string{"openai_responses": ""}},
		{name: "true is valid", opts: map[string]string{"openai_responses": "true"}},
		{name: "false is valid", opts: map[string]string{"openai_responses": "false"}},
		{name: "arbitrary value rejected", opts: map[string]string{"openai_responses": "yes"}, wantErr: "openai_responses"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateOpenAIResponses(tt.opts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateOpenAIResponses: %v", err)
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

// TestProbeResponsesClassification covers the responses-capability
// probe's tri-state classification (real incident: Z.ai's coding-plan
// endpoint 404s /responses while chatting fine over /chat/completions —
// 404/405 are the only unambiguous "no" signals; everything else stays
// unknown rather than guessing).
func TestProbeResponsesClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		fail   bool // simulate a network error / no server at all
		want   *bool
	}{
		{name: "2xx is a definite yes", status: http.StatusOK, want: boolPtrAdmin(true)},
		{name: "201 is still a definite yes", status: http.StatusCreated, want: boolPtrAdmin(true)},
		{name: "404 is a definite no", status: http.StatusNotFound, want: boolPtrAdmin(false)},
		{name: "405 is a definite no", status: http.StatusMethodNotAllowed, want: boolPtrAdmin(false)},
		{name: "401 is unknown", status: http.StatusUnauthorized, want: nil},
		{name: "403 is unknown", status: http.StatusForbidden, want: nil},
		{name: "429 is unknown", status: http.StatusTooManyRequests, want: nil},
		{name: "500 is unknown", status: http.StatusInternalServerError, want: nil},
		{name: "network failure is unknown", fail: true, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := &Admin{}
			baseURL := "http://127.0.0.1:1" // unreachable, used only for the fail case
			if !tt.fail {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/responses" {
						t.Errorf("path = %q, want /responses", r.URL.Path)
					}
					w.WriteHeader(tt.status)
				}))
				defer srv.Close()
				baseURL = srv.URL
			}
			got := a.probeResponses(t.Context(), baseURL, "", "m1", 2*time.Second)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("probeResponses = %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != *tt.want {
				t.Fatalf("probeResponses = %v, want %v", got, *tt.want)
			}
		})
	}
}

func boolPtrAdmin(b bool) *bool { return &b }

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
