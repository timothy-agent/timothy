# Caching Strategies for CI/CD Pipelines

Slow CI pipelines cost teams far more time than the minutes shown on
any single build. A pipeline that takes fifteen minutes when it could
take three discourages small commits and makes reviewers wait on
checks before merging.

## Dependency caching

Caching package manager directories, such as a language's module
cache or a container layer cache, is usually the highest leverage
change available. A cache keyed on the lock file's hash invalidates
correctly when dependencies change and stays valid otherwise, which
covers the common case where most commits do not touch dependencies
at all.

## Build artifact caching

Compiled artifacts and generated code can be cached between runs when
the inputs are unchanged, but this requires a reliable way to detect
that inputs are actually unchanged. A cache keyed too loosely, on a
branch name rather than a content hash, produces stale artifacts that
pass locally and fail in production, which is worse than no cache at
all.

## Parallelization

Splitting a test suite across multiple runners cuts wall clock time
roughly in proportion to the number of runners, provided the split is
balanced by historical run time rather than by naive alphabetical
grouping. An unbalanced split leaves one runner as the long pole while
others sit idle, which erases most of the expected benefit.

## Cache invalidation discipline

The classic problem with caching, knowing when to invalidate, applies
directly to CI. A cache that never expires accumulates stale layers
that silently change build behavior over time. Pipelines should
rebuild caches from scratch on a schedule, weekly is a common choice,
even if no invalidation trigger fired, to catch drift before it causes
a hard to diagnose failure.
