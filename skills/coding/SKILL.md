---
name: coding
description: Disciplined software changes: understand the requirement, reproduce failures, make the smallest safe change, and verify it with evidence. Use when writing, modifying, debugging, or reviewing code, or when a task produces anything a compiler, interpreter, linter, or test suite will evaluate.
---

# Coding task discipline

## Rules

- Before editing, inspect the relevant code, tests, configuration, and repository conventions enough to understand the requested change and define success criteria.
- State the plan before the first edit: what changes, where, and how you will verify them.
- For bugs, reproduce the failure and write or identify a failing test before fixing it when practical. For other changes, define the verification criteria before editing.
- Make the smallest change that satisfies the requirement; avoid unrelated cleanup or refactoring.
- After each coherent change, run the smallest relevant verification available. After completing the coherent unit of work, run broader applicable tests.
- Read the actual error output before theorizing; quote it rather than paraphrasing it.
- When a fix fails, return to the last known-good state before trying a different fix, while preserving useful diagnostic instrumentation.
- Match the codebase's existing style, naming, architecture, and idioms unless the task explicitly requires changing them.
- Never weaken assertions, delete tests, or broaden exception handling merely to make failures disappear.
- Treat new compiler warnings and linter findings introduced by the change as errors.

## Anti-rationalization

| Excuse                                            | Rebuttal                                                                          |
|---------------------------------------------------|-----------------------------------------------------------------------------------|
| "Tests slow me down"                              | Use the smallest relevant verification first; unverified code is unfinished code. |
| "It's a one-line change, no need to run anything" | Small changes can still cause regressions; run the applicable verification.       |
| "The error is probably X"                         | Probably is a guess; reproduce and inspect the actual failure first.              |
| "I'll clean this up while I'm here"               | Unrequested churn hides the real change and increases regression risk.            |
| "The existing style is bad"                       | Match the repository unless changing it is part of the task.                      |

## Red flags: stop and re-check

- You are about to change a test expectation to match new output without establishing that the expected behavior should change.
- You cannot explain why the fix works, only that a test happens to pass.
- The diff touches files unrelated to the stated task.
- You are adding a sleep, retry, timeout, or exception handling solely to make a test pass.
- Two consecutive fix attempts failed: stop patching and inspect the code, test, and failure more deeply.
- You cannot distinguish a newly introduced failure from a pre-existing failure.

## Evidence required

- For bug fixes: evidence of the failure before the fix and successful verification after the fix.
- Applicable verification at the final state: build, lint, tests, type checks, static analysis, or other repository-specific checks.
- Report actual command results and relevant output rather than claiming that something "looks correct."
- Distinguish pre-existing failures from regressions introduced by the current change.
