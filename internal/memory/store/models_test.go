package store

import (
	"testing"
)

func TestVectorRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   Vector
		want string
	}{
		{name: "nil", in: nil, want: ""},
		{name: "empty", in: Vector{}, want: ""},
		{name: "single", in: Vector{0.5}, want: "[0.5]"},
		{name: "several", in: Vector{1, -0.25, 3.5}, want: "[1,-0.25,3.5]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.in.String()
			if got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
			back, err := ParseVector(got)
			if err != nil {
				t.Fatalf("ParseVector(%q): %v", got, err)
			}
			if len(back) != len(tc.in) {
				t.Fatalf("round trip length %d, want %d", len(back), len(tc.in))
			}
			for i := range back {
				if back[i] != tc.in[i] {
					t.Fatalf("round trip [%d] = %v, want %v", i, back[i], tc.in[i])
				}
			}
		})
	}
}

func TestParseVectorRejectsGarbage(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"1,2,3", "[1,2", "[a,b]", "[]extra"} {
		if _, err := ParseVector(s); err == nil {
			t.Fatalf("ParseVector(%q) succeeded, want error", s)
		}
	}
}

func TestParseVectorPgvectorSpacing(t *testing.T) {
	t.Parallel()
	// pgvector's text output has no spaces, but tolerate them.
	v, err := ParseVector("[0.1, 0.2, 0.3]")
	if err != nil {
		t.Fatalf("ParseVector: %v", err)
	}
	if len(v) != 3 {
		t.Fatalf("len = %d, want 3", len(v))
	}
}
