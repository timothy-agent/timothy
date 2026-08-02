---
name: coding-task
description: Disciplined software changes: plan first, test first, verify end-to-end. Use when writing, modifying, debugging, or reviewing code, or when a task produces anything a compiler, interpreter, or test suite will judge.
---

# Coding task discipline

## Rules

- State the plan before the first edit: what changes, where, and how you will know it worked.
- Write or identify the failing test before writing the fix; a bug you cannot reproduce is a bug you cannot claim to have fixed.
- Make the smallest change that satisfies the requirement; delete nothing you did not orphan yourself.
- Build and run the tests after every coherent step, not once at the end: a broken intermediate state with ten changes in flight has ten suspects.
- Read the actual error message before theorizing; quote it, don't paraphrase it.
- When a fix doesn't work, revert to the last known-good state before trying the next idea; stacked failed attempts compound.
- Match the codebase's existing style, naming, and idiom even where you disagree with it.
- Never weaken an assertion, delete a test, or broaden an exception handler to make a failure go away; make the code satisfy the test or prove the test wrong.
- Treat compiler warnings and linter findings in changed code as errors.

## Anti-rationalization

| Excuse | Rebuttal |
|---|---|
| "Tests slow me down" | Untested code is unfinished code; the time returns on the first regression. |
| "It's a one-line change, no need to run anything" | One-line changes have shipped outages; the cost of running the suite is minutes. |
| "The error is probably X, let me just fix that" | Probably is a guess; reproduce first, then fix what actually failed. |
| "I'll clean this up later" | Later never has more context than now. |
| "The existing style is bad, I'll improve it while I'm here" | Unrequested churn hides the real change and breaks review. |

## Red flags: stop and re-check

- You are about to change a test's expected value to match new output.
- You cannot explain why the fix works, only that it does.
- The diff touches files unrelated to the stated task.
- You are adding a sleep, retry, or timeout to make a test pass.
- Two consecutive fix attempts failed: stop patching, start reading.

## Evidence required

- The failing output before the fix and the passing output after, from the same command.
- A clean build/lint/test run at the final state, quoted, not summarized.
- Self-assessment ("looks correct", "should work") counts for nothing.
