package redact

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

var errSentinel = errors.New("delivery status unknown")

func TestToken(t *testing.T) {
	const secret = "123456:super-secret"
	leak := &url.Error{Op: "Post", URL: "https://api.example/bot" + secret + "/sendMessage", Err: errors.New("timeout")}
	tests := []struct {
		name     string
		err      error
		secret   string
		want     string
		sentinel bool
	}{
		{name: "nil error", err: nil, secret: secret},
		{name: "empty secret keeps err", err: errors.New("boom " + secret), secret: "", want: "boom " + secret},
		{name: "plain message", err: errors.New("boom " + secret + " again " + secret), secret: secret, want: "boom REDACTED again REDACTED"},
		{name: "url error", err: fmt.Errorf("post: %w", leak), secret: secret, want: "post: Post \"https://api.example/botREDACTED/sendMessage\": timeout"},
		{name: "sentinel survives", err: fmt.Errorf("post: %w: %w", errSentinel, leak), secret: secret, sentinel: true,
			want: "post: delivery status unknown: Post \"https://api.example/botREDACTED/sendMessage\": timeout"},
		{name: "wrapped sentinel", err: fmt.Errorf("api status 500: %s: %w", secret, errSentinel), secret: secret, sentinel: true,
			want: "api status 500: REDACTED: delivery status unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Token(tt.err, tt.secret)
			if tt.err == nil {
				if got != nil {
					t.Fatalf("Token(nil) = %v", got)
				}
				return
			}
			if got.Error() != tt.want {
				t.Fatalf("Token() = %q, want %q", got.Error(), tt.want)
			}
			if errors.Is(got, errSentinel) != tt.sentinel {
				t.Fatalf("errors.Is(sentinel) = %v, want %v", !tt.sentinel, tt.sentinel)
			}
		})
	}
}

// TestTokenHidesLeakingChain pins that no error reachable through the
// redacted chain still carries the secret.
func TestTokenHidesLeakingChain(t *testing.T) {
	const secret = "xoxb-secret"
	leak := &url.Error{Op: "Post", URL: "https://x/" + secret, Err: errors.New("eof")}
	got := Token(fmt.Errorf("slack: %w: %w", errSentinel, leak), secret)
	var ue *url.Error
	if errors.As(got, &ue) {
		t.Fatalf("errors.As reached the leaking *url.Error: %v", ue)
	}
	var walk func(error)
	walk = func(err error) {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("reachable error leaks the secret: %q", err.Error())
		}
		for _, c := range children(err) {
			walk(c)
		}
	}
	for _, c := range children(got) {
		walk(c)
	}
}
