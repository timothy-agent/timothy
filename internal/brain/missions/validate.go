package missions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/gitprovider"
	"github.com/SumonMSelim/timothy/internal/brain/missions/executor"
)

// ErrInvalidMission is the sentinel every ValidateCreate rejection
// wraps — api/missions.go's failMission uses errors.Is against this to
// map a Driver.Create failure back to 400 instead of 500, the same way
// it already does for ErrNotFound/ErrBranchConflict/etc.
var ErrInvalidMission = errors.New("invalid mission")

// ErrToolAllowlistHarness reports that m's harness cannot honor its
// tool_allowlist (D-119, issue #865): a delegated CLI's surface is its
// own sandbox shell and file edit, never narrowed per tool, so a
// tool_allowlist is only compatible when it grants both.
var ErrToolAllowlistHarness = errors.New("harness cannot honor tool_allowlist")

// ValidateDeps are the store-backed checks ValidateCreate needs beyond
// the Mission struct itself — each nil-gated: an unset func skips that
// one check rather than failing closed, same contract as Driver's other
// optional deps (SetAgentResolver, SetCapacityGate, etc).
type ValidateDeps struct {
	// RouteExists reports whether name resolves to a real configured
	// route. NOT consulted by ValidateCreate: a route name the gateway
	// doesn't recognize surfaces naturally as a run-time gateway error on
	// the mission's first turn, same as it always has, and RouteExists'
	// bool-only signature can't distinguish "unknown route" from "gateway
	// unreachable" — folding it into create-time validation would risk
	// rejecting a momentarily-unreachable-but-valid route. Kept on
	// ValidateDeps (wired the same way DestinationKind is) for a
	// future caller that wants that stricter check.
	RouteExists func(ctx context.Context, name string) bool
	// DestinationKind reports a destination row's kind and enabled state
	// (destinations.Store.KindByID), nil skips destination-entry
	// validation entirely (same as api/missions.go's
	// validateDestinationIDs with h.destinations == nil, except this
	// degrades to "unchecked" rather than "reject every non-empty list",
	// since a caller with no destinations wiring has nothing to check
	// against). ok is false for an unknown or disabled id.
	DestinationKind func(ctx context.Context, id string) (kind string, enabled bool, err error)
	// KBCollectionExists reports whether id names a real kb_collections
	// row: nil skips a "kb" destination entry's collection_id validation
	// entirely, same degrade-to-unchecked reasoning as DestinationKind.
	KBCollectionExists func(ctx context.Context, id string) (bool, error)
}

// maxToolAllowlist caps a mission's tool_allowlist entries at the
// automation trigger cap, so an intersected trigger list always fits.
const maxToolAllowlist = 64

// validModelPin reports whether pin is well-formed "provider name/model"
// (D-078) — a non-empty provider part and a non-empty model part
// separated by the LAST '/', matching router.go's splitProviderModelHint.
// Never checks the pin against a live chain: a chain can change after
// create, and the runtime already falls back to first-usable when a
// wellformed pin names no current entry.
func validModelPin(pin string) bool {
	i := strings.LastIndex(pin, "/")
	if i <= 0 || i == len(pin)-1 {
		return false
	}
	return true
}

// ValidateCreate enforces the domain rules a mission row must satisfy
// regardless of which caller is creating it: the HTTP create handler,
// automation runs and the workflows engine's spawnStep all
// call into Driver.Create, and only the HTTP handler used to validate
// anything (D-071). Callers
// must resolve their own defaults (kind, route, environment auto-detect)
// before calling: ValidateCreate rejects an empty route rather than
// silently picking one, so a caller that wants "the default route"
// resolves it first.
//
// deps may be the zero ValidateDeps{} (every dep-backed check skipped)
// or have individual fields nil (that check skipped) — never required.
func ValidateCreate(ctx context.Context, m Mission, deps ValidateDeps) error {
	switch m.Kind {
	case KindCoding, KindGeneral:
	default:
		return fmt.Errorf(`%w: kind must be "coding" or "general"`, ErrInvalidMission)
	}
	if !ValidOrigin(m.OriginKind) {
		return fmt.Errorf("%w: unknown origin_kind %q", ErrInvalidMission, m.OriginKind)
	}
	if !ValidFlow(string(m.Flow)) {
		return fmt.Errorf("%w: unknown flow %q", ErrInvalidMission, m.Flow)
	}
	if m.Kind == KindCoding && m.Flow != FlowFull {
		return fmt.Errorf("%w: flow must be %q for kind=coding missions", ErrInvalidMission, FlowFull)
	}
	if m.Flow == FlowLight && m.Kind != KindGeneral {
		return fmt.Errorf("%w: flow=%q is only valid for kind=general missions", ErrInvalidMission, FlowLight)
	}
	repoURL, connectorID := m.RepoURL(), m.ConnectorID()
	if !missionPolicyFor(m).canDelegate {
		switch {
		case m.Harness != "":
			return fmt.Errorf("%w: harness is only valid for kind=coding missions", ErrInvalidMission)
		case m.Environment != "":
			return fmt.Errorf("%w: environment is only valid for kind=coding missions", ErrInvalidMission)
		case m.ExecutorSessionPolicy != "":
			return fmt.Errorf("%w: executor_session_policy is only valid for kind=coding missions", ErrInvalidMission)
		case repoURL != "":
			return fmt.Errorf("%w: repo_url is only valid for kind=coding missions", ErrInvalidMission)
		}
	}
	if m.Harness != "" {
		if _, ok := executor.Lookup(m.Harness); !ok {
			return fmt.Errorf("%w: unknown harness %q", ErrInvalidMission, m.Harness)
		}
	}
	if err := CheckToolAllowlistHarness(m); err != nil {
		return err
	}
	if m.ReviewHarness != "" {
		if _, ok := executor.Lookup(m.ReviewHarness); !ok {
			return fmt.Errorf("%w: unknown review_harness %q", ErrInvalidMission, m.ReviewHarness)
		}
	}
	switch m.ExecutorSessionPolicy {
	case "", SessionPolicyResume, SessionPolicyFresh:
	default:
		return fmt.Errorf("%w: unknown executor_session_policy %q", ErrInvalidMission, m.ExecutorSessionPolicy)
	}
	if !ValidEnvironment(m.Environment) {
		return fmt.Errorf("%w: unknown environment %q", ErrInvalidMission, m.Environment)
	}
	switch {
	case repoURL != "" && connectorID == "":
		return fmt.Errorf("%w: connector_id is required with repo_url", ErrInvalidMission)
	case repoURL == "" && connectorID != "":
		return fmt.Errorf("%w: connector_id is only valid alongside repo_url", ErrInvalidMission)
	}
	// github's parser accepts any host, so only the host-pinned kinds
	// can be checked here; each validates against its own descriptor.
	if e, ok := m.repoSource(); ok && e.Source != SourceKindGitHub {
		if _, _, ok := parseRepoURLForKind(e.Source, e.RepoURL); !ok {
			return fmt.Errorf("%w: repo_url is not a recognizable %s https clone URL", ErrInvalidMission, e.Source)
		}
	}
	if m.Route == "" {
		return fmt.Errorf("%w: route is required", ErrInvalidMission)
	}
	switch {
	case m.RouteModel != "" && !validModelPin(m.RouteModel):
		return fmt.Errorf(`%w: route_model must be "provider name/model"`, ErrInvalidMission)
	case m.PlanRouteModel != "" && !validModelPin(m.PlanRouteModel):
		return fmt.Errorf(`%w: plan_route_model must be "provider name/model"`, ErrInvalidMission)
	case m.ReviewRouteModel != "" && !validModelPin(m.ReviewRouteModel):
		return fmt.Errorf(`%w: review_route_model must be "provider name/model"`, ErrInvalidMission)
	}
	if len(m.ToolAllowlist) > maxToolAllowlist {
		return fmt.Errorf("%w: tool_allowlist has more than %d entries", ErrInvalidMission, maxToolAllowlist)
	}
	if slices.Contains(m.ToolAllowlist, "") {
		return fmt.Errorf("%w: tool_allowlist entries must be non-empty", ErrInvalidMission)
	}
	for _, e := range m.Destinations {
		if e.DestinationID == "" || e.RepoURL == "" {
			continue
		}
		if !recognizableRepoURL(e.RepoURL) {
			return fmt.Errorf("%w: repo_url is not a recognizable https clone URL", ErrInvalidMission)
		}
	}
	if deps.DestinationKind != nil {
		var invalid []string
		for _, e := range m.Destinations {
			if e.DestinationID == "" {
				continue
			}
			kind, enabled, err := deps.DestinationKind(ctx, e.DestinationID)
			if err != nil {
				return fmt.Errorf("%w: destination_ids: %s", ErrInvalidMission, err.Error())
			}
			if !enabled {
				invalid = append(invalid, e.DestinationID)
				continue
			}
			repoKind := gitprovider.IsKind(kind)
			if repoKind && !missionPolicyFor(m).canDelegate {
				return fmt.Errorf("%w: a %s destination is only valid for kind=coding missions", ErrInvalidMission, kind)
			}
			if !repoKind && e.RepoURL != "" {
				return fmt.Errorf("%w: repo_url is only valid for a git provider destination entry", ErrInvalidMission)
			}
			// Each kind's descriptor is host-pinned, so another host's URL
			// never resolves against this connector's API (issue #787).
			if repoKind && e.RepoURL != "" {
				d, _ := gitprovider.Lookup(gitprovider.Kind(kind))
				if _, ok := d.ParseRepoURL(e.RepoURL); !ok {
					return fmt.Errorf("%w: repo_url is not a recognizable %s https clone URL", ErrInvalidMission, kind)
				}
			}
		}
		if len(invalid) > 0 {
			return fmt.Errorf("%w: unknown or disabled destination id(s): %s", ErrInvalidMission, strings.Join(invalid, ", "))
		}
	}
	if deps.KBCollectionExists != nil {
		if collectionID := m.KBCollectionID(); collectionID != "" {
			ok, err := deps.KBCollectionExists(ctx, collectionID)
			if err != nil {
				return fmt.Errorf("%w: promote_kb_collection_id: %s", ErrInvalidMission, err.Error())
			}
			if !ok {
				return fmt.Errorf("%w: unknown promote_kb_collection_id %q", ErrInvalidMission, collectionID)
			}
		}
	}
	return nil
}

// CheckToolAllowlistHarness rejects a mission whose harness cannot
// honor its tool_allowlist (D-119, issue #865): a delegated CLI has no
// per-tool gate of its own, so a tool_allowlist narrower than
// delegatedWorkerTools cannot be enforced. "" harness (native) always
// passes.
func CheckToolAllowlistHarness(m Mission) error {
	if m.Harness == "" {
		return nil
	}
	gap := m.delegatedAllowlistGap()
	if len(gap) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w: harness %q runs its own shell and file tools and cannot be narrowed; tool_allowlist lacks %s (the agent's Tools must grant them too) or set harness to native",
		ErrInvalidMission, ErrToolAllowlistHarness, m.Harness, strings.Join(gap, ", "))
}
