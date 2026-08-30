package kb

import "strings"

// retryableMarkers are substrings of a stored failure error that mark
// it a transient embedding/provider failure (issue #414): gateway
// chain exhaustion, provider 5xx/429, timeouts, and connection errors
// all surface through gwclient/memclient error wrapping and reach
// SetFailed with one of these in the message. Anything else (content
// too large, unsupported format, empty document) is treated as
// permanent: the allowlist is deliberately conservative so an unknown
// error string never retries forever instead of surfacing to the
// operator.
var retryableMarkers = []string{
	"chain_exhausted",
	"memoryd unreachable",
	"gateway unreachable",
	"http 429",
	"http 500",
	"http 502",
	"http 503",
	"http 504",
	"timeout",
	"deadline exceeded",
	"connection refused",
	"connection reset",
	"eof",
	"ingestion interrupted by a restart",
}

// IsRetryable reports whether a document's stored error looks like a
// transient embedding/provider failure worth an automatic retry, as
// opposed to a permanent content or format error.
func IsRetryable(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	for _, marker := range retryableMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
