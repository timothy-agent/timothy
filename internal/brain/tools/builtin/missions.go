package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// MissionRecord is the missions tool's own view of a missions.Mission
// — kept as a local struct (not an import of internal/brain/missions)
// because missions itself imports this package (runner.go's
// builtin.Shell/WriteFile); missions.Mission would create an import
// cycle. cmd/brain/main.go, which imports both packages, adapts real
// missions.Mission values into this shape.
type MissionRecord struct {
	ID           string
	Name         string
	Goal         string
	Kind         string
	Phase        string
	Status       string
	Iteration    int
	Harness      string
	RepoURL      string
	Branch       string
	ConnectorID  string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	UnitsPassed  int
	UnitsTotal   int
	PauseReason  string
	PauseMessage string
	OnComplete   string
	// NotPushableReason is missions.NotPushable(m) precomputed by the
	// adapter (that check lives in missions, same import-cycle reason
	// as the rest of this type) — empty means pushable.
	NotPushableReason string
}

// MissionEvent is the missions tool's own view of a missions.Event —
// same import-cycle reasoning as MissionRecord; only the fields
// finding the latest PR url needs.
type MissionEvent struct {
	Kind    string
	Payload json.RawMessage
}

// missionLister is the narrow slice of missions access the list/get
// and push_mission_branch tools need — List and Get, kept as an interface so
// tests fake it without a real Postgres pool.
type missionLister interface {
	ListMissions(ctx context.Context, limit int) ([]MissionRecord, error)
	GetMission(ctx context.Context, id string) (MissionRecord, error)
}

// missionEventReader is the narrow slice of missions access
// get_mission needs to find a mission's latest PR URL, if any.
type missionEventReader interface {
	MissionEvents(ctx context.Context, id string) ([]MissionEvent, error)
}

const (
	missionsListDefaultLimit = 10
	missionsListMaxLimit     = 50
)

type listMissionsArgs struct {
	Limit int `json:"limit"`
}

type getMissionArgs struct {
	Query string `json:"query"`
	ID    string `json:"id"`
}

// missionPROpenedPayload is the shape of a mission.pr_opened event's
// payload (see missions.Completer.OpenPR).
type missionPROpenedPayload struct {
	URL string `json:"url"`
}

// missionSnapshot is the compact JSON shape returned for a single
// mission — the model reads this, so field names stay short and only
// non-empty/non-zero fields worth reporting are included.
type missionSnapshot struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Goal         string `json:"goal"`
	Kind         string `json:"kind"`
	Phase        string `json:"phase"`
	Status       string `json:"status"`
	Iteration    int    `json:"iteration"`
	Harness      string `json:"harness,omitempty"`
	RepoURL      string `json:"repo_url,omitempty"`
	Branch       string `json:"branch,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	UnitsPassed  int    `json:"units_passed"`
	UnitsTotal   int    `json:"units_total"`
	PauseReason  string `json:"pause_reason,omitempty"`
	PauseMessage string `json:"pause_message,omitempty"`
	PRURL        string `json:"pr_url,omitempty"`
	OnComplete   string `json:"on_complete,omitempty"`
}

// missionListItem is the compact shape for the list-mode result.
type missionListItem struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Phase     string `json:"phase"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

func toMissionListItem(m MissionRecord) missionListItem {
	name := m.Name
	if name == "" {
		name = m.Goal
	}
	return missionListItem{
		ID: m.ID, Name: name, Phase: m.Phase, Status: m.Status,
		UpdatedAt: m.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func toMissionSnapshot(ctx context.Context, events missionEventReader, m MissionRecord) missionSnapshot {
	snap := missionSnapshot{
		ID: m.ID, Name: m.Name, Goal: m.Goal, Kind: m.Kind,
		Phase: m.Phase, Status: m.Status, Iteration: m.Iteration,
		Harness: m.Harness, RepoURL: m.RepoURL, Branch: m.Branch,
		CreatedAt:   m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:   m.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UnitsPassed: m.UnitsPassed, UnitsTotal: m.UnitsTotal,
		PauseReason: m.PauseReason, PauseMessage: m.PauseMessage,
		OnComplete: m.OnComplete,
	}
	if events != nil {
		if evs, err := events.MissionEvents(ctx, m.ID); err == nil {
			for i := len(evs) - 1; i >= 0; i-- {
				if evs[i].Kind != "mission.pr_opened" {
					continue
				}
				var p missionPROpenedPayload
				if err := json.Unmarshal(evs[i].Payload, &p); err == nil {
					snap.PRURL = p.URL
				}
				break
			}
		}
	}
	return snap
}

// findMission resolves id or a query substring match against
// name/goal to exactly one mission — ambiguous or absent matches
// return a descriptive error rather than guessing.
func findMission(ctx context.Context, store missionLister, id, query string) (MissionRecord, error) {
	if id != "" {
		m, err := store.GetMission(ctx, id)
		if err != nil {
			return MissionRecord{}, fmt.Errorf("no mission found with id %q", id)
		}
		return m, nil
	}
	all, err := store.ListMissions(ctx, 0)
	if err != nil {
		return MissionRecord{}, fmt.Errorf("list missions: %w", err)
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var matches []MissionRecord
	for _, m := range all {
		if strings.Contains(strings.ToLower(m.Name), q) || strings.Contains(strings.ToLower(m.Goal), q) {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return MissionRecord{}, fmt.Errorf("no mission matches %q", query)
	case 1:
		return matches[0], nil
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%q matches %d missions, be more specific or use id:\n", query, len(matches))
		for _, m := range matches {
			fmt.Fprintf(&b, "- %s: %s\n", m.ID, toMissionListItem(m).Name)
		}
		return MissionRecord{}, fmt.Errorf("%s", b.String())
	}
}

// ListMissions is a read-only, permission-exempt tool: pure reads over
// the missions store, no side effects. Lists recent missions for
// questions like "what missions have I run recently?". Registered
// only when store is non-nil (missions engine wired, see
// cmd/brain/main.go's WORKSPACES gate).
func ListMissions(store missionLister) *tools.Tool {
	return &tools.Tool{
		Name: "list_missions",
		Description: `Lists recent missions from the harness's own records — never the worker/reviewer's claims.

Arguments (optional):
- limit (int): max missions to list (default 10, max 50).

Each list item has id/name/phase/status/updated_at. Use get_mission
with the id to read a full status snapshot.

Example: {} → the 10 most recently updated missions.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"limit": {"type": "integer", "description": "Max missions to list (default 10, max 50)"}
			},
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args listMissionsArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			limit := args.Limit
			if limit <= 0 {
				limit = missionsListDefaultLimit
			}
			if limit > missionsListMaxLimit {
				limit = missionsListMaxLimit
			}
			all, err := store.ListMissions(ctx, limit)
			if err != nil {
				return "", fmt.Errorf("list missions: %w", err)
			}
			items := make([]missionListItem, 0, len(all))
			for _, m := range all {
				items = append(items, toMissionListItem(m))
			}
			out, err := json.Marshal(items)
			if err != nil {
				return "", fmt.Errorf("encode mission list: %w", err)
			}
			return string(out), nil
		},
	}
}

// GetMission is a read-only, permission-exempt tool: pure reads over
// the missions store, no side effects. Answers "is mission X done?"
// and similar status questions from real harness data (never the
// worker/reviewer's own claims — same D-0xx discipline as the harness
// itself). Registered only when store/events are non-nil (missions
// engine wired, see cmd/brain/main.go's WORKSPACES gate).
func GetMission(store missionLister, events missionEventReader) *tools.Tool {
	return &tools.Tool{
		Name: "get_mission",
		Description: `Reads one mission's status: answers "is mission X done?", "what's the status of my last mission?", and similar questions from the harness's own records — never the worker/reviewer's claims.

Arguments (exactly one required):
- id (string): exact mission id.
- query (string): a name/goal substring to find a mission by; must
  match exactly one mission, otherwise you get the list of candidates
  to disambiguate with id. Use list_missions first if unsure.

Returns a snapshot with: id, name, goal, kind, phase, status,
iteration, harness, repo_url, branch, created/updated timestamps, unit
pass counts, pause reason/message if paused, the latest PR url if one
was opened, and the on_complete setting.

Example: {"query": "invoice pdf export"} → that mission's snapshot, or
a disambiguation list if more than one mission matches.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "Exact mission id"},
				"query": {"type": "string", "description": "Name/goal substring to find a mission by"}
			},
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args getMissionArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if args.ID == "" && args.Query == "" {
				return "", fmt.Errorf("one of id or query is required")
			}
			m, err := findMission(ctx, store, args.ID, args.Query)
			if err != nil {
				return "", err
			}
			snap := toMissionSnapshot(ctx, events, m)
			out, err := json.Marshal(snap)
			if err != nil {
				return "", fmt.Errorf("encode mission snapshot: %w", err)
			}
			return string(out), nil
		},
	}
}

// missionCompleter is the narrow slice of *missions.Completer the
// push_mission_branch tool needs — pushing (and optionally opening a PR)
// through the SAME code path the button/auto-fire paths use, so a
// chat-triggered push can never diverge in behavior. Takes the
// mission id rather than a MissionRecord: the adapter (main.go) re-Gets
// the mission from the real store itself, so Completer always acts on
// the authoritative missions.Mission (worktree, plan, etc.) rather
// than a partial copy shuttled through this package's own struct.
type missionCompleter interface {
	PushMissionBranch(ctx context.Context, id, token string) (host string, err error)
	OpenMissionPR(ctx context.Context, id, token string) (url string, number int, err error)
}

// missionTokenResolver resolves a mission's own push token from its
// connector_id — same resolver shape as missions.PushTokenResolver,
// restated here so this file has no import-cycle-inducing dependency
// beyond what missionCompleter already requires.
type missionTokenResolver func(ctx context.Context, connectorID string) (string, error)

type missionPushArgs struct {
	ID     string `json:"id"`
	OpenPR bool   `json:"open_pr"`
}

// PushMissionBranch is permission-GATED — it is deliberately left out
// of Permissions' exempt map (see internal/brain/tools/permissions.go)
// and never appears in any allowlist grant a chat session's model
// could pre-authorize itself into, so every call parks on an explicit
// human approval, matching the "pushes stay human" invariant.
//
// NOTE (slice R): mission worker turns currently see the same base
// tool registry chat sessions do (a known gap slice R will close by
// restricting mission turns to their own tool set) — until then this
// tool is technically reachable from a mission's own turns too, not
// just chat. Do not "fix" that here; it's explicitly out of scope for
// this slice.
func PushMissionBranch(store missionLister, completer missionCompleter, resolveToken missionTokenResolver) *tools.Tool {
	return &tools.Tool{
		Name: "push_mission_branch",
		Description: `Pushes a mission's branch to GitHub, optionally opening a pull request. Requires explicit human approval every time — this tool is never auto-approved.

Only a github-connection coding mission with a completed worktree can
be pushed (same guard the push/PR buttons in the UI use). The approval
prompt shows the mission's goal and target repo so the human sees
exactly what is about to be pushed where.

Arguments:
- id (string, required): the mission id to push.
- open_pr (bool, optional): also open a pull request after pushing
  (default false: push only).

Example: {"id": "3fa1...", "open_pr": true} → pushes the branch, opens
a PR, returns the PR url.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "Mission id to push"},
				"open_pr": {"type": "boolean", "description": "Also open a pull request after pushing (default false)"}
			},
			"required": ["id"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args missionPushArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.ID) == "" {
				return "", fmt.Errorf("id is required")
			}
			m, err := store.GetMission(ctx, args.ID)
			if err != nil {
				return "", fmt.Errorf("no mission found with id %q", args.ID)
			}
			if m.ConnectorID == "" || m.RepoURL == "" {
				return "", fmt.Errorf("mission %s is not a github-connection mission", args.ID)
			}
			if m.NotPushableReason != "" {
				return "", fmt.Errorf("mission %s is not pushable: %s", args.ID, m.NotPushableReason)
			}
			if resolveToken == nil {
				return "", fmt.Errorf("push: connectors are not enabled")
			}
			token, err := resolveToken(ctx, m.ConnectorID)
			if err != nil {
				return "", fmt.Errorf("resolve push token: %w", err)
			}
			if args.OpenPR {
				url, number, err := completer.OpenMissionPR(ctx, args.ID, token)
				if err != nil {
					return "", fmt.Errorf("open pr: %w", err)
				}
				return fmt.Sprintf("pushed %s and opened pull request #%d: %s", m.Branch, number, url), nil
			}
			host, err := completer.PushMissionBranch(ctx, args.ID, token)
			if err != nil {
				return "", fmt.Errorf("push: %w", err)
			}
			return fmt.Sprintf("pushed %s to %s", m.Branch, host), nil
		},
	}
}

// missionTerminalPhases mirrors missions.Phase.Terminal()'s set — kept
// as a local copy (not an import) for the same import-cycle reason
// MissionRecord is its own struct: missions imports this package.
var missionTerminalPhases = map[string]bool{"done": true, "failed": true}

// missionFollowUpCreator is the narrow slice of missions access
// followup_mission needs — *missions.Driver.CreateFollowUp satisfies
// it via cmd/brain/main.go's adapter.
type missionFollowUpCreator interface {
	CreateFollowUpMission(ctx context.Context, parentID, goal string) (string, error)
}

type missionFollowupArgs struct {
	Goal  string `json:"goal"`
	ID    string `json:"id"`
	Query string `json:"query"`
}

// FollowupMission is permission-GATED for the same reason PushMissionBranch
// is — deliberately left out of Permissions' exempt map (see
// internal/brain/tools/permissions.go) so spawning a new mission
// against a finished one always parks on an explicit human approval.
func FollowupMission(store missionLister, creator missionFollowUpCreator) *tools.Tool {
	return &tools.Tool{
		Name: "followup_mission",
		Description: `Spawns a NEW mission continuing a finished one. Requires explicit human approval every time — this tool is never auto-approved.

The parent mission must already be finished (phase done or failed). The
new mission carries the parent's outcome summary into its own explore/
plan/work prompts and inherits the parent's agent, routes, kind, and
repo settings; for a coding mission, its worktree is based on the
parent's own branch when reachable. It never inherits the parent's
push-on-complete choice or destinations — those are per-mission human
decisions, made fresh for the follow-up.

Arguments:
- goal (string, required): the follow-up mission's own goal.
- id (string): exact parent mission id.
- query (string): a name/goal substring to find the parent mission by;
  must match exactly one mission, otherwise you get the list of
  candidates to disambiguate with id.

Exactly one of id/query is required.

Example: {"id": "3fa1...", "goal": "now add tests for the new endpoint"}
→ creates a follow-up mission of 3fa1..., returns its id.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"goal": {"type": "string", "description": "The follow-up mission's own goal"},
				"id": {"type": "string", "description": "Exact parent mission id"},
				"query": {"type": "string", "description": "Name/goal substring to find the parent mission by"}
			},
			"required": ["goal"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args missionFollowupArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.Goal) == "" {
				return "", fmt.Errorf("goal is required")
			}
			if args.ID == "" && args.Query == "" {
				return "", fmt.Errorf("one of id or query is required")
			}
			parent, err := findMission(ctx, store, args.ID, args.Query)
			if err != nil {
				return "", err
			}
			if !missionTerminalPhases[parent.Phase] {
				return "", fmt.Errorf("mission %s is not finished (phase %s); follow-ups need a terminal parent", parent.ID, parent.Phase)
			}
			childID, err := creator.CreateFollowUpMission(ctx, parent.ID, args.Goal)
			if err != nil {
				return "", fmt.Errorf("create follow-up mission: %w", err)
			}
			return fmt.Sprintf("created follow-up mission %s of %s: %s", childID, parent.ID, args.Goal), nil
		},
	}
}
