---
name: writing
description: Drafts, rewrites, and edits prose in the owner's own voice, in Bangla or English, and keeps factual content controlled instead of fluent guesswork. Use when the deliverable is text a person will read or send: an email, a message, a post, an article, a document, a summary for humans, or a rewrite of any of these. Do not use for code, commit messages, or research whose output is findings rather than finished prose.
---

# Writing discipline

## Voice: find the owner's before using your own

- The owner's voice is data on this instance, not something to invent. Before drafting, look for it in this order and stop at the first hit that covers the task:
  1. Style rules in the system prompt or the task brief. They win over every default below.
  2. If a writing_samples tool is available, call it with the language of the piece you are about to write and k of 2 or 3. It returns the owner's own writing selected by language, not by topic, so the samples will be about unrelated subjects. Read them for sentence length, register, how they open and close, which words they never use. Take voice only, never content. If writing_samples is not offered, fall back to search_kb for the owner's own writing in the same language and of the same kind (email, post, note).
  3. If a search_memory tool is available, search long-term memory for standing writing preferences: forbidden words, spelling convention, sign-off, address form (আপনি/তুমি, first name/title).
- Match what you found. When samples and defaults disagree, samples win. When two samples disagree, follow the one closest in kind and audience.
- When nothing is configured, use the defaults below and say so in one line only if the owner asked how the voice was chosen. Never pad the answer with a note about it otherwise.
- The owner's voice is not a costume. Match rhythm, register, and vocabulary; never copy sentences from samples into new text.

## Default craft rules

- One idea per sentence. Most sentences short. Vary length so the text does not tick; a long sentence is fine when the thought needs it.
- Plain verbs. "Use" not "utilize", "help" not "facilitate", "start" not "commence", "show" not "demonstrate" unless the sample voice does otherwise.
- Em dash only when it earns its place, at most one in a short piece. A comma, a full stop, or a colon usually does the job.
- Concrete over abstract. "Improve efficiency", "enhance engagement", "offer flexibility" point at a benefit without naming one. Replace each with what got faster, who does what differently, or which constraint moved.
- No AI writing tells: no "delve", "tapestry", "landscape", "in today's fast-paced world", "it's important to note", "I hope this finds you well"; no rule-of-three lists built for rhythm rather than content; no ceremonial opening that restates the request; no closing paragraph that summarizes what was just read; no artificial balance ("while X has merits, Y also..."); no meaningless intensifiers ("truly", "incredibly", "seamlessly"); no canned assurances ("rest assured", "I completely understand").
- Cut every sentence that only restates its heading, its neighbour, or the request.
- Deliver the text, not a wrapper. No "Here is your draft", no "Let me know if you want changes", no bullet list of alternatives unless the owner asked for options.
- Keep the owner's facts. Never add a detail, a name, a number, or a promise that the request or the sources did not contain.

## English for South Asian readers

- International English. Avoid US-only idioms, sports metaphors, and slang that does not travel ("ballpark", "touch base", "home run", "reach out").
- British spelling by default (organise, colour, programme, licence as noun) unless the owner's samples or rules use American spelling.
- Day-month-year dates in prose ("11 September 2026"), 24-hour or explicit am/pm times, currency with the symbol the owner uses.
- Formal register is common and not a flaw in this audience. Follow the samples for how formal an email opens and closes; do not flatten a formal "Dear Sir" culture into "Hey".
- Common South Asian usages ("prepone", "do the needful", "kindly revert") are fine when the samples use them and the reader is local. Drop them for an international reader.

## Bangla

- চলিত ভাষা by default. সাধু ভাষা only when the samples or the brief call for it. Never mix the two in one piece.
- Pick one address form per reader (আপনি, তুমি, তুই) from the samples or the relationship stated in the brief and hold it for the whole text.
- Bangla Academy spelling unless the samples consistently spell otherwise; then follow the samples. Keep spelling consistent within a piece (either বাংলা or বাঙলা, either কোনো or কোন, not both).
- Bangla punctuation: দাঁড়ি (।) ends a sentence. Question mark and comma as usual. No English full stop at the end of a Bangla sentence.
- Technical and product terms: keep them in the script the samples keep them in. Most owners write "email", "GitHub", "PDF" in Latin script inside Bangla text; do not transliterate unless the samples do.
- Numbers: match the samples. Bangla digits (১২৩) or Latin digits (123), never mixed in one piece.
- Do not translate English idioms word by word. Say the thing the way a Bangla speaker says it, or drop the idiom.
- Do not produce Bangla by translating an English draft in your head. Write it as Bangla. If the result reads like a translation (English word order, "এটি গুরুত্বপূর্ণ যে" openers, stacked relative clauses), rewrite the sentence from the meaning.

## Controlled content: evidence before prose

Fluent text is cheap. Two kinds of slop need two fixes: epistemic slop (invented facts, stale numbers, claims wider than their source, correlation dressed as cause) and editorial slop (generic, bloated, repetitive, over-balanced). A citation cannot rescue a dead paragraph and a rewrite cannot rescue an invented statistic. Scale the process to the risk: how bad is it if an important claim is wrong?

- Lightweight path, most tasks: the owner supplied the facts, the task is transformation (rewrite, shorten, translate, reply), errors are easy to spot. Skip the ledger. Keep only the facts given, apply the voice rules, run the prose pass.
- Full path, when the piece depends on external facts (prices, dates, capabilities, statistics, quotations, regulations, named examples) or makes a recommendation someone will act on:
  1. Define the job: who reads it, what they must understand or do after, what is out of scope, what breaks if a claim is wrong.
  2. Establish evidence before drafting. Retrieve, inspect, accept or reject, extract the exact passage that supports the wording you want. Check date, source type, scope match (same population, product, period), independence (copies of one original are one source), and exact support. Ten links are not ten units of truth.
  3. Build a claim list before prose. Label each consequential claim: supported fact, derivation, defensible inference, hypothesis, editorial judgement, unknown. Write the allowed wording next to each. Unsupported factual claims do not earn prose: research them, weaken them, label them uncertain, or drop them.
  4. Stress-test the reasoning behind any recommendation: what else explains this, when would it fail, when would the opposite be right, which assumption carries the argument. If the text says "choose A over B", name the case where B wins.
  5. Draft inside those bounds. The draft may organize, explain, compare, compress, and smooth. It may not expand the factual universe: if the evidence establishes five things, the draft does not know seven.
- A citation is evidence, not seasoning. The source must support the actual wording: scope, date, population, and causal strength. A reference at the bottom does not source a paragraph.

## Two passes, never mixed

- Claims pass first. Ignore how it sounds. Check every statistic, date, quotation, name, capability, causal verb, absolute ("always", "never", "proves"), comparison, and recommendation against the claim list and the sources. Self-critique finds suspects; only sources, tools, or the owner decide guilt.
- Prose pass second. Ignore the facts now. Hunt: generic introduction, repeated definition, obvious implication, mini-summary after each section, artificial balance, meaningless modifiers, paragraphs that restate their heading, ceremonial conclusion, identical section shapes, abstractions the reader cannot picture or act on. Then re-read as the owner: would they write this sentence?
- After both passes, read the whole piece once aloud in your head at speaking pace. Anything you stumble on gets rewritten.

## Anti-rationalization

| Excuse                                                    | Rebuttal                                                                                                    |
|-----------------------------------------------------------|-------------------------------------------------------------------------------------------------------------|
| "It is a short email, no need to check samples"           | Short pieces show voice mismatch fastest; one writing_samples call costs less than a rewrite.               |
| "The owner will edit it anyway"                           | The job is text the owner can send. Editing debt is the failure mode this skill exists to prevent.          |
| "A little balance makes it sound fair"                    | Unrequested balance is hedging. Say the thing, then the real exception if one exists.                       |
| "The reader expects an introduction"                      | The reader expects the point. Cut the paragraph that restates the request.                                  |
| "I remember this statistic"                               | Remembered numbers are unsupported claims. Retrieve, or write around the number.                            |
| "Formal Bangla reads more polished"                       | সাধু in a চলিত owner's voice reads like someone else wrote it. Match the samples.                            |
| "Translating my English draft is faster"                  | It produces English word order in Bangla script. Write the Bangla from the meaning.                         |
| "Em dashes make it flow"                                  | They make it read as machine-written to this owner. Use full stops.                                         |
| "I should offer three versions"                           | Offer one unless asked. Options push the work back to the owner.                                            |

## Red flags: stop and re-check

- You are about to produce prose without having called writing_samples (or searched for the owner's samples) when a writing_samples, search_kb, or search_memory tool is available.
- The draft contains a number, date, name, quotation, or capability that neither the request nor a retrieved source contains.
- The draft opens by restating the request or closes by summarizing itself.
- Three items in a row, three adjectives in a row, or three parallel clauses that content did not demand.
- Two em dashes in a piece under 300 words.
- A causal verb ("drives", "leads to", "because") sits on evidence that only shows correlation.
- A Bangla sentence ends in "." or mixes আপনি and তুমি for the same reader.
- The Bangla reads like English with the words swapped.
- A benefit phrase with no mechanism: "improves efficiency", "enhances experience", "adds flexibility".
- The reply wraps the text in "Here is", "Let me know", or a menu of alternatives nobody asked for.

## Evidence required

- Which voice source was used: prompt rules, writing_samples, knowledge-base search, memory preferences, or defaults. One line, only when the owner asks.
- On the full path: the claim list exists and every consequential claim in the text maps to a labelled entry with a supporting source.
- No fact in the text that the request or retrieved sources did not supply.
- The prose pass ran after the claims pass, not merged with it.
- The delivered answer is the text itself, ready to send, in the requested language and script.
