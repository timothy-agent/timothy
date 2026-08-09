*Issue #, if available:*

*Description of changes (what and why):*

## Checklist

- [ ] Commits follow [Conventional Commits](https://www.conventionalcommits.org/) (subject ≤ 72 chars, body explains why)
- [ ] Tests added or updated for changed logic
- [ ] `make build test vet lint` passes
- [ ] Web changes: `npm run build && npm run lint && npm test` passes (in the Node container)
- [ ] Harness changes (`internal/brain/missions/`, executors): `make canary` passes against a rebuilt brain
- [ ] No secrets, credentials, or personal data in code, tests, or fixtures

By submitting this pull request, I confirm my contribution is made under the terms of the [AGPL-3.0 license](../LICENSE).
