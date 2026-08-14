package netguard

import (
	"net"
	"testing"
)

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
