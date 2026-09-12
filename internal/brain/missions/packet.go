package missions

import (
	"fmt"
	"path/filepath"
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
// turns); durability lives here (plan, progress log, git log), not in
// conversation history.
type WorkPacket struct {
	Goal      string
	Kind      string
	Plan      Plan
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
	// for a follow-up mission (Mission.ParentContext()) -- gives the
	// worker the prior mission's result without reopening it.
	ParentContext string
	// ReferencedContext is the picked composer #-mention references
	// (Mission.ReferencedContext()), additive to ParentContext: gives
	// the worker the content of what the user explicitly pinned at
	// create time.
	ReferencedContext string
	// References are the same picks as entries (Mission.ReferenceEntries());
	// RenderForDelegated writes each to a file in the run dir and lists
	// the paths instead of inlining the digests (issue #705).
	References []SourceEntry
	// Attachments are the mission's create-time documents, images, and
	// audio clips ("pdf" Sources entries, issue #359) -- reach every
	// worker turn via Render, including a delegated executor's turn
	// (executor packets also go through Render).
	Attachments []SourceEntry
	// SkillsIndex is the rendered skill index for the mission's agent
	// (skills.Index over the agent's allowlist), resolved at packet
	// build time like the scheduler's other agent defaults — an agent
	// edited mid-mission applies on the next turn. Empty when the
	// mission has no agent, the agent lists no skills, or the driver's
	// resolver is unwired. Native workers only: a delegated CLI has no
	// load_skill tool, so RenderForDelegated never includes it.
	SkillsIndex string
	// Light marks a mission that runs build planless (D-069's
	// original light behavior, plus flow=discover_build, D-090,
	// issue #459): Render uses lightSystemPreamble instead of
	// nativeSystemPreamble, and Plan is always empty so the Plan block
	// never renders.
	Light bool
	// DiscoverNotes carries the discover phase's findings into a planless
	// worker turn (D-090): only ever set for flow=discover_build,
	// which runs discover before its planless build pass; a D-069
	// light mission never visits discover, so this stays empty for it.
	// Rendered the same "Discovery findings:" way PlanSession's own
	// prompt renders it (runner.go), kept cache-stable since notes are
	// static once discover completes.
	DiscoverNotes string
	// Location is the operator's configured timezone, used to render
	// progress-note timestamps; nil renders in UTC.
	Location *time.Location
	// Findings is the mission's findings ledger (D-092): render lists
	// the open ones as this turn's work, with ReworkRound of MaxRounds
	// as the cycle position. Shared by the native and delegated paths.
	Findings    []Finding
	ReworkRound int
	MaxRounds   int
	// WritingStyle is the operator's configured writing rules
	// (settings writing_style), rendered after PromptOverlay.
	WritingStyle string
	// WritingSamples marks that a writing-samples kb collection is
	// configured, adding WritingSamplesNote to the same block.
	WritingSamples bool
}

// WritingStyleHeading and WritingSamplesNote are the operator
// writing-style block's shared text. Chat (internal/brain/chat) renders
// the same block, so both constants and WritingStyleBlock live here,
// the package chat already imports.
const (
	WritingStyleHeading = "# Owner writing style"
	WritingSamplesNote  = "The owner's own writing is in the knowledge base. Before drafting or rewriting prose, call writing_samples with the language of the piece and match the voice of what comes back."
)

// WritingStyleBlock renders the operator writing-style block, "" when
// there is neither style text nor a samples collection.
func WritingStyleBlock(style string, samples bool) string {
	if style == "" && !samples {
		return ""
	}
	b := WritingStyleHeading + "\n\n"
	if style != "" {
		b += style
		if samples {
			b += "\n\n"
		}
	}
	if samples {
		b += WritingSamplesNote
	}
	return b
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
const nativeSystemPreamble = "You are executing one unit of a plan. Work toward the goal, then end your turn with exactly one mission_status tool call: done (with evidence), retry (with analysis), or blocked (with a question). Create or update files ONLY with the write_file tool using workspace-relative paths — never shell redirects (>, >>) or heredocs, which classify as writes requiring interactive approval and will stall you; artifact tracking depends on write_file being the only way files get created. Use shell for reading and checking, not writing. The harness commits the unit's files itself after your turn, so never run git add, commit, reset, stash, or checkout. The harness verifies your declared artifacts exist on disk; describing a file is not producing it. The goal's explicit constraints outrank the plan: if a plan unit requires violating something the goal explicitly forbids, do not do it, report the conflict via mission_status with outcome blocked instead. When you end with retry or blocked, include a handoff note summarizing state, remaining work, and gotchas — the next session starts fresh and sees only your handoff, the plan, and the git log." + toolDisciplineNote

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
//
// The body order differs from Render (issue #705): a one-shot CLI reads
// the prompt top to bottom and weighs the tail, so lineage and
// references are one line each pointing at files under runDir/refs
// (returned in files, keyed relative to runDir), and the current unit
// with its artifacts and verify command comes last. Codex once read a
// parent digest saying "done, docs only" as the final word and reported
// DONE without a single tool call.
func (p WorkPacket) RenderForDelegated(runDir string) (system, user string, files map[string]string) {
	system = p.systemPrompt("")
	files = map[string]string{}

	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n", NeutralizeSlot(p.Goal))
	fmt.Fprintf(&b, "Iteration: %d\n\n", p.Iteration)

	if p.DiscoverNotes != "" {
		b.WriteString("Discovery findings:\n")
		b.WriteString(NeutralizeSlot(p.DiscoverNotes))
		b.WriteString("\n\n")
	}

	if p.ParentContext != "" {
		files["refs/parent-mission.md"] = p.ParentContext
		fmt.Fprintf(&b, "Follow-up of a previous mission; its outcome digest is at %s (background only, this mission's plan below is the work).\n\n", filepath.Join(runDir, "refs", "parent-mission.md"))
	}
	if len(p.References) > 0 {
		b.WriteString("Referenced documents, read when the unit needs them:\n")
		for i, e := range p.References {
			rel := filepath.Join("refs", fmt.Sprintf("%02d-%s.md", i+1, slugify(referenceName(e))))
			files[rel] = e.Digest
			fmt.Fprintf(&b, "- %s: %s\n", NeutralizeSlot(referenceName(e)), filepath.Join(runDir, rel))
		}
		b.WriteString("\n")
	}

	if len(p.Plan.Units) > 0 {
		b.WriteString("Plan:\n")
		for _, u := range p.Plan.Units {
			b.WriteString(renderPlanLine(u))
		}
		b.WriteString("\n")
	}

	b.WriteString(renderOpenFindings(p.Findings, p.ReworkRound, p.MaxRounds))
	b.WriteString(p.renderProgress())

	if p.GitLog != "" {
		b.WriteString("Recent commits in this worktree:\n")
		b.WriteString(NeutralizeSlot(p.GitLog))
		b.WriteString("\n")
	}

	b.WriteString(renderAttachments(p.Attachments))

	if len(p.Plan.Units) > 0 {
		if unit, _ := currentUnit(p.Plan); unit != nil {
			b.WriteString("\n")
			fmt.Fprintf(&b, "Current unit: %s\n", NeutralizeSlot(unit.Title))
			b.WriteString(renderUnitFailure(*unit))
			for _, c := range unit.Criteria {
				fmt.Fprintf(&b, "  criterion: %s\n", NeutralizeSlot(c))
			}
			for _, a := range unit.Artifacts {
				fmt.Fprintf(&b, "  must produce (exact path): %s\n", NeutralizeSlot(a))
			}
			if unit.VerifyCmd != "" {
				fmt.Fprintf(&b, "  verified by: %s\n", NeutralizeSlot(unit.VerifyCmd))
			}
			b.WriteString("Do this unit now.\n")
		}
	}

	return system, b.String(), files
}

// slugify turns a reference name into a short filename stem.
func slugify(name string) string {
	var b strings.Builder
	last := '-'
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			last = r
		default:
			if last != '-' {
				b.WriteRune('-')
				last = '-'
			}
		}
		if b.Len() >= 40 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "reference"
	}
	return s
}

// systemPrompt assembles the system half shared by Render and
// RenderForDelegated.
func (p WorkPacket) systemPrompt(preamble string) string {
	system := preamble + p.ExecEnvironmentNote
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
	// Operator config like PromptOverlay: never neutralized.
	if block := WritingStyleBlock(p.WritingStyle, p.WritingSamples); block != "" {
		system += "\n\n" + block
	}
	return system
}

// renderProgress is the "Progress so far" block, capped at
// progressRenderCap notes.
func (p WorkPacket) renderProgress() string {
	if len(p.Progress) == 0 {
		return ""
	}
	loc := p.Location
	if loc == nil {
		loc = time.UTC
	}
	var b strings.Builder
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
	return b.String()
}

func (p WorkPacket) render(preamble string) (system, user string) {
	system = p.systemPrompt(preamble)

	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n", NeutralizeSlot(p.Goal))
	fmt.Fprintf(&b, "Iteration: %d\n\n", p.Iteration)

	if p.DiscoverNotes != "" {
		b.WriteString("Discovery findings:\n")
		b.WriteString(NeutralizeSlot(p.DiscoverNotes))
		b.WriteString("\n\n")
	}

	if len(p.Plan.Units) > 0 {
		if unit, _ := currentUnit(p.Plan); unit != nil {
			fmt.Fprintf(&b, "Current unit: %s\n", NeutralizeSlot(unit.Title))
			b.WriteString(renderUnitFailure(*unit))
		}
		b.WriteString("Plan:\n")
		for _, u := range p.Plan.Units {
			b.WriteString(renderPlanLine(u))
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

	b.WriteString(renderOpenFindings(p.Findings, p.ReworkRound, p.MaxRounds))
	b.WriteString(p.renderProgress())

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

// unitStatus is the plan marker a worker or reviewer prompt shows for a
// unit (D-099, issue #533): reviewed (a review approved it),
// harness-verified (harness evidence, awaiting approval), or pending.
// A regressed unit is pending; renderPlanLine adds the regressed note.
func unitStatus(u PlanUnit) string {
	switch {
	case u.Passes:
		return "reviewed"
	case u.HarnessPassed:
		return "harness-verified"
	default:
		return "pending"
	}
}

// renderPlanLine is the plan list entry both the worker and reviewer
// packets show for a unit: the marker, the title, and for a regressed
// unit the note that it passed before.
func renderPlanLine(u PlanUnit) string {
	line := fmt.Sprintf("- [%s] %s", unitStatus(u), NeutralizeSlot(u.Title))
	if u.Regressed && !u.verified() {
		line += fmt.Sprintf(" (regressed: passed before, now fails its %s check)", u.VerifyCheck)
	}
	return line + "\n"
}

// renderUnitFailure is the current-unit block's harness evidence
// (D-094): the last failing check and its output excerpt, named as a
// regression when the unit had passed before. Empty for a unit the
// harness has not failed yet. The excerpt is command output, so it
// passes NeutralizeSlot.
func renderUnitFailure(u PlanUnit) string {
	if u.verified() || u.VerifyCheck == "" {
		return ""
	}
	var b strings.Builder
	if u.Regressed {
		fmt.Fprintf(&b, "REGRESSED: this unit passed before and now fails its %s check. Fix it before anything else.\n", u.VerifyCheck)
	} else {
		fmt.Fprintf(&b, "Last harness %s check failed for this unit.\n", u.VerifyCheck)
	}
	if u.VerifyExcerpt != "" {
		fmt.Fprintf(&b, "Harness output:\n%s\n", NeutralizeSlot(strings.TrimRight(u.VerifyExcerpt, "\n")))
	}
	return b.String()
}

// renderOpenFindings is the rework turn's work order (D-092): the open
// findings by id, with the cycle position, and the instruction to fix
// rather than re-verify. Empty when nothing is open. Title, file and
// detail are reviewer (model) output, so each passes NeutralizeSlot.
func renderOpenFindings(findings []Finding, round, maxRounds int) string {
	open := OpenFindings(findings)
	if len(open) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Current work: close open review findings (round %d of %d)\n", round, maxRounds)
	b.WriteString("Open review findings, all must be closed this turn:\n")
	for _, f := range open {
		severity := f.Severity
		if severity == "" {
			severity = SeverityBlocking
		}
		fmt.Fprintf(&b, "- %s [%s]", f.ID, severity)
		if f.File != "" {
			fmt.Fprintf(&b, " %s:", NeutralizeSlot(f.File))
		}
		fmt.Fprintf(&b, " %s.", NeutralizeSlot(strings.TrimSuffix(strings.TrimSpace(f.Title), ".")))
		if f.Detail != "" {
			fmt.Fprintf(&b, " %s", NeutralizeSlot(f.Detail))
		}
		b.WriteString("\n")
	}
	b.WriteString("Do not re-verify the whole project. Change code, run the affected unit's verify_cmd, commit, and report per finding what changed.\n\n")
	return b.String()
}

// attachmentLabel names an attachment's rendered section by mime
// (issue #359): an image renders as its caption, an audio clip as its
// transcript, everything else (pdf/text) as a document.
func attachmentLabel(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "Attached image %s (description):\n%s\n"
	case strings.HasPrefix(mime, "audio/"):
		return "Attached audio %s (transcript):\n%s\n"
	default:
		return "Attached document %s:\n%s\n"
	}
}

// renderAttachments formats each attachment with markdown into a
// section labeled by mime (attachmentLabel), neutralized like every
// other model-reachable field, shared by WorkPacket.Render and the
// discover/plan runner sessions (runner.go) so the three near-identical
// loops stay in sync. An attachment with no markdown (a conversion
// that somehow never ran) renders nothing.
func renderAttachments(atts []SourceEntry) string {
	var b strings.Builder
	for _, a := range atts {
		if a.Markdown == "" {
			continue
		}
		name := a.Name
		if name == "" {
			name = a.ID
		}
		fmt.Fprintf(&b, "\n"+attachmentLabel(a.Mime), NeutralizeSlot(name), NeutralizeSlot(a.Markdown))
	}
	return b.String()
}
