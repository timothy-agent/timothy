package missions

// KindCoding and KindGeneral are the two mission kinds — see
// Mission.Kind.
const (
	KindCoding  = "coding"
	KindGeneral = "general"
)

// missionPolicy derives every kind/light-dependent behavior once
// (D-072) — call sites consult the table instead of re-testing
// Kind/Light strings.
type missionPolicy struct {
	needsWorktree   bool // clone/branch/rollback machinery
	alwaysReview    bool // LLM review round can never be skipped
	checksCitations bool // CheckCitations on verify
	canDelegate     bool // harness (delegated CLI executor) allowed
	skipsPlanning   bool // born in execute; no explore/plan/review
	canPush         bool // on_complete push/push_pr allowed
}

// policyFor derives kind's policy, then folds in light's effect
// (general only, D-069). An unknown kind gets the general-shaped
// policy with alwaysReview forced true — fail conservative: a kind
// this table doesn't recognize must never silently skip review.
//
// D-072: the single source of behavior-by-kind; every call site that
// used to test Kind/Light directly now derives it from here instead.
func policyFor(kind string, light bool) missionPolicy {
	switch kind {
	case KindCoding:
		return missionPolicy{
			needsWorktree:   true,
			alwaysReview:    true,
			checksCitations: false,
			canDelegate:     true,
			skipsPlanning:   false,
			canPush:         true,
		}
	case KindGeneral:
		p := missionPolicy{
			needsWorktree:   false,
			alwaysReview:    false,
			checksCitations: true,
			canDelegate:     false,
			skipsPlanning:   false,
			canPush:         false,
		}
		if light {
			p.skipsPlanning = true
		}
		return p
	default:
		return missionPolicy{
			needsWorktree:   false,
			alwaysReview:    true,
			checksCitations: true,
			canDelegate:     false,
			skipsPlanning:   false,
			canPush:         false,
		}
	}
}

// missionPolicyFor is policyFor over a live Mission row.
func missionPolicyFor(m Mission) missionPolicy {
	return policyFor(m.Kind, m.Light)
}

// initialPhase is the phase a newly created mission row starts in:
// PhaseExecute for a light mission (D-069, skips explore/plan),
// PhaseExplore otherwise. Shared by store.go's Create and
// scheduler.go's createFromTemplate, which both used to duplicate
// this check inline.
func initialPhase(kind string, light bool) Phase {
	if policyFor(kind, light).skipsPlanning {
		return PhaseExecute
	}
	return PhaseExplore
}
