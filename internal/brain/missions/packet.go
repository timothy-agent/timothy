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
	// PromptOverlay is the creating agent's overlay text, snapshotted
	// onto the mission at create time — appended to the worker's
	// system prompt, same instructions a chat session with that agent
	// would get.
	PromptOverlay string
	// ExecEnvironmentNote describes what shell/verify_cmd commands
	// actually run against (sandbox container vs the minimal in-process
	// shell) — without this a worker has no way to know whether e.g.
	// python3 exists, and can report "done" on a step whose own
	// verify_cmd will fail for want of a runtime that was never there.
	ExecEnvironmentNote string
}

// Render turns the packet into the system/user message a worker
// session's first turn receives. Progress notes and git log content
// can contain prior model-produced text (a worker's own commit
// messages, an earlier note); both pass through NeutralizeSlot before
// insertion — self-injection hardening.
func (p WorkPacket) Render() (system, user string) {
	system = "You are executing one unit of a plan. Work toward the goal, then end your turn with exactly one mission_status tool call: done (with evidence), retry (with analysis), or blocked (with a question). Create or update files ONLY with the write_file tool using workspace-relative paths — never shell redirects (>, >>) or heredocs, which classify as writes requiring interactive approval and will stall you; artifact tracking depends on write_file being the only way files get created. Use shell for reading and checking, not writing. The harness verifies your declared artifacts exist on disk; describing a file is not producing it." + p.ExecEnvironmentNote
	if p.PromptOverlay != "" {
		// Operator-authored config, not model output — unlike Progress/
		// GitLog below, this never passes through NeutralizeSlot.
		system += "\n\n" + p.PromptOverlay
	}

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
			// The EXACT artifact paths the harness will check — a worker
			// that invents its own filename (http-429 vs http429) fails
			// verification without ever seeing why.
			for _, a := range u.Artifacts {
				fmt.Fprintf(&b, "  must produce (exact path): %s\n", NeutralizeSlot(a))
			}
			if u.VerifyCmd != "" {
				fmt.Fprintf(&b, "  verified by: %s\n", NeutralizeSlot(u.VerifyCmd))
			}
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
