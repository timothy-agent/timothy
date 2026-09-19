package gitprovider

import (
	"net/url"
	"regexp"
	"strings"
)

// scpLikePattern matches the scp-form remote (git@github.com:u/r.git):
// no "://", so url.Parse would mis-read it. Recognized and normalized to
// ssh:// here rather than rejected, so the parser shape already fits the
// ssh transport work without changing.
var scpLikePattern = regexp.MustCompile(`^([\w.-]+)@([\w.-]+):(.+)$`)

// NormalizeSCP rewrites an scp-form remote into its canonical ssh:// URL
// and reports whether raw was scp-form at all.
func NormalizeSCP(raw string) (string, bool) {
	m := scpLikePattern.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return "", false
	}
	return "ssh://" + m[1] + "@" + m[2] + "/" + strings.TrimPrefix(m[3], "/"), true
}

// parseRepoURL is the shared host-pinned parser: it accepts https, ssh
// and scp-form URLs on one of hosts, then reads owner/name from the
// path, tolerating a .git suffix, a trailing slash, an embedded user@,
// and any of pagePaths as a browser-URL tail.
func parseRepoURL(raw string, hosts []string, pagePaths []string) (RepoRef, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RepoRef{}, false
	}
	if ssh, ok := NormalizeSCP(raw); ok {
		raw = ssh
	}
	u, err := url.Parse(raw)
	if err != nil {
		return RepoRef{}, false
	}
	switch u.Scheme {
	case "https", "http", "ssh":
	default:
		return RepoRef{}, false
	}
	if !hostIn(u.Hostname(), hosts) {
		return RepoRef{}, false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 2 || segs[0] == "" || segs[1] == "" {
		return RepoRef{}, false
	}
	owner, name := segs[0], strings.TrimSuffix(segs[1], ".git")
	if name == "" {
		return RepoRef{}, false
	}
	if len(segs) > 2 && !pageIn(segs[2], pagePaths) {
		return RepoRef{}, false
	}
	return RepoRef{Owner: owner, Name: name}, true
}

func hostIn(host string, hosts []string) bool {
	for _, h := range hosts {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	return false
}

func pageIn(seg string, pages []string) bool {
	for _, p := range pages {
		if seg == p {
			return true
		}
	}
	return false
}
