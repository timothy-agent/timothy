package kb

import (
	"strings"
	"testing"
)

// TestTruncate pins SetFailed's error cap (issue #408): chain_exhausted
// errors can carry every attempt's underlying provider message, which
// must stay bounded before it lands in kb_documents.error.
func TestTruncate(t *testing.T) {
	t.Parallel()
	short := "embedding failed: provider_error"
	if got := truncate(short, kbErrorCap); got != short {
		t.Fatalf("truncate(short) = %q, want unchanged", got)
	}

	long := strings.Repeat("ValidationException: 400 Bad Request: Too many input tokens. ", 20)
	got := truncate(long, kbErrorCap)
	if len(got) <= kbErrorCap {
		t.Fatalf("truncated length = %d, want > cap (includes the … marker)", len(got))
	}
	if len(got) > kbErrorCap+len("…") {
		t.Fatalf("truncated length = %d, want <= cap+marker", len(got))
	}
	if got[:kbErrorCap] != long[:kbErrorCap] {
		t.Fatal("truncate must keep the original prefix")
	}
}
