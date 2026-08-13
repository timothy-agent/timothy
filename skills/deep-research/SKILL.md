---
name: deep-research
description: Decomposes a complex research question into independent sub-questions, researches each in its own unit, then synthesizes a cited report. Use when a question needs multiple independent angles of investigation. Never use when a single search answers the question.
---

# Deep research discipline

## Rules

- Decompose the goal into 2-4 independent sub-questions during explore. A
  sub-question is independent only if it can be researched without knowing
  the answer to another one; questions that depend on each other's results
  stay together in one unit, they do not get split for the sake of
  splitting.
- Scout each sub-question with 1-2 web_search calls before locking the plan:
  confirm it is actually answerable and roughly how much material exists.
  This is reconnaissance, not the research itself.
- If a kb_search tool is available, search the knowledge base before the
  web and cite kb sources by their kb:// ref.
- One plan unit per sub-question. Each unit declares exactly one artifact,
  `findings-<slug>.md`, created with write_file. Each findings file has:
  the answer to that sub-question, key facts with an inline citation and
  date on each, and a closing `## Sources` section listing only URLs
  fetched or seen in that unit.
- Close every findings file with `## Sources`: numbered list, one line
  each, `1. [Title](URL)`. No dates or notes on that line. A source in the
  list that the body never cites is not a citation.
- Two independent sources for any fact the report will treat as a
  decision-driving claim; one source is enough for background color.
  Prefer the primary source (the vendor, the filing, the paper) over an
  aggregator repeating it.
- Label inference explicitly: "Inference: ..." for anything reasoned from
  sources rather than stated by one. Never blur the line between the two.
- Cite only URLs you fetched with web_fetch this unit. A search snippet
  is a lead, not a source: fetch the page before citing it. The harness
  checks cited URLs against ones actually retrieved and fails the unit on
  a mismatch, so an invented or remembered URL is not a shortcut, it is a
  failed unit.
- Stop searching a sub-question once it has an answer backed by adequate
  sources. If a query line goes nowhere, note the dead end in one line and
  try a different angle, don't repeat the same query hoping for a
  different result.
- The final unit is synthesis only: read every findings-*.md file, do not
  run new web_search calls. Write `report.md` with write_file: a section
  per sub-question plus an overview, every claim citing a source drawn
  from a findings file's Sources section, closing with one consolidated
  `## Sources` list merged from all findings files.
- Verify each research unit's artifact with a verify_cmd that greps the
  findings file for a `## Sources` heading and at least one `https://`
  URL. Verify the synthesis unit's verify_cmd checks report.md exists and
  contains a `## Sources` heading.
- Conflicting sources get reported as a conflict in both the findings file
  and, if load-bearing, the report. Do not silently pick a side.

## Anti-rationalization

| Excuse | Rebuttal |
|---|---|
| "These sub-questions all touch the same topic, I'll merge them" | Same topic doesn't mean same unit; independence is about whether one needs the other's answer. |
| "The snippet already told me, no need to fetch the page" | A snippet is a lead, not a source; fetch the page, confirm the claim, then cite it. |
| "The synthesis can just re-search to fill a gap" | Synthesis reads findings files only; a gap found at synthesis time means a findings unit was incomplete, not a license to search again. |
| "One source is fine, this fact is obviously true" | Obviousness is not verification; decision-driving claims still need two independent sources. |
| "Close enough to the URL I remember" | The harness matches cited URLs against fetched ones exactly; a remembered or reconstructed URL fails the unit. |

## Red flags: stop and re-check

- A sub-question depends on another sub-question's answer but got its own
  parallel unit anyway.
- A findings file cites a URL that was never fetched with web_fetch in
  that unit.
- The synthesis unit is making a web_search or fetch call.
- report.md contains a claim with no traceable source in any findings
  file's Sources section.
- Every source in a findings file's Sources list is the same domain.

## Evidence required

- Each findings-<slug>.md: answer, cited facts with dates, `## Sources`
  listing only URLs actually retrieved that unit.
- report.md: every claim traceable to a findings file's Sources section,
  closing with a consolidated `## Sources` list.
- Conflicts between sources surfaced explicitly, never silently resolved.
