package destinations

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// Payload is the rendered delivery content, kind-agnostic: adapters
// format it per their own contract (email HTML, webhook JSON/text,
// telegram MarkdownV2). Files/OversizeFiles are never serialized —
// webhook's JSON body is body+links only, per the plan's kind gate;
// they carry through only for the email/telegram adapters, which read
// the fields directly rather than through JSON.
type Payload struct {
	MissionID string `json:"mission_id"`
	Name      string `json:"name"`
	Goal      string `json:"goal"`
	// Subject is set only by the ad-hoc deliver tool (missions never
	// set it); the email adapter uses it as the subject line in place
	// of its mission-derived default when non-empty.
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body"`
	// CompletedAt is the mission's terminal-transition time, UTC. Zero
	// for the ad-hoc deliver tool (no mission behind it) — adapters
	// render nothing when zero, never a guessed "now".
	CompletedAt time.Time `json:"completed_at,omitempty"`
	Links       []string  `json:"links"`
	Files       []File    `json:"-"`
	// TextArtifacts are .md/.txt declared artifacts, rendered inline as
	// delivery content (Telegram MarkdownV2, email HTML) instead of
	// attached — see resolveArtifactFiles.
	TextArtifacts []TextArtifact `json:"-"`
	OversizeFiles []string       `json:"-"`
}

// Render builds a mission's delivery Payload: body is a short
// completion line, not the outcome digest — recipients want the
// mission's generated output, not the goal/plan/review process digest.
// missions.OutcomeDigest keeps serving memory extraction and follow-up
// parent_context, both untouched by this. CompletedAt is the mission's
// terminal-transition timestamp (UpdatedAt), UTC — never a guessed
// local timezone, since destinations carry no per-mission timezone
// setting. links is the mission detail URL (built from webBaseURL when
// non-empty) plus branch/PR URL when the mission has them.
// Files/TextArtifacts/OversizeFiles come from the mission's declared
// plan-unit artifacts, read from its workspace (resolveArtifactFiles) —
// exactly what CheckArtifacts already verified exists.
func Render(m missions.Mission, webBaseURL string, events []missions.Event) Payload {
	name := m.Name
	if name == "" {
		name = m.Goal
	}
	var links []string
	if webBaseURL != "" {
		links = append(links, strings.TrimRight(webBaseURL, "/")+"/missions/"+m.ID)
	}
	if m.Branch != "" {
		links = append(links, "branch: "+m.Branch)
	}
	if url, ok := lastPROpenedURL(events); ok {
		links = append(links, url)
	}
	files, texts, oversize := resolveArtifactFiles(m)
	return Payload{
		MissionID:     m.ID,
		Name:          name,
		Goal:          m.Goal,
		Body:          "Mission complete: " + name,
		CompletedAt:   m.UpdatedAt.UTC(),
		Links:         links,
		Files:         files,
		TextArtifacts: texts,
		OversizeFiles: oversize,
	}
}

// lastPROpenedURL scans events for the most recent mission.pr_opened
// event's url — missions has no dedicated PR-URL column, only the
// event Completer.OpenPR appends.
func lastPROpenedURL(events []missions.Event) (string, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != "mission.pr_opened" {
			continue
		}
		var payload struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(events[i].Payload, &payload); err != nil || payload.URL == "" {
			return "", false
		}
		return payload.URL, true
	}
	return "", false
}
