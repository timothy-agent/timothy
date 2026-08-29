---
name: research-brief
description: Source-grounded factual research with verification and citations. Use when a question requires current or externally verifiable facts and a single research path is sufficient. Use deep-research when multiple independent research angles are required.
---

# Research brief discipline

## Rules

- Retrieve before you write: every load-bearing factual claim must be supported by a source actually retrieved this turn, not by trained recall.
- If a search_kb tool is available, search the knowledge base before the web and cite kb sources by their kb:// ref.
- Use two genuinely independent sources for any claim that drives a decision when practical. Syndicated copies, reposts, or sources relying on the same underlying material do not count as independent. One authoritative source is sufficient for background information when additional corroboration would not materially improve confidence.
- Record where each important fact came from. A source mentioned only in the trailing list without being referenced in the body is not a citation.
- Distinguish clearly between what a source states and what you infer from it. Label inference explicitly as `Inference: ...`.
- Prefer primary sources such as official documentation, vendor pages, laws, filings, standards, and original papers over aggregators or secondary sources repeating them.
- Include the relevant publication, effective, event, or access date for time-sensitive facts such as prices, versions, laws, schedules, and availability.
- When credible sources conflict, report the conflict and explain the difference when possible. Do not silently choose one.
- If retrieval fails or a fact cannot be verified, say so. A stated evidence gap is better than a confident guess.
- Answer the actual question asked. Do not turn a focused research request into a broad survey unless the additional information materially helps answer it.
- Close every answer with a `## Sources` section containing a numbered markdown list, one fetched source per line, formatted exactly as:
  `1. [Title](URL)`
  Nothing else on the line. Every listed URL must be a source actually retrieved this turn.
- When comparing a value against a limit or allowance, explicitly show the comparison before stating the conclusion.
- When pricing usage with a free allowance, subtract the allowance before calculating the billable remainder.

## Anti-rationalization

| Excuse                                                     | Rebuttal                                                                                                               |
|------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------|
| "I already know this"                                      | The question requires current or externally verifiable information; use retrieved evidence rather than trained recall. |
| "One good source is enough"                                | Decision-driving claims deserve independent corroboration when practical.                                              |
| "Citing everything clutters the answer"                    | Important actionable claims need traceable evidence.                                                                   |
| "The aggregator summarizes the primary source fine"        | Prefer the primary source when it is available and materially relevant.                                                |
| "Two articles say the same thing, so they are independent" | Not if they rely on the same underlying source or report.                                                              |
| "The source could not be fetched, but I know the URL"      | An unverified or remembered URL is not evidence.                                                                       |
| "I can infer the missing value"                            | Label an inference explicitly; never present an unsupported inference as a sourced fact.                               |

## Red flags: stop and re-check

- You are writing a material number, date, name, price, version, or legal claim that you did not verify from a retrieved source this turn.
- A decision-driving claim has only one weak or non-independent source despite reasonable corroboration being available.
- All citations come from the same domain even though the claim requires independent corroboration.
- The answer contradicts a retrieved source without explaining the discrepancy.
- You are about to answer without making the required retrieval call.
- A source is being cited because a search snippet mentioned it, but the underlying source was never retrieved.
- A retrieved source is being presented as authoritative when it is actually an aggregator or secondary copy of a more authoritative source.

## Evidence required

- Every important factual claim is traceable to a source retrieved this turn.
- Time-sensitive claims include the relevant date.
- Decision-driving claims have independent corroboration when practical.
- Conflicts between credible sources are surfaced explicitly.
- Inferences are clearly distinguished from sourced facts.
- `## Sources` contains only sources actually retrieved this turn.