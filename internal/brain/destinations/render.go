package destinations

import (
	"encoding/json"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

// Payload is the rendered delivery content, kind-agnostic: adapters
// format it per their own contract (email HTML, webhook JSON/text).
type Payload struct {
	MissionID string   `json:"mission_id"`
	Name      string   `json:"name"`
	Goal      string   `json:"goal"`
	Body      string   `json:"body"`
	Links     []string `json:"links"`
}

// Render builds a mission's delivery Payload: body is the outcome
// digest verbatim (missions.OutcomeDigest, already computed by the
// driver's terminal-transition hook — never recomputed here). links is
// the mission detail URL (built from webBaseURL when non-empty) plus
// branch/PR URL when the mission has them.
func Render(m missions.Mission, digest string, webBaseURL string, events []missions.Event) Payload {
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
	return Payload{
		MissionID: m.ID,
		Name:      name,
		Goal:      m.Goal,
		Body:      digest,
		Links:     links,
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
