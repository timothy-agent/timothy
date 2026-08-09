# Security Policy

Timothy is a self-hosted assistant that holds provider API keys, OAuth grants, and personal data, and runs model-directed code in sandboxes — security reports are taken seriously.

## Reporting a vulnerability

Please do **not** open a public GitHub issue for security problems.

Instead, use one of:

- **GitHub private vulnerability reporting** (preferred): [Report a vulnerability](https://github.com/timothy-agent/timothy/security/advisories/new) — creates a private advisory only maintainers can see.
- **Email**: [sumonmselim@gmail.com](mailto:sumonmselim@gmail.com) with a description, reproduction steps, and impact assessment.

You can expect an acknowledgment within a few days. Please practice coordinated disclosure: give us a reasonable window to ship a fix before any public write-up.

## Supported versions

Timothy is in alpha. Only the **latest release** receives security fixes; there are no backports. If you're running an older tag, upgrade first and check whether the issue reproduces.

## Scope notes

Things that are **by design** in the current single-operator posture (still worth reporting if you find a way to escalate beyond them):

- Timothy is designed to be operated by a single trusted user; there is no multi-user privilege boundary inside one instance.
- Mission sandboxes run model-authored code with network egress; isolation boundaries between a mission and its own workspace contents are documented as accepted risks (`D-0XX` markers in code).

Things we especially want to hear about:

- Secret material (API keys, tokens, signing keys) leaking into logs, API responses, the database in plaintext, or the frontend
- Sandbox escape to the host or to another mission's workspace
- Authentication bypass on the public API
- SSRF/injection reachable through connector or tool inputs
