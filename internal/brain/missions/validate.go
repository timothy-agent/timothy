package missions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/missions/executor"
)

// ErrInvalidMission is the sentinel every ValidateCreate rejection
// wraps — api/missions.go's failMission uses errors.Is against this to
// map a Driver.Create failure back to 400 instead of 500, the same way
// it already does for ErrNotFound/ErrBranchConflict/etc.
var ErrInvalidMission = errors.New("invalid mission")

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
	// ValidateDeps (wired the same way DestinationEnabled is) for a
	// future caller that wants that stricter check.
	RouteExists func(ctx context.Context, name string) bool
	// DestinationEnabled reports whether id names a real, enabled
	// destinations row (destinations.Store.EnabledByID) — nil skips
	// destination-entry validation entirely (same as api/missions.go's
	// validateDestinationIDs with h.destinations == nil, except this
	// degrades to "unchecked" rather than "reject every non-empty list",
	// since a caller with no destinations wiring has nothing to check
	// against).
	DestinationEnabled func(ctx context.Context, id string) (bool, error)
	// KBCollectionExists reports whether id names a real kb_collections
	// row: nil skips a "kb" destination entry's collection_id validation
	// entirely, same degrade-to-unchecked reasoning as DestinationEnabled.
	KBCollectionExists func(ctx context.Context, id string) (bool, error)
}

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
// regardless of which caller is creating it — the HTTP create handler
// and the workflows engine's spawnStep both call into Driver.Create,
// and only the HTTP handler used to validate anything (D-071). Callers
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
	if !ValidFlow(string(m.Flow)) {
		return fmt.Errorf("%w: unknown flow %q", ErrInvalidMission, m.Flow)
	}
	if m.Kind == KindCoding && m.Flow != FlowFull {
		return fmt.Errorf("%w: flow must be %q for kind=coding missions", ErrInvalidMission, FlowFull)
	}
	if m.Flow == FlowLight && m.Kind != KindGeneral {
		return fmt.Errorf("%w: flow=%q is only valid for kind=general missions", ErrInvalidMission, FlowLight)
	}
	githubEntry, hasGitHub := m.GitHubEntry()
	repoURL, connectorID := m.RepoURL(), m.ConnectorID()
	if !missionPolicyFor(m).canDelegate {
		switch {
		case m.Harness != "":
			return fmt.Errorf("%w: harness is only valid for kind=coding missions", ErrInvalidMission)
		case m.Environment != "":
			return fmt.Errorf("%w: environment is only valid for kind=coding missions", ErrInvalidMission)
		case repoURL != "":
			return fmt.Errorf("%w: repo_url is only valid for kind=coding missions", ErrInvalidMission)
		case hasGitHub:
			return fmt.Errorf("%w: a github destination is only valid for kind=coding missions", ErrInvalidMission)
		}
	}
	if m.Harness != "" {
		if _, ok := executor.Lookup(m.Harness); !ok {
			return fmt.Errorf("%w: unknown harness %q", ErrInvalidMission, m.Harness)
		}
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
	if hasGitHub {
		switch githubEntry.Mode {
		case "":
			// A github entry can carry only BranchPattern/CommitStyle with
			// no on_complete choice: the operator overriding the git
			// strategy without opting into auto-push.
		case "push", "push_pr":
			// connector_id is always required: it authenticates the
			// eventual push/PR call regardless of source. repo_url is
			// required UNLESS CreateIfMissing is set (issue #483): a
			// create-if-missing github entry legitimately has no repo yet
			// at create time (a scratch mission whose repo
			// destinations.GitHubAdapter creates at delivery, named from
			// the mission's own goal): its own ConnectorID (or, absent
			// that, the mission's clone-source connector_id) still has to
			// authenticate that create call.
			entryConnectorID := githubEntry.ConnectorID
			if entryConnectorID == "" {
				entryConnectorID = connectorID
			}
			switch {
			case githubEntry.CreateIfMissing:
				if entryConnectorID == "" {
					return fmt.Errorf("%w: create_if_missing requires connector_id on a kind=coding mission", ErrInvalidMission)
				}
			case repoURL == "" || connectorID == "":
				return fmt.Errorf("%w: on_complete requires repo_url and connector_id on a kind=coding mission", ErrInvalidMission)
			}
		default:
			return fmt.Errorf(`%w: on_complete must be "", "push", or "push_pr"`, ErrInvalidMission)
		}
		if githubEntry.BranchPattern != "" {
			if err := ValidateBranchPattern(githubEntry.BranchPattern); err != nil {
				return fmt.Errorf("%w: %s", ErrInvalidMission, err.Error())
			}
		}
		if err := ValidateCommitStyle(githubEntry.CommitStyle); err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidMission, err.Error())
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
	if deps.DestinationEnabled != nil {
		var invalid []string
		for _, id := range m.DestinationIDs() {
			ok, err := deps.DestinationEnabled(ctx, id)
			if err != nil {
				return fmt.Errorf("%w: destination_ids: %s", ErrInvalidMission, err.Error())
			}
			if !ok {
				invalid = append(invalid, id)
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
