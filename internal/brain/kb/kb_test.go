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

// TestRecentTextsFetch pins the writing_samples over-fetch: a caller
// filtering by language needs more candidates than it returns, bounded
// so a large k cannot pull the whole collection.
func TestRecentTextsFetch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		limit int
		want  int
	}{
		{1, recentTextsFetchFactor},
		{3, 3 * recentTextsFetchFactor},
		{5, 5 * recentTextsFetchFactor},
		{100, recentTextsFetchCap},
	}
	for _, tc := range cases {
		if got := recentTextsFetch(tc.limit); got != tc.want {
			t.Fatalf("recentTextsFetch(%d) = %d, want %d", tc.limit, got, tc.want)
		}
	}
}
