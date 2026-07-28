---
name: daily-briefing
description: Writes a structured briefing covering every topic named in the mission goal, plus calendar/email highlights when connected. Use when running a scheduled/recurring briefing mission — never for one-off research or chat answers.
---

# Daily briefing discipline

## Rules

- Cover EVERY topic named in the mission goal — check each one with web_search. A topic with nothing new still gets a line saying so; never silently drop it.
- Last ~24 hours only: this is a daily brief, not a history lesson. Older context only if a topic literally has nothing from the last day.
- Every claim carries a source URL and a date/time — a briefing fact with no citation is not a fact, it's a guess.
- Calendar section only when a calendar tool is actually available. If it's not, write "calendar not connected" — never invent events or imply a schedule you cannot see.
- Email section only when a gmail tool is available: use `gmail_search is:unread newer_than:1d`, summarize senders and subjects, never long quotes from message bodies. No tool available → "email not connected".
- Write the full briefing as `briefing.md` at the workspace root via write_file — this is the mission's artifact contract; nothing else counts as the deliverable.
- Structure every briefing the same way: Topics (one subsection per goal topic), Calendar, Email, Sources — consistency is what makes a recurring brief scannable at a glance.
- Only call gmail_send if the mission goal explicitly names a recipient. Subject line: "Daily briefing — <date>". No goal-named recipient → never send, the artifact alone is the deliverable.

## Anti-rationalization

| Excuse | Rebuttal |
|---|---|
| "This topic had nothing, skip it" | The reader expects every goal topic accounted for; "nothing new" is itself the answer. |
| "I remember calendar tools were connected last time" | Check THIS turn's tool list; connectivity can change between runs. |
| "Paraphrasing the email preview is basically quoting it" | Keep it to sender + subject + one-line gist — the reader can open their own inbox for more. |
| "The goal didn't say NOT to email it" | Silence is not permission; only an explicit recipient in the goal authorizes gmail_send. |

## Red flags — stop and re-check

- A topic named in the mission goal is missing from the briefing entirely.
- A claim has no source URL or no date.
- The calendar or email section contains content but no tool was actually called this turn.
- gmail_send was called without an explicit recipient named in the goal.

## Evidence required

- Source and timestamp on every topic claim.
- Every topic named in the mission goal present, including "nothing new" topics.
- briefing.md actually written via write_file at the workspace root before ending the turn.
