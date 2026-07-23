package missions

import (
	"fmt"
	"strings"
)

// gitLogCap bounds how much committed history a fresh worker sees —
// enough to orient on recent progress, not the whole project history.
const gitLogCap = 4 << 10

// WorkPacket is everything a FRESH worker session is seeded with —
// workers never inherit prior transcripts (statelessness between
// turns); durability lives here (spec, progress log, git log), not in
// conversation history.
type WorkPacket struct {
	Goal      string
	Kind      string
	Spec      Spec
	Progress  []ProgressNote
	GitLog    string
	Iteration int
}

// Render turns the packet into the system/user message a worker
// session's first turn receives. Progress notes and git log content
// can contain prior model-produced text (a worker's own commit
// messages, an earlier note); both pass through NeutralizeSlot before
// insertion — self-injection hardening.
func (p WorkPacket) Render() (system, user string) {
	system = "You are executing one unit of a plan. Work toward the goal, then end your turn with exactly one mission_status tool call: done (with evidence), retry (with analysis), or blocked (with a question)."

	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n", NeutralizeSlot(p.Goal))
	fmt.Fprintf(&b, "Iteration: %d\n\n", p.Iteration)

	if len(p.Spec.Units) > 0 {
		b.WriteString("Plan:\n")
		for _, u := range p.Spec.Units {
			status := "pending"
			if u.Passes {
				status = "verified"
			}
			fmt.Fprintf(&b, "- [%s] %s\n", status, NeutralizeSlot(u.Title))
		}
		b.WriteString("\n")
	}

	if len(p.Progress) > 0 {
		b.WriteString("Progress so far:\n")
		for _, n := range p.Progress {
			fmt.Fprintf(&b, "- %s: %s\n", n.At.Format("2006-01-02 15:04"), NeutralizeSlot(n.Note))
		}
		b.WriteString("\n")
	}

	if p.GitLog != "" {
		b.WriteString("Recent commits in this worktree:\n")
		b.WriteString(NeutralizeSlot(p.GitLog))
		b.WriteString("\n")
	}

	return system, b.String()
}
