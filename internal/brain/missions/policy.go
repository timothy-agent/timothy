package missions

// KindCoding and KindGeneral are the two mission kinds — see
// Mission.Kind.
const (
	KindCoding  = "coding"
	KindGeneral = "general"
)

// Flow names the phase set a mission runs, chosen once at create time
// and snapshotted onto the row (D-090, issue #459): never model-
// mutable, no tool or sentinel arg can change it.
type Flow string

const (
	// FlowFull is discover->plan->generate->prove->result, today's
	// default and the only flow that existed before #459.
	FlowFull Flow = "full"
	// FlowDiscoverGenerate is a true planless flow: discover->generate
	// ->result, no plan, no review. Discover runs as normal (its
	// findings land in Mission.ExploreNotes); generate's own turn then
	// runs the exact D-069 light worker path (Mission.RunsPlanless):
	// WorkPacket.Light rendering, no plan units, the worker's final
	// message (mission_status's final_output) is the deliverable. The
	// only difference from a plain light mission is that this one runs
	// discover first, and its explore notes reach the planless prompt
	// via WorkPacket.ExploreNotes.
	FlowDiscoverGenerate Flow = "discover_generate"
	// FlowNoProve is discover->plan->generate->result: skips only the
	// LLM reviewer round. CheckArtifacts (harness evidence) still runs
	// on generate's exit for any unit declaring artifacts, same as
	// today's review_skipped path for non-coding missions.
	FlowNoProve Flow = "no_prove"
	// FlowLight is generate->result (D-069): existing light behavior.
	FlowLight Flow = "light"
)

// ValidFlow reports whether raw names one of the four defined flows.
func ValidFlow(raw string) bool {
	switch Flow(raw) {
	case FlowFull, FlowDiscoverGenerate, FlowNoProve, FlowLight:
		return true
	default:
		return false
	}
}

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
// (general only, D-069) and flow's effect (D-090, issue #459):
// flow=no_prove forces alwaysReview false on a general-shaped policy,
// since skipping the LLM reviewer is the whole point of choosing it
// (CheckArtifacts in verifier.go still runs via trySkipReview either
// way). flow=discover_generate does NOT need this override: it never
// reaches trySkipReview at all, its generate turn takes the same
// planless short-circuit as light (Mission.RunsPlanless), which never
// consults alwaysReview. Coding stays alwaysReview true regardless of
// flow (ValidateCreate rejects any non-full flow for kind=coding at
// create time, so this is a second, defensive belt, not the
// enforcement point). An unknown kind also stays alwaysReview true:
// fail conservative, a kind this table doesn't recognize must never
// silently skip review.
//
// D-072: the single source of behavior-by-kind; every call site that
// used to test Kind/Light directly now derives it from here instead.
func policyFor(kind string, light bool, flow Flow) missionPolicy {
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
		if flow == FlowNoProve {
			p.alwaysReview = false
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
	return policyFor(m.Kind, m.Light, m.Flow)
}

// RunsPlanless reports whether m's generate phase runs the D-069
// light worker path: no plan, no artifact check, the worker's final
// message (mission_status's final_output) is the deliverable.
// FlowDiscoverGenerate (D-090, issue #459) shares this exact worker
// behavior with Light, the only difference being it runs discover
// first; unlike Light (missionPolicy.skipsPlanning), it is NOT used
// by initialPhase: a discover_generate mission is still born in
// PhaseDiscover, only generate itself runs planless. Exported: read
// outside this package by destinations.renderPayload, which needs the
// same "final_output IS the result" gate memory.go's digest uses.
func (m Mission) RunsPlanless() bool {
	return m.Light || m.Flow == FlowDiscoverGenerate
}

// initialPhase is the phase a newly created mission row starts in:
// PhaseGenerate for a light mission (D-069, skips discover/plan),
// PhaseDiscover otherwise. Shared by store.go's Create and
// scheduler.go's createFromTemplate, which both used to duplicate
// this check inline.
func initialPhase(kind string, light bool) Phase {
	if policyFor(kind, light, FlowFull).skipsPlanning {
		return PhaseGenerate
	}
	return PhaseDiscover
}
