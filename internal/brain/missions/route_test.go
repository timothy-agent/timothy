package missions

import (
	"context"
	"testing"
)

func TestDefaultCodingRoute(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		routeExists  func(context.Context, string) bool
		defaultRoute string
		want         string
	}{
		{
			name:         "prefers coding when it exists",
			routeExists:  func(context.Context, string) bool { return true },
			defaultRoute: "default",
			want:         "coding",
		},
		{
			name:         "falls back to default when coding does not exist",
			routeExists:  func(context.Context, string) bool { return false },
			defaultRoute: "default",
			want:         "default",
		},
		{
			name:         "nil routeExists skips straight to default",
			routeExists:  nil,
			defaultRoute: "default",
			want:         "default",
		},
		{
			name:         "falls back to empty default when coding does not exist and no default is configured",
			routeExists:  func(context.Context, string) bool { return false },
			defaultRoute: "",
			want:         "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DefaultCodingRoute(context.Background(), tc.routeExists, tc.defaultRoute); got != tc.want {
				t.Errorf("DefaultCodingRoute() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDefaultCodingRouteChecksThePreferredName confirms routeExists is
// called with exactly "coding" — the fixed convention DefaultCodingRoute
// prefers, never a value derived from anything else.
func TestDefaultCodingRouteChecksThePreferredName(t *testing.T) {
	t.Parallel()
	var gotName string
	routeExists := func(_ context.Context, name string) bool {
		gotName = name
		return false
	}
	DefaultCodingRoute(context.Background(), routeExists, "default")
	if gotName != "coding" {
		t.Errorf("routeExists called with %q, want %q", gotName, "coding")
	}
}
