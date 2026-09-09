---
name: hackathon
description: Carries a hackathon from its published rules to a submission-ready project. Extracts every rule and constraint first, ranks ideas that all comply, and builds toward the exact deliverables the judges expect. Use when a mission is given a hackathon link or an inline hackathon brief. Do not use for general coding or research tasks that have no rules document, deadline, or judging rubric to satisfy.
---

# Hackathon discipline

## Rules

- Rules before ideas. Given a link, fetch the main page with fetch_url and then every linked rules, FAQ, prizes, schedule, judging criteria, sponsor track, and terms page, HTML and PDF alike, before proposing anything. Follow links found on those pages as well until no new rules-bearing page appears.
- If a search_kb tool is available, search the knowledge base for past entries, rubrics, and sponsor notes on the same event or organizer before the web.
- Produce a rules checklist as the first artifact, `rules-checklist.md`, with one row per constraint: eligibility, team size, registration and submission deadlines, required deliverables (repository, demo video, writeup, sponsor technology), IP and license terms, disallowed content and data, and judging criteria with weights when published. Every row carries the source URL and the quoted sentence it comes from. Nothing enters the checklist without a quote.
- Number the rows R1 upward with no gaps, and state the last ID used at the end of the file as `Rows: R1-Rn`. Every later reference to a row, in any file, uses one of those IDs. A row ID that is not in the checklist is a fabricated citation.
- Convert every deadline to Europe/Amsterdam with convert_time and show both the original and the converted time. State the remaining time from now to the submission deadline; that number drives feasibility later.
- Given an inline brief instead of a link, build the same checklist from the pasted text. Anything the brief leaves open goes under `## Open questions`, one line per gap. Never fill a gap with an assumption.
- Login-walled pages (Devpost dashboards, Discord, private docs) are unreachable through fetch_url. When one is hit, stop, name the page, and ask the operator for the pasted text. Do not guess what it says.
- Propose three to five ideas in `ideas.md`, ranked by three factors in this order: feasibility in the remaining time, prize and judging fit, novelty. Each idea states which checklist rows constrain it and how it complies with each. An idea that violates any row is dropped and listed under `## Dropped` with the violated row; it is never ranked low instead.
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
| "A row roughly like R31 must cover this, the checklist is long" | Open the checklist and read the ID. An unverified row number is a fabricated citation, not a shortcut.              |

## Red flags: stop and re-check

- Ideas are being proposed and no `rules-checklist.md` exists yet.
- A checklist row has a source URL but no quoted sentence, or a quote that was never retrieved.
- A deadline appears in one timezone only.
- An idea's compliance section names no checklist rows.
- A cited row ID is above the checklist's last row, or names a constraint the row does not contain.
- A dropped idea is ranked instead of listed under `## Dropped`.
- The build plan has no unit for the README criteria map, the writeup, or the demo script.
- Code is being written before the operator picked an idea.
- A login wall was met and the mission is inferring the hidden content.

## Evidence required

- `rules-checklist.md` with one row per constraint, each carrying a row ID, a source URL and a quoted sentence, deadlines shown in the organizer's zone and Europe/Amsterdam, a closing `Rows: R1-Rn` line, and an `## Open questions` section (empty is acceptable, missing is not).
- `ideas.md` with three to five ranked ideas, each naming checklist rows that exist in `rules-checklist.md`, and a `## Dropped` section for rule-violating ideas.
- In the build mission: a README with a judging-criteria map, a writeup draft, a demo script, and the final checklist walk with each row marked met, not applicable, or open.
- Actual fetch_url results for every cited page; a search snippet or a remembered URL is not evidence.
