package missions

// KindCoding and KindGeneral are the two mission kinds — see
// Mission.Kind.
const (
	KindCoding  = "coding"
	KindGeneral = "general"
)

// SessionPolicyResume and SessionPolicyFresh are Mission.
// ExecutorSessionPolicy's values (issue #720): resume (the default,
// "" means the same) carries the prior CLI session into the next unit;
// fresh starts every run cold.
const (
	SessionPolicyResume = "resume"
	SessionPolicyFresh  = "fresh"
)

// Flow names the phase set a mission runs, chosen once at create time
// and snapshotted onto the row (D-090, issue #459): never model-
// mutable, no tool or sentinel arg can change it.
type Flow string

const (
	// FlowFull is discover->plan->build->prove->result, today's
	// default and the only flow that existed before #459.
	FlowFull Flow = "full"
	// FlowDiscoverBuild is a true planless flow: discover->build
	// ->result, no plan, no review. Discover runs as normal (its
	// findings land in Mission.DiscoverNotes); build's own turn then
	// runs the exact D-069 light worker path (Mission.RunsPlanless):
	// WorkPacket.Light rendering, no plan units, the worker's final
	// message (mission_status's final_output) is the deliverable. The
	// only difference from a plain light mission is that this one runs
	// discover first, and its discover notes reach the planless prompt
	// via WorkPacket.DiscoverNotes.
	FlowDiscoverBuild Flow = "discover_build"
	// FlowNoProve is discover->plan->build->result: skips only the
	// LLM reviewer round. CheckArtifacts (harness evidence) still runs
	// on build's exit for any unit declaring artifacts, same as
	// today's review_skipped path for non-coding missions.
	FlowNoProve Flow = "no_prove"
	// FlowLight is build->result (D-069): existing light behavior.
	FlowLight Flow = "light"
)

// ValidFlow reports whether raw names one of the four defined flows.
func ValidFlow(raw string) bool {
	switch Flow(raw) {
	case FlowFull, FlowDiscoverBuild, FlowNoProve, FlowLight:
		return true
	default:
		return false
	}
}

// parseFlow maps a stored flow string onto a Flow, translating the
// pre-#611 discover_generate spelling onto FlowDiscoverBuild. Rows a
// data migration hasn't touched yet still carry the old value; drop
// this alias once scripts/pending-alters.md has run everywhere and the
// first stable release ships. An unknown value passes through
// unchanged, leaving validate.go's ValidFlow check to reject it.
func parseFlow(raw string) Flow {
	if raw == "discover_generate" {
		return FlowDiscoverBuild
	}
	return Flow(raw)
}

// missionPolicy derives every kind/light-dependent behavior once
// (D-072) — call sites consult the table instead of re-testing
// Kind/Light strings.
type missionPolicy struct {
	needsWorktree   bool // clone/branch/rollback machinery
	alwaysReview    bool // LLM review round can never be skipped
	checksCitations bool // CheckCitations on verify
	canDelegate     bool // harness (delegated CLI executor) allowed
	skipsPlanning   bool // born in build; no discover/plan/prove
	canPush         bool // on_complete push/push_pr allowed
}

// policyFor derives kind's policy, then folds in flow's effect
// (D-090, issue #459): flow=light (general only, D-069) sets
// skipsPlanning; flow=no_prove forces alwaysReview false on a
// general-shaped policy, since skipping the LLM reviewer is the whole
// point of choosing it (CheckArtifacts in verifier.go still runs via
// routeVerified either way). flow=discover_build does NOT need this
// override: it never reaches routeVerified at all, its build turn
// takes the same planless short-circuit as flow=light
// (Mission.RunsPlanless), which never consults alwaysReview. Coding
// stays alwaysReview true regardless of flow (ValidateCreate rejects
// any non-full flow for kind=coding at create time, so this is a
// second, defensive belt, not the enforcement point). An unknown kind
// also stays alwaysReview true: fail conservative, a kind this table
// doesn't recognize must never silently skip review.
//
// D-072: the single source of behavior-by-kind; every call site that
// used to test Kind/Light directly now derives it from here instead.
func policyFor(kind string, flow Flow) missionPolicy {
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
		if flow == FlowLight {
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
	return policyFor(m.Kind, m.Flow)
}

// RunsPlanless reports whether m's build phase runs the D-069
// light worker path: no plan, no artifact check, the worker's final
// message (mission_status's final_output) is the deliverable.
// FlowDiscoverBuild (D-090, issue #459) shares this exact worker
// behavior with FlowLight, the only difference being it runs discover
// first; unlike FlowLight (missionPolicy.skipsPlanning), it is NOT
// used by initialPhase: a discover_build mission is still born in
// PhaseDiscover, only build itself runs planless. Exported: read
// outside this package by destinations.renderPayload, which needs the
// same "final_output IS the result" gate memory.go's digest uses.
func (m Mission) RunsPlanless() bool {
	return m.Flow == FlowLight || m.Flow == FlowDiscoverBuild
}

// initialPhase is the phase a newly created mission row starts in:
// PhaseBuild for a flow=light mission (D-069, skips discover/plan),
// PhaseDiscover otherwise. Shared by store.go's Create and
// scheduler.go's createFromTemplate, which both used to duplicate
// this check inline.
func initialPhase(kind string, flow Flow) Phase {
	if policyFor(kind, flow).skipsPlanning {
		return PhaseBuild
	}
	return PhaseDiscover
}
