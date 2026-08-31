package missions

import (
	"fmt"
	"strings"
	"time"
)

// gitLogCap bounds how much committed history a fresh worker sees —
// enough to orient on recent progress, not the whole project history.
const gitLogCap = 4 << 10

// progressRenderCap bounds how many progress notes a rendered packet
// shows — on a long-running mission the durable log (missions.Progress,
// append-only in store.go) keeps growing, and rendering all of it into
// every fresh worker's first turn would balloon packet tokens over time.
// The durable log itself is untouched; this only bounds what's shown.
const progressRenderCap = 10

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
	// ParentContext is the parent mission's outcome digest, set only
	// for a follow-up mission (missions.Mission.ParentContext) — gives
	// the worker the prior mission's result without reopening it.
	ParentContext string
	// ReferencedContext is the picked composer #-mention references
	// (missions.Mission.ReferencedContext), additive to ParentContext:
	// gives the worker the content of what the user explicitly pinned
	// at create time.
	ReferencedContext string
	// Attachments are the mission's create-time PDF documents — reach
	// every worker turn via Render, including a delegated executor's
	// turn (executor packets also go through Render).
	Attachments []MissionAttachment
	// SkillsIndex is the rendered skill index for the mission's agent
	// (skills.Index over the agent's allowlist), resolved at packet
	// build time like the scheduler's other agent defaults — an agent
	// edited mid-mission applies on the next turn. Empty when the
	// mission has no agent, the agent lists no skills, or the driver's
	// resolver is unwired. Native workers only: a delegated CLI has no
	// load_skill tool, so RenderForDelegated never includes it.
	SkillsIndex string
	// Light marks a mission that skips discover/plan/prove (D-069):
	// Render uses lightSystemPreamble instead of nativeSystemPreamble,
	// and Spec is always empty so the Plan block never renders.
	Light bool
	// Location is the operator's configured timezone, used to render
	// progress-note timestamps; nil renders in UTC.
	Location *time.Location
}

// toolDisciplineNote is the tool-loop stop-rule contract shared by the
// discover prompt (runner.go) and both worker preambles: without it,
// models repeat failed tool calls verbatim and burn iterations
// (observed on glm-5.3 and the nova family), and fill gaps with
// plausible guesses instead of naming what's missing.
const toolDisciplineNote = " Tool discipline: before each tool call, know what you still need; after each result, judge whether it answered that. Never repeat a failed call unchanged — vary the approach or move on. If information cannot be found after a few attempts, continue with what you have and state what is missing rather than guessing."

// nativeSystemPreamble is the mission_status/write_file contract a
// native (in-process loop.Agent) worker turn must follow — meaningless
// to a delegated CLI, which has neither tool (RenderForDelegated uses
// delegatedSystemPreamble instead).
const nativeSystemPreamble = "You are executing one unit of a plan. Work toward the goal, then end your turn with exactly one mission_status tool call: done (with evidence), retry (with analysis), or blocked (with a question). Create or update files ONLY with the write_file tool using workspace-relative paths — never shell redirects (>, >>) or heredocs, which classify as writes requiring interactive approval and will stall you; artifact tracking depends on write_file being the only way files get created. Use shell for reading and checking, not writing. The harness verifies your declared artifacts exist on disk; describing a file is not producing it. The goal's explicit constraints outrank the plan: if a plan unit requires violating something the goal explicitly forbids, do not do it, report the conflict via mission_status with outcome blocked instead. When you end with retry or blocked, include a handoff note summarizing state, remaining work, and gotchas — the next session starts fresh and sees only your handoff, the plan, and the git log." + toolDisciplineNote

// lightSystemPreamble is nativeSystemPreamble's counterpart for a light
// mission (D-069): single pass, no plan, no artifact check — the
// worker's final message is delivered to the user verbatim.
const lightSystemPreamble = "You are completing this goal in a single pass. Work toward the goal, then end your turn with exactly one mission_status tool call: done (with evidence), retry (with analysis), or blocked (with a question). On done, put the COMPLETE final deliverable text in the mission_status call's final_output argument — it is delivered to the user verbatim as the result, so it must be the deliverable itself, never a summary of work done. Create or update files ONLY with the write_file tool using workspace-relative paths — never shell redirects (>, >>) or heredocs, which classify as writes requiring interactive approval and will stall you. Use shell for reading and checking, not writing. When you end with retry or blocked, include a handoff note summarizing state, remaining work, and gotchas — the next session starts fresh and sees only your handoff and the git log." + toolDisciplineNote

// Render turns the packet into the system/user message a native
// worker session's first turn receives. Progress notes and git log
// content can contain prior model-produced text (a worker's own
// commit messages, an earlier note); both pass through NeutralizeSlot
// before insertion — self-injection hardening.
func (p WorkPacket) Render() (system, user string) {
	if p.Light {
		return p.render(lightSystemPreamble)
	}
	return p.render(nativeSystemPreamble)
}

// RenderForDelegated is Render's delegated-executor counterpart: same
// goal/plan/progress/git-log/attachments body, but without the
// mission_status/write_file preamble a delegated CLI (codex, claude
// code) has no way to honor — sending both that preamble AND
// delegatedSystemAppend's correction in the same prompt was observed
// to confuse a model into reporting BLOCKED over tools it was never
// offered, rather than using its own native file-edit/patch tool and
// the harness's structured-output contract.
func (p WorkPacket) RenderForDelegated() (system, user string) {
	return p.render("")
}

func (p WorkPacket) render(preamble string) (system, user string) {
	system = preamble + p.ExecEnvironmentNote
	if preamble != "" && p.SkillsIndex != "" {
		// preamble=="" is the delegated path (RenderForDelegated) —
		// no load_skill tool there, so the index would only mislead.
		system += "\n\n" + p.SkillsIndex
	}
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
		loc := p.Location
		if loc == nil {
			loc = time.UTC
		}
		b.WriteString("Progress so far:\n")
		notes := p.Progress
		if len(notes) > progressRenderCap {
			fmt.Fprintf(&b, "(%d earlier notes omitted)\n", len(notes)-progressRenderCap)
			notes = notes[len(notes)-progressRenderCap:]
		}
		for _, n := range notes {
			fmt.Fprintf(&b, "- %s: %s\n", n.At.In(loc).Format("2006-01-02 15:04 MST"), NeutralizeSlot(n.Note))
		}
		b.WriteString("\n")
	}

	if p.GitLog != "" {
		b.WriteString("Recent commits in this worktree:\n")
		b.WriteString(NeutralizeSlot(p.GitLog))
		b.WriteString("\n")
	}

	if p.ParentContext != "" {
		b.WriteString("Previous mission outcome:\n")
		b.WriteString(NeutralizeSlot(p.ParentContext))
		b.WriteString("\n")
	}

	if p.ReferencedContext != "" {
		b.WriteString("Referenced context:\n")
		b.WriteString(NeutralizeSlot(p.ReferencedContext))
		b.WriteString("\n")
	}

	b.WriteString(renderAttachments(p.Attachments))

	return system, b.String()
}

// renderAttachments formats each attachment with markdown into a
// "Attached document <name>:" section, neutralized like every other
// model-reachable field — shared by WorkPacket.Render and the
// discover/plan runner sessions (runner.go) so the three near-identical
// loops stay in sync. An attachment with no markdown (a conversion
// that somehow never ran) renders nothing.
func renderAttachments(atts []MissionAttachment) string {
	var b strings.Builder
	for _, a := range atts {
		if a.Markdown == "" {
			continue
		}
		name := a.Name
		if name == "" {
			name = a.ID
		}
		fmt.Fprintf(&b, "\nAttached document %s:\n%s\n", NeutralizeSlot(name), NeutralizeSlot(a.Markdown))
	}
	return b.String()
}
