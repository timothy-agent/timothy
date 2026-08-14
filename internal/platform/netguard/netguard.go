// Package netguard provides an SSRF-safe dialer for fetching
// model- or user-supplied URLs: the host is resolved here and only
// vetted public unicast addresses are dialed, so a DNS answer can't
// change between check and connect, and redirects re-enter the guard
// on every hop.
package netguard

import (
	"context"
	"fmt"
	"net"
	"time"
)

// Dial resolves the host, refuses non-public addresses, and dials the
// vetted IP directly. Suitable as an http.Transport DialContext.
func Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split host: %w", err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var lastErr error
	for _, ip := range ips {
		if reason := BlockedIP(ip.IP); reason != "" {
			lastErr = fmt.Errorf("blocked address: %s resolves to %s (%s)", host, ip.IP, reason)
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
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

var cgnatNet = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// BlockedIP returns a non-empty reason when the address must not be
// dialed: anything that isn't plain public unicast. Covers loopback,
// RFC 1918 + IPv6 ULA, link-local (169.254.0.0/16 — cloud metadata
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
