package builtin

import (
	"fmt"
	"strings"
	"testing"
)

func TestHeadTailWriter(t *testing.T) {
	t.Parallel()
	const mib = 1 << 20
	cases := []struct {
		name        string
		size        int
		chunk       int
		wantDropped int
	}{
		{"under cap", 1000, 100, 0},
		{"exactly cap", ShellHeadBytes + ShellTailBytes, 4096, 0},
		{"1 MiB in 4 KiB writes", mib, 4096, mib - ShellHeadBytes - ShellTailBytes},
		{"1 MiB in one write", mib, mib, mib - ShellHeadBytes - ShellTailBytes},
		{"1 MiB in tiny writes", mib, 7, mib - ShellHeadBytes - ShellTailBytes},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := make([]byte, tc.size)
			for i := range src {
				src[i] = byte('a' + i%26)
			}
			w := NewHeadTailWriter(ShellHeadBytes, ShellTailBytes)
			for off := 0; off < len(src); off += tc.chunk {
				end := min(off+tc.chunk, len(src))
				if n, err := w.Write(src[off:end]); err != nil || n != end-off {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			if w.Dropped() != tc.wantDropped {
				t.Fatalf("dropped = %d, want %d", w.Dropped(), tc.wantDropped)
			}
			got := w.String()
			if tc.wantDropped == 0 {
				if got != string(src) {
					t.Fatal("undropped stream changed")
				}
				return
			}
			marker := fmt.Sprintf("\n[%d bytes dropped]\n", tc.wantDropped)
			want := string(src[:ShellHeadBytes]) + marker + string(src[len(src)-ShellTailBytes:])
			if got != want {
				t.Fatalf("head, marker and tail mismatch (len %d, want %d)", len(got), len(want))
			}
			if !strings.Contains(got, marker) {
				t.Fatal("marker missing")
			}
		})
	}
}
