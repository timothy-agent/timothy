# Timothy

[![CI](https://github.com/timothy-agent/timothy/actions/workflows/ci.yml/badge.svg)](https://github.com/timothy-agent/timothy/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/timothy-agent/timothy/graph/badge.svg?token=TTV3A14CFX)](https://codecov.io/gh/timothy-agent/timothy)
[![Release](https://img.shields.io/github/v/release/timothy-agent/timothy?include_prereleases&sort=semver&label=release)](https://github.com/timothy-agent/timothy/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](https://react.dev)
[![TypeScript](https://img.shields.io/badge/TypeScript-7.0-3178C6?logo=typescript&logoColor=white)](https://www.typescriptlang.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18%20%2B%20pgvector-4169E1?logo=postgresql&logoColor=white)](https://www.postgresql.org)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)

![Timothy](assets/timothy.png)

**The open-source control plane for your personal AI workforce.** Run your own AI agents. Your infrastructure. Your models. Your rules.

Timothy runs on your hardware and works for you around the clock. Shape agents with their own model, tools and knowledge, hand them real work, and let them chat, research, write code, read your inbox and calendar, and brief you about what matters. They remember who you are across every conversation and deliver results to your phone while you sleep. Every conversation, memory, document, and API key stays on infrastructure you control.

Use any model you want: Anthropic, OpenAI, Amazon Bedrock, GLM, a local Ollama, or any compatible endpoint. Route each kind of work to whichever model does it best, switch anytime from settings, no code changes, no lock-in.

**Status: early, under active development.** 

Alpha releases with prebuilt images are available on the [Releases page](https://github.com/timothy-agent/timothy/releases); expect rough edges and breaking changes between releases.

## Features

| Feature                        | What you get                                                                                                                                                                                                                                                            |
|--------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| **One assistant, every model** | Anthropic, OpenAI, Amazon Bedrock, local models via Ollama, or any compatible provider, all behind one interface. Pick which model handles chat, coding, research, or briefings, and let Timothy fail over to a backup when a provider has a bad day.                   |
| **Give it real work**          | Hand Timothy a task (research a topic, write a report, fix a bug) and it works unattended: plans, executes, verifies its own output, and shows you the result with a full timeline of what it did. Quick tasks skip the ceremony and just get done.                     |
| **Results find you**           | Any task or schedule can deliver its result to Telegram, email, a webhook, or GitHub (a pushed branch or an opened pull request) the moment it finishes, files attached. No checking a dashboard: the answer lands where you already are.                               |
| **It writes code safely**      | Coding tasks run in an isolated sandbox with the toolchains each repo pins (Go, Node, Python, Java, PHP, Ruby, Rust), on their own git branch, with the work verified before you see it. It can also hand the coding work to a CLI agent you already use, Claude Code, Codex, Cursor, opencode, or pi, while keeping review and budgets in your hands. |
| **Your daily briefings**       | Wake up to a digest of your inbox, calendar, and spending, delivered to Telegram or email in your timezone, saying only what actually needs your attention. Schedule any task to run on your clock.                                                                     |
| **Connected to your life**     | Gmail, Google Calendar, Docs, Drive, GitHub, Outlook, IMAP, CalDAV, and any MCP server. Timothy reads them when a task needs it, and asks before doing anything destructive.                                                                                            |
| **Shape your own assistants**  | Create named agents with their own personality, favorite model, and exactly the tools and knowledge they need, nothing more. A briefing agent that reads only your mail and calendar can never touch your code or send a message on your behalf.                        |
| **It remembers you**           | Preferences, projects, and facts you share carry across conversations, and recurring patterns become insights over time. You approve what becomes a standing instruction; noise gets filtered before it ever reaches you.                                               |
| **Your documents, searchable** | Drop in files or URLs; Timothy files them into topic collections and uses them to answer your questions. Your own knowledge base, on your own disk.                                                                                                                     |
| **Nothing gets lost**          | Conversations survive restarts, crashes, and upgrades. Pick up any session exactly where it left off.                                                                                                                                                                   |
| **You control the spend**      | Every model call is priced and logged honestly. Set budgets with alerts, see exactly where the money goes, and route routine work to cheap or free models.                                                                                                              |
| **Private by design**          | Runs entirely on your hardware. Sensitive content like email can be pinned to a local model so it never leaves your network, and API keys live in an encrypted store (or your own Vault / AWS Secrets Manager), never in logs, never in the UI.                         |
| **Talk to it**                 | Optional voice input with fully local speech-to-text. Audio never leaves your machine.                                                                                                                                                                                  |

## A day with Timothy

Things Timothy's own operator actually runs it for:

- **07:00, your phone buzzes.** "Two things need you today: the client call at 14:00 has an unanswered thread from yesterday, and your card was charged twice by the same vendor. The other 14 emails were newsletters." A scheduled briefing read your inbox and calendar, cross-referenced them, and messaged you on Telegram.
- **"Find every receipt from my Portugal trip and total it per currency."** Timothy searches your Gmail, opens each receipt (never trusting a snippet), and reports an itemized breakdown with per-currency totals it computed with a calculator, not vibes.
- **"Research the current EU AI Act timeline and write me a cited summary."** It searches the web, reads primary sources, writes the report to a file, and a verification step checks the artifact exists and cites real URLs before you ever see "done".
- **"Fix the flaky test in my repo."** A coding mission clones the repo into a sandbox, works on its own branch, runs the tests, and opens the result for your review. Your laptop stays untouched.
- **"Remember that Ana owes me EUR 200 from dinner, she'll pay in September."** Weeks later: "Who owes me money?" answers correctly, because facts you tell it persist and stay retrievable.
- **Drop a PDF into the knowledge base.** It lands in the right topic collection automatically, and next week "what did that scaling article say about probabilistic counting?" quotes it back.
- **Every evening at 20:00**, an expense digest lists the day's spending from your inbox; every Monday at 07:00, a week-prep note cross-references your calendar with recent email threads and flags meetings that need preparation.

Each of these is a schedule, a chat message, or a one-line task. No plugins to write, no pipelines to build.

## Architecture

Go microservices behind a single public API, one PostgreSQL database, React web UI. All run via Docker Compose.

| Service      | Role                                                                                          |
|--------------|-----------------------------------------------------------------------------------------------|
| `brain`      | Public API: chat orchestration, agent loop, missions, event-sourced sessions, SSE streaming   |
| `gateway`    | Internal LLM gateway: multi-provider routing, cost ledger                                     |
| `memoryd`    | Internal memory service: pgvector-backed recall                                               |
| `sandboxd`   | Internal service holding the Docker socket: per-mission sandbox containers                    |
| `web`        | React + Tailwind interface: chat, missions, usage, settings                                   |
| `searxng`    | Internal metasearch backend for the search_web tool                                           |
| `markitdown` | Internal Python sidecar: file→markdown conversion                                             |
| `whisper`    | Internal Python sidecar: local speech-to-text for the web mic button (opt-in, off by default) |
| `pdfgen`     | Internal Python sidecar: markdown→PDF via Typst, powers mission PDF export                    |

Plus Postgres (18 + pgvector), internal only, no host port. Migrations are embedded in each Go binary and applied automatically at startup; there's no separate migrate command. Every Go service exposes `GET /health` and `GET /metrics`; brain's `/metrics`, the only one on a published port, requires `Authorization: Bearer $TIMOTHY_METRICS_TOKEN`.

Sessions are an append-only event log: every turn, tool run, and compaction is an immutable event, so conversations survive crashes mid-stream and replay exactly as they happened.

**Published ports** (everything else is compose-internal):

| Port   | What                         |
|--------|------------------------------|
| `3300` | Web UI                       |
| `8300` | Brain (public API)           |
| `3301` | Vite dev server (`make dev`) |

Both published ports serve plain HTTP and are meant for a trusted LAN. For any exposure beyond that, put a reverse proxy in front that terminates TLS: the API token travels in an `Authorization` header on every request and is stored in the browser's `localStorage`, so over plain HTTP anyone on the path can read it. The web UI ships a Content-Security-Policy, `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, and `Referrer-Policy: strict-origin-when-cross-origin`; a proxy that rewrites response headers should preserve them.

## Quick start (prebuilt images)

The fastest way to run Timothy: no Go/Node toolchain, no build step, just Docker and the released images.

```sh
curl -fsSL https://raw.githubusercontent.com/timothy-agent/timothy/main/deploy/release/install.sh | sh
```

The installer resolves the newest release, installs into `~/timothy` (override with `TIMOTHY_HOME=/some/dir`), generates a `.env` with fresh secrets (`POSTGRES_PASSWORD`, `TIMOTHY_MASTER_KEY`, `TIMOTHY_API_TOKEN`), pulls the images, starts the stack, and prints a magic sign-in link once the web UI is up. Open the link: the web UI signs in automatically.

Prefer to inspect scripts before running them? Every release also ships `install.sh` as an asset: download it from the [releases page](https://github.com/timothy-agent/timothy/releases), read it, then `sh install.sh`.

Everything else about running Timothy lives in the docs: [timothy-agent.github.io/docs](https://timothy-agent.github.io/docs/).

## Documentation

| Topic | Where |
|-------|-------|
| Requirements, quick start, build from source | [Install](https://timothy-agent.github.io/docs/install/) |
| Upgrading, pending schema changes | [Upgrade](https://timothy-agent.github.io/docs/install/upgrade/) |
| Backups and restoring onto a fresh host | [Backup and restore](https://timothy-agent.github.io/docs/install/backup-and-restore/) |
| Sign in, welcome wizard, first chat, first mission | [First run](https://timothy-agent.github.io/docs/first-run/) |
| Agents, routes, missions, memory, knowledge, automations | [Concepts](https://timothy-agent.github.io/docs/concepts/) |
| Gmail, GitHub, Telegram and the rest | [Connectors and channels](https://timothy-agent.github.io/docs/connectors/) |
| Every settings tab and field | [Settings reference](https://timothy-agent.github.io/docs/settings/) |
| Something broke | [Troubleshooting](https://timothy-agent.github.io/docs/troubleshooting/) |

## Local development

The Go toolchain runs fully containerized; no host Go install required.

```sh
make build   # compile everything
make test    # unit tests
make vet     # go vet
make lint    # golangci-lint
```

Frontend development with hot reload:

```sh
make dev   # Vite dev server on :3301, proxies /v1 to brain
```

`make test-integration`, `make canary`, and `make kb-eval` (retrieval eval harness, `scripts/kb-eval/`) need the compose stack up (`make up` first).

Design decisions are documented as `D-0XX` markers in code comments next to the code they explain.

## License

[AGPL-3.0](LICENSE)
