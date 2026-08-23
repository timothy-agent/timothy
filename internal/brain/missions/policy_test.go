package missions

import "testing"

func TestPolicyFor(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		light bool
		want  missionPolicy
	}{
		{
			name: "coding",
			kind: KindCoding,
			want: missionPolicy{needsWorktree: true, alwaysReview: true, checksCitations: false, canDelegate: true, skipsPlanning: false, canPush: true},
		},
		{
			name: "coding light is meaningless but still coding-shaped",
			kind: KindCoding, light: true,
			want: missionPolicy{needsWorktree: true, alwaysReview: true, checksCitations: false, canDelegate: true, skipsPlanning: false, canPush: true},
		},
		{
			name: "general",
			kind: KindGeneral,
			want: missionPolicy{needsWorktree: false, alwaysReview: false, checksCitations: true, canDelegate: false, skipsPlanning: false, canPush: false},
		},
		{
			name: "general light",
			kind: KindGeneral, light: true,
			want: missionPolicy{needsWorktree: false, alwaysReview: false, checksCitations: true, canDelegate: false, skipsPlanning: true, canPush: false},
		},
		{
			name: "unknown kind fails conservative",
			kind: "bogus",
			want: missionPolicy{needsWorktree: false, alwaysReview: true, checksCitations: true, canDelegate: false, skipsPlanning: false, canPush: false},
		},
		{
			name: "unknown kind with light stays conservative, ignores light",
			kind: "bogus", light: true,
			want: missionPolicy{needsWorktree: false, alwaysReview: true, checksCitations: true, canDelegate: false, skipsPlanning: false, canPush: false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := policyFor(tc.kind, tc.light)
			if got != tc.want {
				t.Fatalf("policyFor(%q, %v) = %+v, want %+v", tc.kind, tc.light, got, tc.want)
			}
		})
	}
}

func TestMissionPolicyFor(t *testing.T) {
	m := Mission{Kind: KindGeneral, Light: true}
	got := missionPolicyFor(m)
	want := policyFor(KindGeneral, true)
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
		{name: "coding starts at explore", kind: KindCoding, want: PhaseExplore},
		{name: "general starts at explore", kind: KindGeneral, want: PhaseExplore},
		{name: "light general starts at execute", kind: KindGeneral, light: true, want: PhaseExecute},
		{name: "unknown kind starts at explore even if light requested", kind: "bogus", light: true, want: PhaseExplore},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := initialPhase(tc.kind, tc.light); got != tc.want {
				t.Fatalf("initialPhase(%q, %v) = %v, want %v", tc.kind, tc.light, got, tc.want)
			}
		})
	}
}
