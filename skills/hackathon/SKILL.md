---
name: hackathon
description: Carries a hackathon from its published rules to a submission-ready project. Extracts every rule and constraint first, ranks ideas that all comply, and builds toward the exact deliverables the judges expect. Use when a mission is given a hackathon link or an inline hackathon brief. Do not use for general coding or research tasks that have no rules document, deadline, or judging rubric to satisfy.
---

# Hackathon discipline

## Rules

- Rules before ideas. Given a link, fetch the main page with fetch_url and then every linked rules, FAQ, prizes, schedule, judging criteria, sponsor track, and terms page, HTML and PDF alike, before proposing anything. Follow links found on those pages as well until no new rules-bearing page appears.
- If a search_kb tool is available, search the knowledge base for past entries, rubrics, and sponsor notes on the same event or organizer before the web.
- If a search_memory tool is available, search long-term memory before writing ideas: the operator's languages and stack, the infrastructure and data they run, the tasks they repeat, and any standing preference. One query per topic. The operator-fit rule below cannot be met from the goal text alone.
- Produce a rules checklist as the first artifact, `rules-checklist.md`, with one row per constraint: eligibility, team size, registration and submission deadlines, required deliverables (repository, demo video, writeup, sponsor technology), IP and license terms, disallowed content and data, and judging criteria with weights when published. Every row carries the source URL and the quoted sentence it comes from. Nothing enters the checklist without a quote.
- Number the rows R1 upward with no gaps, and state the last ID used at the end of the file as `Rows: R1-Rn`. Every later reference to a row, in any file, uses one of those IDs. A row ID that is not in the checklist is a fabricated citation.
- Convert every deadline to Europe/Amsterdam with convert_time and show both the original and the converted time. State the remaining time from now to the submission deadline; that number drives feasibility later.
- Given an inline brief instead of a link, build the same checklist from the pasted text. Anything the brief leaves open goes under `## Open questions`, one line per gap. Never fill a gap with an assumption.
- Login-walled pages (Devpost dashboards, Discord, private docs) are unreachable through fetch_url. When one is hit, stop, name the page, and ask the operator for the pasted text. Do not guess what it says.
- Generate a wide pool of candidates first, at least fifteen, and cut before researching. Drop the rule violators, the thin wrappers, the ones whose sponsor use is decorative, and the ones that cannot be demoed. Research only what survives; researching the whole pool wastes the time the build needs.
- Research each surviving candidate against what already exists: products, GitHub, papers, articles, prior entries to this event, sponsor examples. Name the closest existing solution, what it already solves, and what this idea does differently. A candidate with no named comparison has not been researched.
- Rate differentiation in words, not numbers: highly differentiated, differentiated, somewhat differentiated, crowded, incremental, or unclear. An empty search result means unclear, never novel. Say what the searches covered and what they could not reach.
- Propose five ideas in `ideas.md` (three or four only when the rules narrow the space that far), ranked by three factors in this order: feasibility in the remaining time, prize and judging fit, novelty. Each idea states which checklist rows constrain it and how it complies with each. When the rules publish weighted judging criteria, rank against those weights and say which criterion decided a close call. Do not sum the criteria into one score: a total to two figures reads as a measurement, and this is a judgement. An idea that violates any row is dropped and listed under `## Dropped` with the violated row; it is never ranked low instead. `## Dropped` also carries the pool candidates that were cut, one line each with the reason: rule violation and its row, too generic, crowded, infeasible in the time, or undemoable. A cut with no reason is an idea that was forgotten, not judged.
- Each idea carries enough for the operator to choose without asking a follow-up question, in this shape, roughly a page:

```
### <name>
**Pitch:** one or two sentences: the problem, who has it, what the thing does end to end.
**Fit:** why this event and this operator, naming the checklist rows that constrain it and how it complies with each.
**Differentiation:** closest existing solution, what it already solves, what this does differently, and the rating in words.
**Build:** components, sponsor technology, data and integrations, what runs where, and the thinnest version that still wins on the judging criteria.
**Demo:** three or four beats, ending on what a judge remembers.
**Why choose it:** the strongest single reason.
**Why not:** the biggest risk to finishing in the time left, or the weakness a judge will find.
```
- Name what a judge would find non-obvious about each idea, and what the agent does that a single prompt or a scripted workflow could not. When the judging criteria weigh originality, the obvious framing of the event's own theme is the entry every other submission files: say so and go past it.
- Ideas are for this operator, not a generic entrant. Use what search_memory and the goal say about them, their languages and stack, the infrastructure and data they already run, and the tasks they actually repeat. When a required sponsor technology rules out a standing preference, say which preference yields and why.
- Before writing `ideas.md`, re-read `rules-checklist.md` and cite only IDs you can see in it. Cite the row for what it actually says: quote or paraphrase each row you name, so a wrong ID is visible as a mismatch. Never continue an ID sequence past the last row, and never carry a row number over from another event.
- Stop after the ideas. The build starts only after the operator picks one. When the mission has a followup_mission tool, offer to open the build mission with the chosen idea and the checklist attached.
- In the build mission, the plan lists the submission pack as explicit units alongside the code: a README that maps each judging criterion to where the project meets it, a writeup draft in the platform's required shape and length, and a demo script timed to the allowed video length. A plan that only builds code is incomplete.
- Every build unit that ships a deliverable names the checklist row it satisfies. Before declaring done, walk the checklist top to bottom and mark each row met, not applicable, or open; open rows go to the operator.
- Use only libraries, models, and data the rules allow. When a sponsor technology is required for a track, it appears in the code, not only in the writeup.
- Submitting on the platform and recording the demo video belong to the operator. Prepare the text, the script, and the repository state; do not claim either was done.

## Anti-rationalization

| Excuse                                                          | Rebuttal                                                                                                           |
|-----------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------|
| "The rules page is long, the summary on the landing page is enough" | Landing pages omit eligibility and IP terms; fetch the full rules and quote them.                                |
| "This idea is great, it only bends one rule"                    | A rule violation disqualifies the whole entry. Drop the idea and say which row it broke.                            |
| "The deadline is obviously in my timezone"                      | Hackathon deadlines are published in the organizer's zone; convert and show both.                                   |
| "I'll write the README and video script at the end if time allows" | The submission pack is a deliverable the judges score. It is planned as units, not leftovers.                    |
| "The brief did not mention team size, so there is no limit"     | Silence in a brief is an open question, not permission.                                                            |
| "I remember how Devpost submissions work"                       | Platforms change their required fields; read this event's page or ask for the pasted text.                         |
| "The sponsor API is hard, I'll mention it in the writeup"       | Sponsor tracks are judged on real use; the technology must run in the code.                                        |
| "The operator can flesh out the interesting ideas themselves"   | An idea without a build sketch and a named risk cannot be chosen between. Cost the work before ranking it.          |
| "I know enough about the operator from the goal"                | The goal names an event, not a person. Search memory for their stack and preferences, then write.                  |
| "The first ideas that fit the theme are the ideas"              | The obvious reading of the theme is what every other entry submits; originality is scored, so go past it.           |
| "The search found nothing like it, so it is novel"              | An empty result is unclear, not novel. Name what was searched and what it could not reach.                          |
| "Fifteen candidates is a lot of writing for five slots"         | The pool is thinking, not writing. One line each, then cut, then research the survivors.                            |
| "A row roughly like R31 must cover this, the checklist is long" | Open the checklist and read the ID. An unverified row number is a fabricated citation, not a shortcut.              |

## Red flags: stop and re-check

- Ideas are being proposed and no `rules-checklist.md` exists yet.
- A checklist row has a source URL but no quoted sentence, or a quote that was never retrieved.
- A deadline appears in one timezone only.
- An idea's compliance section names no checklist rows.
- A cited row ID is above the checklist's last row, or names a constraint the row does not contain.
- An idea is a one-liner with no build sketch, no demo beats, and no named risk.
- An idea claims novelty, or names no existing solution to compare against.
- The five ideas were written straight out, with no wider pool cut down first.
- Every idea is the event theme's most obvious reading, or could have been written without knowing anything about this operator.
- `ideas.md` is being written and search_memory was offered but never called.
- A dropped idea is ranked instead of listed under `## Dropped`.
- The build plan has no unit for the README criteria map, the writeup, or the demo script.
- Code is being written before the operator picked an idea.
- A login wall was met and the mission is inferring the hidden content.

## Evidence required

- `rules-checklist.md` with one row per constraint, each carrying a row ID, a source URL and a quoted sentence, deadlines shown in the organizer's zone and Europe/Amsterdam, a closing `Rows: R1-Rn` line, and an `## Open questions` section (empty is acceptable, missing is not).
- `ideas.md` with five ranked ideas (three or four only when the rules narrow the space that far), each naming checklist rows that exist in `rules-checklist.md`, each carrying pitch, fit, differentiation with a named closest solution and a worded rating, build sketch, demo beats, thinnest winning version, non-obvious angle, why choose it and why not, and a `## Dropped` section listing both the rule-violating ideas and the cut pool candidates, each with its reason.
- In the build mission: a README with a judging-criteria map, a writeup draft, a demo script, and the final checklist walk with each row marked met, not applicable, or open.
- Actual fetch_url results for every cited page; a search snippet or a remembered URL is not evidence.
