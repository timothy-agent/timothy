---
name: research-brief
description: Source-grounded research with verification and citations. Use when a question needs facts you must look up: current events, prices, specifications, laws, schedules, or any claim the user will act on.
---

# Research brief discipline

## Rules

- Retrieve before you write: every load-bearing claim needs a source you actually fetched this turn, not trained recall.
- If a kb_search tool is available, search the knowledge base before the web and cite kb sources by their kb:// ref.
- Two independent sources for any claim that drives a decision; one source for background color.
- Record where each fact came from; a source mentioned only in the trailing list without ever being referenced in the body is not a citation, it is decoration.
- Distinguish plainly between what a source states and what you infer from it; label inference as inference.
- Prefer primary sources (the vendor's page, the law's text, the paper) over aggregators repeating them.
- Note the date on every time-sensitive fact; an undated price or version number is a trap for the reader.
- When sources conflict, report the conflict; do not silently pick one.
- If retrieval fails or the fact cannot be found, say so; a stated gap beats a confident guess.
- End with the answer to the actual question asked, not a survey of everything found.
- Close every answer with a `## Sources` section: a numbered markdown
  list, one fetched source per line, formatted exactly as
  `1. [Title](URL)`. Nothing else on that line: no dates, no notes,
  no extra prose. Every URL listed must be one you actually fetched or
  searched this turn.
- Write arithmetic in plain text or inline code, never LaTeX or math
  notation (`\frac`, `\times`, `\text`, `$...$`); the interface does
  not render it.
- Before a tool call, write at most one short line saying what you are
  checking, never the answer itself. The answer comes only after all
  tool results are in.
- State the final answer exactly once. Never repeat a sentence or
  paragraph you already wrote this turn, and do not add an `## Answer`
  heading.
- When comparing a number against a limit or allowance, write the
  comparison out explicitly (`184,320 < 400,000: under the limit`)
  before concluding. When pricing usage that has a free allowance,
  subtract the allowance first and price only the remainder.

## Anti-rationalization

| Excuse                                              | Rebuttal                                                             |
|-----------------------------------------------------|----------------------------------------------------------------------|
| "I already know this"                               | Knowledge has a training cutoff; the question is being asked now.    |
| "One good source is enough"                         | Single sources are wrong often enough that decisions deserve two.    |
| "Citing everything clutters the answer"             | An uncited actionable claim is a liability, not a courtesy.          |
| "The aggregator summarizes the primary source fine" | Aggregators introduce errors and lag; the primary is one fetch away. |

## Red flags: stop and re-check

- You are writing a number, date, or name you did not see in a fetched source this turn.
- Every citation points at the same domain.
- The answer contradicts something a source said and you haven't explained why.
- You are about to answer without having made a single retrieval call.

## Evidence required

- Each key claim traceable to a fetched source (URL and access date).
- Conflicts between sources surfaced explicitly.
- "I'm confident" is not evidence; a quote from the source is.
