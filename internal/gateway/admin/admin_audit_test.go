package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRedactAuditValueProvider covers issue #444: header-authenticated
// providers put secrets (Authorization, x-api-key) in Headers, and
// admin_audit has no dedicated secret handling of its own.
func TestRedactAuditValueProvider(t *testing.T) {
	t.Parallel()
	secret := "Bearer sk-super-secret-value" //nolint:gosec // fake fixture value, not a credential
	p := Provider{
		Name:    "p",
		Kind:    "api",
		Driver:  "openaicompat",
		Headers: map[string]string{"Authorization": secret, "x-api-key": "another-secret"},
	}

	redacted := redactAuditValue(p)

	b, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), secret) || strings.Contains(string(b), "another-secret") {
		t.Fatalf("audit payload leaked a header secret: %s", b)
	}
	if !strings.Contains(string(b), "Authorization") || !strings.Contains(string(b), "x-api-key") {
		t.Fatalf("audit payload dropped header keys: %s", b)
	}

	// the live struct passed in must not have been mutated
	if p.Headers["Authorization"] != secret {
		t.Fatalf("redactAuditValue mutated the live Provider.Headers: %v", p.Headers)
	}
}

func TestRedactAuditValueProviderNoHeaders(t *testing.T) {
	t.Parallel()
	p := Provider{Name: "p", Kind: "api", Driver: "openaicompat"}
	redacted, ok := redactAuditValue(p).(Provider)
	if !ok {
		t.Fatalf("redactAuditValue changed type: %T", redacted)
	}
	if len(redacted.Headers) != 0 {
		t.Fatalf("Headers = %v, want empty", redacted.Headers)
	}
}

func TestRedactAuditValuePassesThroughOtherTypes(t *testing.T) {
	t.Parallel()
	v := map[string]any{"configured": true}
	if got := redactAuditValue(v); got == nil {
		t.Fatalf("redactAuditValue(%v) = nil", v)
	}
	if got := redactAuditValue(nil); got != nil {
		t.Fatalf("redactAuditValue(nil) = %v, want nil", got)
	}
}
