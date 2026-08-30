# Timothy Threat Model

Status: alpha-exit review, 2026-08-30. Companion to `SECURITY.md` (which
covers reporting). This document names the trust boundaries, the assets
worth protecting, the attack surfaces, and the disposition of each known
risk: mitigated, accepted, or open (tracked by issue).

## System and trust posture

Timothy is single-operator by design. One person holds the API token and
runs the instance for themselves. There is no multi-user privilege
boundary inside one instance, and the schema is single-tenant on purpose.
The token that reaches chat is the same token that administers providers,
routes, and secrets: chat access equals full administrative access. This
is intentional for the single-operator model, not a defect, but it means
the token is the whole security perimeter of the control plane.

The system runs as Docker Compose services sharing one PostgreSQL
database:

- `brain` publishes the only externally reachable API (`:8300`), plus a
  static web UI (`:3300`).
- `gateway`, `memoryd`, `sandboxd`, `searxng`, `markitdown`, `whisper`,
  and `pdfgen` publish no host ports and are internal-only.
- `sandboxd` holds the Docker socket and lives on its own network
  (`timothy-sandbox`), reachable only by `brain`.

## Trust boundaries

1. **Internet to brain.** The single trusted operator crosses this with a
   bearer token. Everything else on this boundary is untrusted.
2. **Brain to internal services.** gateway, memoryd, and sandboxd have no
   authentication. The boundary is the network: they are unpublished and
   only brain (and, for sandboxd, brain alone) can reach them. Anything
   that gains brain's network position without brain's authorization
   checks can call them directly.
3. **Brain to model, and model output back into the loop.** The model is
   not trusted. Its output can request tool calls. This is the boundary
   that prompt injection attacks.
4. **Mission sandbox to host.** Model-authored code runs in per-mission
   containers. The container is the real isolation boundary; everything
   above it (shell command classification, path checks) is a UX
   speed-bump, not a wall.
5. **Untrusted content to prompt.** Fetched web pages, search results,
   mail bodies, KB documents, and converted attachments all enter the
   prompt as data but are read by a model that also holds tools.

## Assets

- Provider API keys, OAuth grants, signing keys, bot tokens: stored in
  the secret store by `credential_ref`, encrypted at rest (db backend) or
  held in vault/AWS Secrets Manager (external backends). The master key
  is the root of trust and the one credential still in env.
- Session transcripts (`session_events`), extracted memories, and KB
  content: the richest personal-data stores, plaintext in `pgdata`.
- The API token: a single shared bearer credential; compromise is total.
- Host integrity: sandboxd's Docker socket is root-equivalent on the
  host.

## Attack surfaces and dispositions

### Public API and authentication

Every `/v1` route requires the bearer token, validated with a
constant-time compare and failing closed when the token is unset. The
only unauthenticated routes are `/health`, `/metrics`, and the OAuth
callback (which authenticates via a single-use expiring state token
because an identity provider redirects a browser to it).

- **Mitigated:** token validation (constant-time, fail-closed), complete
  route coverage, gateway admin routes reachable only through brain's
  authenticated proxy allowlist.
- **Open:** `/metrics` is unauthenticated on the published port and
  exposes route-level telemetry. Tracked in issue #438.
- **Accepted:** single token, no roles. Correct for single-operator;
  documented here so it is a deliberate choice, not an oversight.
- **Transport:** no built-in TLS. Any exposure beyond localhost requires
  an external reverse proxy terminating TLS. Called out in the web
  hardening work (issue #432); the token travels cleartext without it.

### Secret handling

Secrets are AES-256-GCM encrypted under a single master key (db backend),
or delegated to vault / AWS Secrets Manager. Raw values never appear in
API responses (only ref names and referents), and deletion is refused
while a ref is still referenced. Git tokens and the Telegram bot token
are scrubbed from subprocess output and error strings at their known
leak points.

- **Mitigated:** encryption at rest for secret columns, no-plaintext-in-
  responses, referential-integrity delete guard, redirect-drop on the
  vault HTTP client, sandbox containers never receive brain's env.
- **Open:** GCM seals with nil additional data, so `ref_name` is not
  bound to its ciphertext; a DB-write attacker without the key could swap
  ciphertext between rows. One-line hardening, tracked in issue #433.
- **Residual:** redaction is per-site, not a central logging filter. A
  new code path that logs a resolved secret has nothing catching it.
  Noted as a coding invariant (secrets by ref only) rather than a
  separate issue.

### Outbound requests (SSRF)

`netguard` resolves the target host itself, checks every resolved IP
against blocked ranges (loopback, RFC1918/ULA, link-local including cloud
metadata, CGNAT, non-global-unicast), then dials the vetted IP, closing
the DNS-rebind window, and re-enters per redirect hop. `fetch_url` and KB
URL ingest go through it and strip userinfo.

- **Mitigated:** the two model-facing URL paths (`fetch_url`, KB ingest).
- **Open:** webhook destinations, the MCP connector endpoint, and the
  mission webhook notifier use plain HTTP clients with no netguard. The
  model can pick a webhook destination via `deliver`, and brain sits on
  both networks where the unauthenticated internal services live, so an
  unguarded URL is an in-network request primitive. Tracked in issue
  #431.
- **Accepted:** fixed-vendor connector clients (GitHub/Google/Microsoft)
  and sidecar clients (markitdown/pdfgen/whisper/searxng) are unguarded
  because their addresses are operator env, never model input. CalDAV
  deliberately permits cleartext basic auth to loopback for test
  fixtures; documented as a small accepted hole.

### Prompt injection and tool actions

The agent loop consumes untrusted content and can call tools. Memories
are fenced with a `trust="data"` wrapper and close-tag escaping (D-011),
telling the model they are data, not instructions.

- **Mitigated:** memory content fencing (single-sourced, tolerant of
  forged close tags).
- **Open (highest severity):** no equivalent fencing for web pages,
  search results, mail bodies, KB chunks, or converted attachments, while
  `fetch_url`, `search_web`, `search_kb`, and `read_kb` are permission-
  exempt. That gives injected content a prompt-free read-then-exfiltrate
  chain (`fetch_url` to a public URL carrying data in the query string,
  which netguard does not stop because the destination is a real external
  host). The fencing mechanism already exists; it just is not applied to
  the higher-volume channels. Tracked in issue #430.
- **Mitigated:** action tools that change state or leave the system
  (`shell`, `write_file`, `push_mission_branch`, `deliver`) are not
  permission-exempt and prompt unless an operator-authored agent has
  pre-granted them in its approval allowlist. Turn-ending sentinels are
  pure argument parsing.

### Mission sandbox

sandboxd holds the Docker socket but runs read-only, all-caps-dropped,
no-new-privileges, distroless, network-isolated. Its API never accepts
container names, images, mounts, or arbitrary env; the mission ID is
shape-validated before any Docker call. Mission containers run as an
unprivileged user with capped memory, CPU, PIDs, and OOM sacrifice bias,
a deny-by-default env allowlist, and value-length limits. Shell commands
are scored by a classifier that treats anything it cannot parse
(substitutions, `eval`, `sh -c`) as destructive and prompts. `write_file`
resolves symlinks before writing and rejects paths outside the workspace.
Mission file downloads force octet-stream with nosniff and collapse
not-found and out-of-bounds into the same 404.

- **Mitigated:** sandboxd hardening, narrow unauthenticated-but-
  unreachable API, per-mission resource caps, env allowlist, symlink-safe
  writes, download containment.
- **Accepted:** the shell classifier is a best-effort regex, not a
  boundary; the container is the boundary. Stated in code and here.
- **Open:** mission containers lack read-only rootfs, pinned seccomp, and
  ulimits, and share one read-write workspace volume across missions, so
  missions are not isolated from each other's files. Sandbox containers on
  the default bridge can reach host-published ports (including brain's
  `:8300`, whose unauthenticated `/metrics` is then reachable); the token
  is never given to the sandbox, so this is defense-in-depth. Tracked in
  issue #437.

### Sidecars

markitdown, whisper, and pdfgen are internal-only FastAPI services that
parse attacker-influenceable bytes. markitdown loads no third-party
converters. pdfgen writes document content to separate files and pulls it
in with Typst `read()` rather than interpolating, and escapes titles, so
Typst injection is handled; it shells out with an argv list and no shell.

- **Mitigated:** markitdown converter isolation, pdfgen content/argument
  separation, URL fetch kept in brain behind netguard (sidecars convert
  bytes only).
- **Open:** all three read unbounded request bodies (bounded only by
  brain's caller-side caps); pdfgen's Typst compile has no timeout and
  returns stderr (temp paths) to callers; no sidecar carries compose-level
  memory/CPU/PID limits, so a pathological file can hang a worker or OOM
  the host. Tracked in issue #434.

### Web UI

The SPA is served same-origin and proxies `/v1` to brain, so there is no
CORS surface. Model markdown is rendered through `rehypeRaw` then
`rehypeSanitize` with the unmodified default schema, which strips scripts,
event handlers, and `javascript:` URLs. Attachment MIME types come from a
server-side sniff against an image/audio/video allowlist, never the client
header.

- **Mitigated:** same-origin proxy, default-schema sanitization,
  server-side MIME sniffing with allowlist.
- **Open:** no CSP, X-Frame-Options, or HSTS, with the API token in
  `localStorage`, so any successful XSS is a full token compromise. The
  mermaid `dangerouslySetInnerHTML` path renders model-authored diagram
  source outside the sanitize pipeline and needs its security level
  pinned to strict. Tracked in issue #432.

### Data at rest and recovery

PostgreSQL is unpublished. Only the secret columns are encrypted;
transcripts, memories, and KB content are plaintext in the volume.

- **Accepted for now:** no database-level encryption at rest. Single-
  operator, single-host; the mitigation is host access control.
- **Open:** no backup tooling or restore doc in the repo, against an
  append-only full-transcript store. Tracked in issue #436.

### Build and release

Images publish to GHCR with per-job scoped write permissions. The release
compose digest-pins the one third-party image (searxng).

- **Open:** Timothy images and postgres are pinned by mutable tag; there
  is no image signing, provenance, or SBOM; `install.sh` downloads release
  assets without checksum verification (secrets it generates locally use a
  CSPRNG with adequate entropy). Tracked in issue #435.

## Open-risk summary

| Risk | Severity | Issue |
|------|----------|-------|
| Unfenced untrusted content plus exempt read/fetch tools (injection to exfil) | High | #430 |
| Operator-URL outbound paths bypass netguard | High | #431 |
| No CSP/frame headers; token in localStorage; mermaid SVG path | Medium | #432 |
| Secret-store AES-GCM without AAD | Medium | #433 |
| Sidecar input limits, Typst timeout, resource caps | Medium | #434 |
| Release integrity (signing, digest pins, checksums) | Medium | #435 |
| No DB backup tooling or restore doc | Medium | #436 |
| Mission sandbox hardening round 2 | Low | #437 |
| Unauthenticated `/metrics` on the public port | Low | #438 |

Accepted risks (single-operator posture, documented deliberately): one
token equals administrative access; no TLS without an external proxy; the
shell classifier is advisory; no database-level encryption at rest on a
single host; fixed-vendor and sidecar clients skip netguard by design.
