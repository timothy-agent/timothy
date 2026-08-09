# Contributing Guidelines

Thank you for your interest in contributing to Timothy. Whether it's a bug report, a new feature, a correction, or additional documentation, feedback and contributions are welcome.

Please read this document before submitting an issue or pull request.

## Security issue notifications

If you discover a potential security issue, please follow the process in [SECURITY.md](SECURITY.md). Do **not** create a public GitHub issue for security problems.

## Reporting bugs / requesting features

Use the [GitHub issue tracker](https://github.com/timothy-agent/timothy/issues). Before filing, check open and recently closed issues to avoid duplicates. Useful details:

- Reproducible steps or a test case
- The release tag or commit you're running (`TIMOTHY_VERSION` in your `.env`, or `git rev-parse HEAD`)
- Relevant service logs (`make logs` or `docker compose logs <service>`), with any secrets removed
- Anything unusual about your environment or deployment

## Development setup

Everything runs in Docker; no host Go or Node toolchain is required. See the [README](README.md) for the full build-from-source guide. The short version:

```sh
cp deploy/env.example deploy/.env   # set POSTGRES_PASSWORD
make up                             # start the stack
make build test vet lint            # the canonical pre-commit gates
```

Web checks run in a Node container:

```sh
docker run --rm -v "$PWD/web":/app -w /app node:24.18.0-alpine \
  sh -c "npm run build && npm run lint && npm test"
```

Integration tests (`make test-integration`) and the mission regression gate (`make canary`) need the compose stack up.

## Contributing via pull requests

Before sending a pull request:

1. Work against the latest `main`.
2. Check open and recently merged PRs for overlap.
3. For significant changes, open an issue first to discuss the approach, so your time isn't wasted on a direction we can't merge.

When submitting:

1. Fork the repository and create a branch named `<type>/<short-description>` (e.g. `feat/mission-labels`, `fix/route-cache`).
2. Keep the change focused. Unrelated reformatting or refactoring makes review harder.
3. Follow [Conventional Commits](https://www.conventionalcommits.org/): `<type>[scope]: <description>` with a subject ≤ 72 chars, lowercase, no trailing period. The body explains **why**, not what.
4. Add or update tests for any logic you add or change. Table-driven tests are the house style for Go.
5. Make sure `make build test vet lint` and the web checks pass locally.
6. If your change touches the missions harness (anything under `internal/brain/missions/` or the executor path), run `make canary` against a rebuilt brain.
7. Fill in the pull request template and stay involved in the review conversation.

### Project invariants

A few rules are enforced in review and are not up for relaxation. See [CLAUDE.md](CLAUDE.md) for the full list. Highlights:

- Append-only stores stay append-only (`session_events`, `mission_events`, `memories`).
- Safety invariants (allowlists, ceilings, permission gates) live in Go code, never in a prompt.
- Secrets are referenced by name (`credential_ref`) only; raw values never appear in the database, API responses, logs, or frontend.
- Unknown prices are recorded as NULL, never guessed.
- No speculative abstractions: no interface with one implementation, no config nothing reads.
- Never edit an already-applied migration (except during the pre-release window, per maintainer direction).

## Finding contributions to work on

Issues labeled `help wanted` or `good first issue` are the best starting points. Documentation improvements are always welcome.

## Code of Conduct

This project has adopted a [Code of Conduct](CODE_OF_CONDUCT.md). By participating, you agree to uphold it.

## Licensing

Timothy is licensed under [AGPL-3.0](LICENSE). By submitting a pull request, you agree that your contribution is licensed under the same terms.
