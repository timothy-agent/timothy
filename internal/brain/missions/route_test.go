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

func TestResolveHarness(t *testing.T) {
	t.Parallel()
	settingsDefault := func(context.Context) string { return "codex-cli" }
	cases := []struct {
		name            string
		kind            string
		explicit        string
		agentHarness    string
		settingsDefault func(context.Context) string
		wantHarness     string
		wantSource      string
	}{
		{
			name: "explicit beats agent and settings",
			kind: KindCoding, explicit: "claude-cli", agentHarness: "pi", settingsDefault: settingsDefault,
			wantHarness: "claude-cli", wantSource: HarnessSourceExplicit,
		},
		{
			name: "agent beats settings",
			kind: KindCoding, explicit: "", agentHarness: "pi", settingsDefault: settingsDefault,
			wantHarness: "pi", wantSource: HarnessSourceAgent,
		},
		{
			name: "settings applies when agent is empty",
			kind: KindCoding, explicit: "", agentHarness: "", settingsDefault: settingsDefault,
			wantHarness: "codex-cli", wantSource: HarnessSourceSettings,
		},
		{
			name: "native explicit normalizes to native (empty)",
			kind: KindCoding, explicit: "native", agentHarness: "pi", settingsDefault: settingsDefault,
			wantHarness: "", wantSource: "",
		},
		{
			name: "nil settingsDefault with nothing else set stays native",
			kind: KindCoding, explicit: "", agentHarness: "", settingsDefault: nil,
			wantHarness: "", wantSource: "",
		},
		{
			name: "non-coding kind ignores harness entirely",
			kind: KindGeneral, explicit: "claude-cli", agentHarness: "pi", settingsDefault: settingsDefault,
			wantHarness: "", wantSource: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			harness, source := ResolveHarness(context.Background(), tc.kind, tc.explicit, tc.agentHarness, tc.settingsDefault)
			if harness != tc.wantHarness || source != tc.wantSource {
				t.Errorf("ResolveHarness() = (%q, %q), want (%q, %q)", harness, source, tc.wantHarness, tc.wantSource)
			}
		})
	}
}
