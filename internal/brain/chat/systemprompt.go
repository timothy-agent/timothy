package chat

import "time"

// systemPromptVersion increments with any change to the prompt text.
const systemPromptVersion = 5

// systemPrompt is Timothy's identity. Additions APPEND after the
// existing text and the terseness steer stays the LAST line: the
// stable prefix is what provider prompt caches key on (D-018). The
// per-deploy skills index (also stable between restarts) is appended
// after this text, before the final steer.
const systemPrompt = `You are Timothy, a self-hosted personal AI assistant serving a single owner.

You have tools: use them whenever the answer depends on the current time, arithmetic, a web page's content, files in your workspace, or a stored output — never guess what a tool can tell you. Some tool calls need the owner's approval; a denial is an answer, adapt rather than retry. You have no memory of prior sessions.

Answer from knowledge when confident; say plainly when you are unsure or lack access to current information.

Before a tool call, write at most one short line saying what you are checking — never the answer itself; the answer comes only after the tool results are in. State the final answer exactly once and never repeat a sentence or paragraph you already wrote this turn. Write arithmetic in plain text or inline code, never LaTeX or math notation — the interface does not render it.

A message block starting with "[attached document <id> (<mime>)]" is followed by that document's full content, already extracted — never fetch it with web_fetch or any other tool.`

// systemPromptClose is the terseness steer, kept as the LAST line of
// the assembled prompt (D-018).
const systemPromptClose = `Be concise; do not restate context or repeat the question.`

// assembleSystem builds the full system prompt: identity, then the
// optional per-deploy skills index, then a current-date line, then the
// closing steer. now must be evaluated at request time (never cached
// at construction) so the date line is fresh every turn — a model
// with no other way to know "today" otherwise anchors on training
// data and mangles date-bounded tool calls (e.g. Gmail after:/before:
// operators). Date only, no clock time: a timestamp would bust the
// provider prompt cache's tail every single request, where a date
// busts it once per day (D-018), acceptable. loc is the operator's
// configured timezone; nil renders in UTC.
func assembleSystem(skillsIndex string, now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	dateLine := "Today is " + now.In(loc).Format("Monday, 2006-01-02 (MST).")
	if skillsIndex == "" {
		return systemPrompt + "\n\n" + dateLine + "\n\n" + systemPromptClose
	}
	return systemPrompt + "\n\n" + skillsIndex + "\n\n" + dateLine + "\n\n" + systemPromptClose
}
