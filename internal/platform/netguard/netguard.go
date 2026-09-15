// Package netguard provides an SSRF-safe dialer for fetching
// model- or user-supplied URLs: the host is resolved here and only
// vetted public unicast addresses are dialed, so a DNS answer can't
// change between check and connect, and redirects re-enter the guard
// on every hop.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrBlocked marks a dial the guard refused, so callers can tell it
// from a connection failure.
var ErrBlocked = errors.New("blocked address")

// Test seams; production keeps the defaults.
var (
	lookupIP    = net.DefaultResolver.LookupIPAddr
	dialContext = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
)

// Guard dials like Dial, with an operator allowlist of hosts that may
// bypass the address check (issue #431). Operator-configured URLs
// (webhook destinations, MCP endpoints, the mission notify webhook)
// sometimes point at a deliberately internal receiver; Allowed is read
// per dial so a settings change needs no restart. nil or empty permits
// none.
type Guard struct {
	Allowed func(ctx context.Context) []string
}

// Dial resolves the host, refuses non-public addresses unless the host
// is allowlisted, and dials the vetted IP directly. Suitable as an
// http.Transport DialContext.
func (g Guard) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split host: %w", err)
	}
	if g.allowed(ctx, host) {
		return dialContext(ctx, network, addr)
	}
	ips, err := lookupIP(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	var lastErr error
	for _, ip := range ips {
		if reason := BlockedIP(ip.IP); reason != "" {
			lastErr = fmt.Errorf("%w: %s resolves to %s (%s)", ErrBlocked, host, ip.IP, reason)
			continue
		}
		conn, err := dialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no addresses for %s", host)
	}
	return nil, lastErr
}

// Transport is an http.Transport dialing through g. It speaks http and
// https only, so no other scheme reaches the dialer.
func (g Guard) Transport() *http.Transport {
	return &http.Transport{DialContext: g.Dial, ForceAttemptHTTP2: true}
}

func (g Guard) allowed(ctx context.Context, host string) bool {
	if g.Allowed == nil {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, h := range g.Allowed(ctx) {
		if strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), ".")) == host {
			return true
		}
	}
	return false
}

// Dial is Guard.Dial with no allowlist: model- and user-supplied URLs
// never get an exception.
func Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return Guard{}.Dial(ctx, network, addr)
}

var cgnatNet = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// BlockedIP returns a non-empty reason when the address must not be
// dialed: anything that isn't plain public unicast. Covers loopback,
// RFC 1918 + IPv6 ULA, link-local (169.254.0.0/16, cloud metadata
// endpoints live there), CGNAT, unspecified, and multicast.
func BlockedIP(ip net.IP) string {
	switch {
	case ip.IsLoopback():
		return "loopback"
	case ip.IsPrivate():
		return "private range"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "link-local"
	case ip.IsUnspecified():
		return "unspecified"
	case ip.IsMulticast():
		return "multicast"
	case cgnatNet.Contains(ip):
		return "carrier-grade NAT range"
	case !ip.IsGlobalUnicast():
		return "not global unicast"
	}
	return ""
}
