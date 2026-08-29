---
name: email-research
description: Searches, reads, and aggregates Gmail content, finding bookings, costs, topics, or senders, and summarizing or digesting threads. Use when the ask involves email in any way: finding a message, extracting amounts or dates from mail, summarizing a thread, or digesting what a sender or label contains.
---

# Email research discipline

## Rules

- Gmail query operators (search_mail's Google accounts; a Microsoft/Outlook account takes a plain keyword query instead: see the tool's own description for which accounts are connected), used correctly:
  - Quote multi-word phrases: `subject:"booking confirmation"`, not `subject:booking confirmation` (the latter only matches the first word as the operator's argument).
  - `from:`/`to:` take an address or domain: `from:noreply@airline.com`, `from:airline.com`.
  - OR grouping: `{from:a@x.com from:b@x.com}` or `(from:a@x.com OR from:b@x.com)`; bare `OR` between terms with no parens/braces is parsed unreliably, always group it explicitly.
  - Date bounds: `after:YYYY/MM/DD`, `before:YYYY/MM/DD`, `newer_than:Nd` (or `Nm`/`Ny`), `older_than:Nd`.
  - `has:attachment`, `label:`, `category:`, `is:unread`, `is:read`.
- Date bounds MUST be derived from the current date in the system prompt, never a guessed or recalled year. If a search built from a "today" you assumed returns nothing, re-derive the bound from the actual system-prompt date before concluding the mail isn't there.
- Pipeline discipline: start broad (wide date range, no subject/keyword narrowing), then narrow iteratively; a `from:` combined with subject/keyword terms narrows twice and a near-miss becomes a zero-result search.
- A `search_mail` result (subjects, senders, snippets) is a digest, not content. Before citing or aggregating what is "in" any message, `read_mail` it: snippets truncate and routinely omit the amount, date, or detail you need.
- Extract every amount together with its currency (`$42.50`, `€19`, `12.00 USD`): a bare number is not usable in a total.
- All arithmetic (sums, counts, averages) goes through the `calculate` tool; never add or estimate in your head. Show the expression you evaluated.
- Any total or aggregate figure shows its per-item breakdown (each item's amount and source email) alongside the total, not the total alone.
- Distinguish confirmations/receipts from marketing and reminders, even from the same sender domain: a confirmation states a completed transaction with an amount and reference number; marketing and reminders promise, upsell, or nudge. Label which is which rather than lumping every message from a domain together.

## Multi-currency

- Report one subtotal PER currency present; never silently convert or combine different currencies into one number.
- If a conversion is explicitly requested, state the rate and its date/source; otherwise leave amounts in their original currency.

## Anti-rationalization

| Excuse                                                        | Rebuttal                                                                                  |
|---------------------------------------------------------------|-------------------------------------------------------------------------------------------|
| "The snippet already shows the amount"                        | Snippets truncate; read_mail the message before citing a figure from it.                 |
| "I can add these three numbers myself"                        | Mental arithmetic on amounts you'll report as fact goes through calculate, no exceptions. |
| "Combining $40 and €40 into one total is close enough"        | Different currencies are different totals; report each separately.                        |
| "It's from the same sender as the confirmation, so it counts" | Marketing and reminders from that domain are not the transaction; label them separately.  |
| "The user's message implies this year"                        | Never guess a year: read it from the system prompt's current date.                        |
| "No mail tool is available, but I probably know the answer"   | Say "email not connected"; never answer from assumption when the tool is simply missing.  |

## Red flags: stop and re-check

- An amount is being reported without its currency.
- A total appears with no per-item breakdown, or was computed without calculate.
- A date bound (`after:`/`before:`) was written without checking the system prompt's current date first.
- A claim about a message's content is being made from a search snippet alone, with no read_mail call this turn.
- Two different currencies are being added into a single total.
- A privacy refusal ("I can't access your email") is about to be given while mail tools are present in this turn's tool list.

## Evidence required

- Every cited message actually opened with read_mail this turn (or read_mail_attachment, for attachment content).
- Every amount paired with its currency and its source message.
- Every total accompanied by the calculate expression used and the per-item breakdown it was built from.
- Confirmations distinguished from marketing/reminders explicitly, not merged by sender domain alone.
