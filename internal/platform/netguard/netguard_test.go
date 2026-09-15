package netguard

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

// TestGuardDial swaps the resolver and dialer so a fake public host
// lands on a local listener: the guard's decision is what's under test,
// not the network.
func TestGuardDial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	hosts := map[string]string{
		"hooks.example": "93.184.216.34",
		"gateway":       "10.0.0.5",
		"localhost":     "127.0.0.1",
	}
	origLookup, origDial := lookupIP, dialContext
	defer func() { lookupIP, dialContext = origLookup, origDial }()
	lookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if lit := net.ParseIP(host); lit != nil {
			return []net.IPAddr{{IP: lit}}, nil
		}
		ip, ok := hosts[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
	}
	var dialed string
	dialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = addr
		return (&net.Dialer{}).DialContext(ctx, network, ln.Addr().String())
	}

	tests := []struct {
		name       string
		allowed    []string
		addr       string
		wantErr    string
		wantDialed string
	}{
		{name: "public host dialed at vetted ip", addr: "hooks.example:443", wantDialed: "93.184.216.34:443"},
		{name: "private range refused", addr: "gateway:8081", wantErr: "blocked address: gateway resolves to 10.0.0.5 (private range)"},
		{name: "loopback refused", addr: "localhost:8080", wantErr: "blocked address: localhost resolves to 127.0.0.1 (loopback)"},
		{name: "ip literal refused", addr: "169.254.169.254:80", wantErr: "(link-local)"},
		{name: "allowlisted host dialed as given", allowed: []string{"gateway"}, addr: "gateway:8081", wantDialed: "gateway:8081"},
		{name: "allowlist match is case-insensitive and trimmed", allowed: []string{" Gateway "}, addr: "gateway:8081", wantDialed: "gateway:8081"},
		{name: "allowlist is per host, not per range", allowed: []string{"gateway"}, addr: "localhost:8080", wantErr: "(loopback)"},
		{name: "allowlisted ip literal", allowed: []string{"127.0.0.1"}, addr: "127.0.0.1:8080", wantDialed: "127.0.0.1:8080"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dialed = ""
			g := Guard{}
			if tc.allowed != nil {
				g.Allowed = func(context.Context) []string { return tc.allowed }
			}
			conn, err := g.Dial(context.Background(), "tcp", tc.addr)
			if conn != nil {
				_ = conn.Close()
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Dial(%s) err = %v, want containing %q", tc.addr, err, tc.wantErr)
				}
				if !errors.Is(err, ErrBlocked) {
					t.Fatalf("Dial(%s) err = %v, want ErrBlocked", tc.addr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Dial(%s): %v", tc.addr, err)
			}
			if dialed != tc.wantDialed {
				t.Fatalf("dialed %q, want %q", dialed, tc.wantDialed)
			}
		})
	}
}

func TestBlockedIP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ip      string
		blocked bool
	}{
		{ip: "127.0.0.1", blocked: true},
		{ip: "::1", blocked: true},
		{ip: "10.1.2.3", blocked: true},
		{ip: "172.16.0.1", blocked: true},
		{ip: "192.168.1.1", blocked: true},
		{ip: "169.254.169.254", blocked: true}, // cloud metadata
		{ip: "100.64.0.1", blocked: true},      // CGNAT
		{ip: "0.0.0.0", blocked: true},
		{ip: "224.0.0.1", blocked: true},
		{ip: "fd00::1", blocked: true}, // IPv6 ULA
		{ip: "fe80::1", blocked: true}, // IPv6 link-local
		{ip: "8.8.8.8", blocked: false},
		{ip: "1.1.1.1", blocked: false},
		{ip: "2606:4700:4700::1111", blocked: false},
	}
	for _, tc := range tests {
		t.Run(tc.ip, func(t *testing.T) {
			t.Parallel()
			reason := BlockedIP(net.ParseIP(tc.ip))
			if (reason != "") != tc.blocked {
				t.Fatalf("BlockedIP(%s) = %q, want blocked=%v", tc.ip, reason, tc.blocked)
			}
		})
	}
}
