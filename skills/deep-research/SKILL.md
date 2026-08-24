---
name: deep-research
description: Decomposes complex research questions into independent sub-questions, researches each in its own unit, then synthesizes a cited report. Use when a question requires multiple independent angles of investigation. Do not use when a single research path is sufficient to answer the question.
---

# Deep research discipline

## Rules

- Determine whether the task genuinely requires multi-angle research. When multiple independent angles are required, decompose the goal into 2-4 independent sub-questions during explore. A sub-question is independent only if it can be researched without knowing the answer to another one; questions that depend on each other's results stay together in one unit.
- Scout each sub-question with 1-2 web_search calls before locking the plan: confirm it is actually answerable and roughly how much material exists. This is reconnaissance, not the research itself.
- If a kb_search tool is available, search the knowledge base before the web and cite kb sources by their kb:// ref.
- Use one research unit per independent sub-question. Each unit declares exactly one artifact, `findings-<slug>.md`, created with write_file. Each findings file contains:
  - the answer to that sub-question
  - key supporting facts with inline citations
  - relevant dates for time-sensitive facts
  - a closing `## Sources` section listing only sources actually retrieved or seen in that unit
- Close every findings file with `## Sources`: numbered list, one line each, `1. [Title](URL)`. Do not add dates or notes to those lines. A source listed there must be cited in the body.
- For any decision-driving claim, seek two genuinely independent corroborating sources when practical. Syndicated copies, reposts, or sources clearly relying on the same underlying material do not count as independent. One authoritative source is sufficient for background information when additional corroboration would not materially improve confidence.
- Prefer primary sources such as official documentation, vendor statements, filings, standards, or original papers over secondary sources repeating them.
- Label reasoning explicitly as `Inference: ...` whenever it goes beyond what a source directly states. Never blur sourced facts and inference.
- Cite only sources actually retrieved during the research unit. A search snippet is a lead, not evidence: retrieve the underlying page before citing it. Never invent, reconstruct, or rely on remembered URLs.
- Stop researching a sub-question once it has an adequately supported answer. If a query goes nowhere, note the dead end briefly and try a materially different research angle rather than repeating the same query.
- The final unit is synthesis only: read every `findings-*.md` file and do not perform new web research. Write `report.md` with an overview plus a section per sub-question, using claims supported by the findings files. If synthesis discovers a material evidence gap, return it to the appropriate research unit rather than silently filling it.
- Close `report.md` with one consolidated `## Sources` list merged from the findings files. Every substantive factual claim in the report must be traceable to a cited source in those findings files; analytical conclusions should be clearly identified as inference when appropriate.
- Verify each research unit's artifact with a verify_cmd that confirms the findings file exists, contains a `## Sources` heading, and contains at least one retrieved source URL. Verify the synthesis unit's verify_cmd confirms `report.md` exists and contains a `## Sources` heading.
- Conflicting credible sources must be reported explicitly as a conflict in both the relevant findings file and, when load-bearing, the final report. Do not silently pick a side.

## Anti-rationalization

| Excuse                                                                          | Rebuttal                                                                                                                  |
|---------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------|
| "These sub-questions all touch the same topic, I'll merge them"                 | Same topic does not mean same unit; independence is determined by whether one answer is required to research another.     |
| "The snippet already told me, no need to fetch the page"                        | A snippet is a lead, not evidence; retrieve the source, confirm the claim, then cite it.                                  |
| "The synthesis can just re-search to fill a gap"                                | Synthesis is evidence assembly, not new research. A material gap means the appropriate research unit is incomplete.       |
| "One source is fine, this fact is obviously true"                               | Obviousness is not verification; decision-driving claims require independent corroboration when practical.                |
| "These two articles count as independent sources"                               | Not if they merely repeat the same underlying source or reporting.                                                        |
| "Close enough to the URL I remember"                                            | Cite only a source actually retrieved during the research unit; reconstructed or remembered URLs are not evidence.        |
| "All the useful sources are from the same domain, so the research must be weak" | Domain diversity is not required when a source is authoritative and independence would not materially improve confidence. |

## Red flags: stop and re-check

- A sub-question depends on another sub-question's answer but was split into its own parallel unit.
- A research unit cites a URL that was never actually retrieved in that unit.
- A search snippet is being cited as evidence without retrieving the underlying source.
- The synthesis unit is making new web_search or fetch calls.
- A material claim in `report.md` has no traceable source in the findings files.
- A decision-driving claim relies on a single non-authoritative source when independent corroboration is reasonably available.
- Two supposedly independent sources clearly derive from the same underlying material.
- A conflict between credible sources is being silently resolved.
- A research unit is repeating unsuccessful searches without changing the research angle.

## Evidence required

- Each `findings-<slug>.md` contains the answer, supporting cited facts, relevant dates for time-sensitive claims, and a `## Sources` section listing only sources actually retrieved in that unit.
- Every cited source in a findings file is traceable to a source actually retrieved during that unit.
- Decision-driving claims have independent corroboration when practical, with primary sources preferred.
- `report.md` contains an overview, a section per research unit, and claims traceable to sources recorded in the findings files.
- `report.md` closes with a consolidated `## Sources` list.
- Conflicts between credible sources are surfaced explicitly rather than silently resolved.