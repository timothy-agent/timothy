package connectors

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRedactAuditValueConnector covers issue #444: free-form connector
// Config (imap/caldav) can carry sensitive values that the dedicated
// secret path never sees, and admin_audit has no secret handling of
// its own.
func TestRedactAuditValueConnector(t *testing.T) {
	t.Parallel()
	secret := "hunter2-app-password"
	c := Connector{
		ID:     "1",
		Name:   "personal-imap",
		Kind:   "imap",
		Config: json.RawMessage(`{"host":"imap.example.com","port":993,"username":"me@example.com","password":"` + secret + `","tls":true}`),
	}

	redacted, ok := redactAuditValue(c).(Connector)
	if !ok {
		t.Fatalf("redactAuditValue changed type: %T", redactAuditValue(c))
	}

	b, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), secret) || strings.Contains(string(b), "me@example.com") || strings.Contains(string(b), "imap.example.com") {
		t.Fatalf("audit payload leaked a config value: %s", b)
	}
	for _, key := range []string{"host", "port", "username", "password", "tls"} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("audit payload dropped config key %q: %s", key, b)
		}
	}
	// non-string values are kept as-is, only string leaves are redacted
	if !strings.Contains(string(b), "993") {
		t.Fatalf("audit payload should keep the numeric port: %s", b)
	}
	if !strings.Contains(string(b), "true") {
		t.Fatalf("audit payload should keep the boolean tls flag: %s", b)
	}

	// the stored connector's Config must not have been mutated
	if !strings.Contains(string(c.Config), secret) {
		t.Fatalf("redactAuditValue mutated the live Connector.Config: %s", c.Config)
	}
}

func TestRedactAuditValueConnectorNestedConfig(t *testing.T) {
	t.Parallel()
	c := Connector{
		Config: json.RawMessage(`{"auth":{"token":"abc123"},"scopes":["mail.read","mail.send"]}`),
	}

	redacted, ok := redactAuditValue(c).(Connector)
	if !ok {
		t.Fatalf("redactAuditValue changed type: %T", redactAuditValue(c))
	}
	b, _ := json.Marshal(redacted)
	if strings.Contains(string(b), "abc123") || strings.Contains(string(b), "mail.read") {
		t.Fatalf("audit payload leaked nested string values: %s", b)
	}
	if !strings.Contains(string(b), `"token"`) {
		t.Fatalf("audit payload dropped nested key: %s", b)
	}
}

func TestRedactAuditValueConnectorEmptyConfig(t *testing.T) {
	t.Parallel()
	c := Connector{ID: "1", Name: "n", Kind: "imap"}
	redacted, ok := redactAuditValue(c).(Connector)
	if !ok {
		t.Fatalf("redactAuditValue changed type: %T", redactAuditValue(c))
	}
	if len(redacted.Config) != 0 {
		t.Fatalf("Config = %s, want empty", redacted.Config)
	}
}

func TestRedactAuditValuePassesThroughOtherTypes(t *testing.T) {
	t.Parallel()
	if got := redactAuditValue(nil); got != nil {
		t.Fatalf("redactAuditValue(nil) = %v, want nil", got)
	}
}
