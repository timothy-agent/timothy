package missions

import "testing"

func TestPolicyFor(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		light bool
		flow  Flow
		want  missionPolicy
	}{
		{
			name: "coding",
			kind: KindCoding, flow: FlowFull,
			want: missionPolicy{needsWorktree: true, alwaysReview: true, checksCitations: false, canDelegate: true, skipsPlanning: false, canPush: true},
		},
		{
			name: "coding light is meaningless but still coding-shaped",
			kind: KindCoding, light: true, flow: FlowFull,
			want: missionPolicy{needsWorktree: true, alwaysReview: true, checksCitations: false, canDelegate: true, skipsPlanning: false, canPush: true},
		},
		{
			name: "general",
			kind: KindGeneral, flow: FlowFull,
			want: missionPolicy{needsWorktree: false, alwaysReview: false, checksCitations: true, canDelegate: false, skipsPlanning: false, canPush: false},
		},
		{
			name: "general light",
			kind: KindGeneral, light: true, flow: FlowLight,
			want: missionPolicy{needsWorktree: false, alwaysReview: false, checksCitations: true, canDelegate: false, skipsPlanning: true, canPush: false},
		},
		{
			name: "unknown kind fails conservative",
			kind: "bogus", flow: FlowFull,
			want: missionPolicy{needsWorktree: false, alwaysReview: true, checksCitations: true, canDelegate: false, skipsPlanning: false, canPush: false},
		},
		{
			name: "unknown kind with light stays conservative, ignores light",
			kind: "bogus", light: true, flow: FlowFull,
			want: missionPolicy{needsWorktree: false, alwaysReview: true, checksCitations: true, canDelegate: false, skipsPlanning: false, canPush: false},
		},
		{
			name: "coding no_prove still always reviews (D-090: coding stays full)",
			kind: KindCoding, flow: FlowNoProve,
			want: missionPolicy{needsWorktree: true, alwaysReview: true, checksCitations: false, canDelegate: true, skipsPlanning: false, canPush: true},
		},
		{
			name: "general no_prove skips review",
			kind: KindGeneral, flow: FlowNoProve,
			want: missionPolicy{needsWorktree: false, alwaysReview: false, checksCitations: true, canDelegate: false, skipsPlanning: false, canPush: false},
		},
		{
			// discover_generate never reaches trySkipReview (its generate
			// turn takes the planless short-circuit instead), so policyFor
			// does not special-case it: this is the plain general policy,
			// same as flow=full; alwaysReview is false here only because
			// that is KindGeneral's own baseline, not a flow override.
			name: "general discover_generate: policyFor has no special case",
			kind: KindGeneral, flow: FlowDiscoverGenerate,
			want: missionPolicy{needsWorktree: false, alwaysReview: false, checksCitations: true, canDelegate: false, skipsPlanning: false, canPush: false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := policyFor(tc.kind, tc.light, tc.flow)
			if got != tc.want {
				t.Fatalf("policyFor(%q, %v, %q) = %+v, want %+v", tc.kind, tc.light, tc.flow, got, tc.want)
			}
		})
	}
}

func TestMissionPolicyFor(t *testing.T) {
	m := Mission{Kind: KindGeneral, Light: true, Flow: FlowLight}
	got := missionPolicyFor(m)
	want := policyFor(KindGeneral, true, FlowLight)
	if got != want {
		t.Fatalf("missionPolicyFor(%+v) = %+v, want %+v", m, got, want)
	}
}

func TestInitialPhase(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		light bool
		want  Phase
	}{
		{name: "coding starts at discover", kind: KindCoding, want: PhaseDiscover},
		{name: "general starts at discover", kind: KindGeneral, want: PhaseDiscover},
		{name: "light general starts at generate", kind: KindGeneral, light: true, want: PhaseGenerate},
		{name: "unknown kind starts at discover even if light requested", kind: "bogus", light: true, want: PhaseDiscover},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := initialPhase(tc.kind, tc.light); got != tc.want {
				t.Fatalf("initialPhase(%q, %v) = %v, want %v", tc.kind, tc.light, got, tc.want)
			}
		})
	}
}
