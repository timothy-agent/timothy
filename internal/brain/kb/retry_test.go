package kb

import "testing"

func TestIsRetryable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  string
		want bool
	}{
		{"chain exhausted", "every provider failed: chain_exhausted", true},
		{"gateway 502", "gwclient: gateway http 502: bad gateway", true},
		{"gateway 503", "gwclient: gateway http 503: service unavailable", true},
		{"gateway 429", "gwclient: gateway http 429: rate limited", true},
		{"memoryd unreachable", "memclient: memoryd unreachable: dial tcp: connection refused", true},
		{"gateway unreachable", "gwclient: gateway unreachable: dial tcp: i/o timeout", true},
		{"timeout", "embedding failed: context deadline exceeded", true},
		{"connection reset", "memclient: memoryd http 500: read: connection reset by peer", true},
		{"stale restart", "ingestion interrupted by a restart — re-ingest to retry", true},
		{"unexpected EOF", "embedding failed: unexpected EOF", true},
		{"unsupported format", "unsupported content type \"application/zip\" at example.com: only html, pdf, markdown, and plain text", false},
		{"empty document", "document produced no chunks", false},
		{"no markdown", "document has no stored markdown to reingest", false},
		{"unknown error", "something went sideways", false},
		{"empty string", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsRetryable(tc.err); got != tc.want {
				t.Fatalf("IsRetryable(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
