# Timothy Threat Model

Status: alpha-exit review, 2026-08-30; dispositions refreshed against
the code on 2026-10-07. Companion to `SECURITY.md` (which
covers reporting). This document names the trust boundaries, the assets
worth protecting, the attack surfaces, and the disposition of each known
risk: mitigated, accepted, or open (tracked by issue where one exists).

## System and trust posture

Timothy is single-operator by design. One person holds the API token and
runs the instance for themselves. There is no multi-user privilege
boundary inside one instance, and the schema is single-tenant on purpose.
The token that reaches chat is the same token that administers providers,
routes, and secrets: chat access equals full administrative access. This
is intentional for the single-operator model, not a defect, but it means
the token is the whole security perimeter of the control plane.

The system runs as Docker Compose services, or as the Helm chart's
pods on Kubernetes, sharing one PostgreSQL database:

- `brain` publishes the only externally reachable API (`:8300`), plus a
  static web UI (`:3300`).
- `gateway`, `memoryd`, `sandboxd`, `searxng`, `markitdown`, `ocr`,
  `whisper`, and `pdfgen` publish no host ports and are internal-only.
- On Compose `sandboxd` holds the Docker socket and lives on its own
  network (`timothy-sandbox`), reachable only by `brain`. On Kubernetes
  it holds no socket: it creates mission pods through the API server
  with a Role scoped to the sandbox namespace, and NetworkPolicies
  replace the Compose networks.

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
- Host integrity: on Compose, sandboxd's Docker socket is
  root-equivalent on the host. On Kubernetes, sandboxd's service
  account token creates and execs into pods in the sandbox namespace,
  and the shared workspace volume holds every mission's files.

## Attack surfaces and dispositions

### Public API and authentication

Every `/v1` route requires the bearer token, validated with a
constant-time compare and failing closed when the token is unset. The
routes outside the API token are `/health`, `/metrics` (its own token,
below), the OAuth callback (which authenticates via a single-use
expiring state token because an identity provider redirects a browser
to it), and `POST /hooks/{trigger_id}` (each delivery is checked
against its trigger's HMAC-SHA256 key in constant time, with a
timestamp skew bound, a body cap, and a per-trigger rate limit;
`internal/brain/api/hooks.go`).

- **Mitigated:** token validation (constant-time, fail-closed), complete
  route coverage, gateway admin routes reachable only through brain's
  authenticated proxy allowlist.
- **Mitigated:** `/metrics` on brain's published port requires a bearer
  token, `TIMOTHY_METRICS_TOKEN`, separate from the API token
  (`httpserver.Server.ProtectMetrics`, D-100). The compare is
  constant-time, and an unset token fails closed with 503. Issue #438.
- **Accepted:** single token, no roles. Correct for single-operator;
  documented here so it is a deliberate choice, not an oversight.
- **Accepted:** no built-in TLS. Any exposure beyond a trusted LAN
  requires an external reverse proxy terminating TLS, and README.md
  states this (issue #432). Without it the token travels in cleartext.

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
- **Mitigated:** GCM binds each row's `ref_name` as additional data
  (`internal/secretstore/cipher.go`, D-105), so ciphertext swapped
  between rows fails to open. Rows sealed before D-105 are re-sealed on
  first read (`Store.openDB`) and by a startup sweep
  (`Store.ResealLegacy`, D-114). Issue #433.
- **Residual:** the legacy no-AAD open path (`openLegacy`) stays as a
  fallback until the sweep is known to have run on every instance.
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
- **Mitigated:** webhook destinations (`destinations.WebhookAdapter`),
  the MCP connector endpoint, the mission webhook notifier, and the
  channel client all dial through `netguard.Guard` (wired in
  `cmd/brain/main.go`), so a URL that resolves to a blocked range is
  refused. An internal receiver needs an explicit host in the
  `outbound_host_allowlist` setting, which is empty by default. Issue
  #431.
- **Accepted:** fixed-vendor connector clients (GitHub/Google/Microsoft)
  and sidecar clients (markitdown/ocr/pdfgen/whisper/searxng) are
  unguarded because their addresses are operator env, never model
  input. CalDAV and IMAP/SMTP connectors also skip netguard; their
  endpoints come from operator connector config through the
  authenticated API. CalDAV deliberately permits cleartext basic auth
  to loopback for test fixtures; documented as a small accepted hole.

### Prompt injection and tool actions

The agent loop consumes untrusted content and can call tools. Memories
are fenced with a `trust="data"` wrapper and close-tag escaping (D-011),
telling the model they are data, not instructions.

- **Mitigated:** memory content fencing (single-sourced, tolerant of
  forged close tags).
- **Mitigated:** every tool result that is not marked `Trusted` on its
  `tools.Tool` value is fenced the same way before the model sees it
  (`fenceUntrusted` in `internal/brain/loop/agent.go`, one fence in
  `internal/platform/trustfence`, D-107 and D-109). That covers web
  pages, search results, mail bodies, KB passages, converted documents,
  and remote MCP output. Trust is opt-in per tool, so a new tool is
  fenced by default. Issue #430.
- **Open (no issue):** the fence is advice to the model, not a Go
  check. `fetch_url`, `search_web`, `search_kb`, and `read_kb` stay
  permission-exempt, so injected content that the model obeys anyway
  still has a prompt-free read-then-exfiltrate chain (`fetch_url` to a
  public URL carrying data in the query string, which netguard does
  not stop because the destination is a real external host). Revoking
  those exemptions was scoped out of #430. `shell` output is shown
  unfenced; it only counts as untrusted for memory writes (D-128).
- **Mitigated:** action tools that change state or leave the system
  (`shell`, `push_mission_branch`, `deliver`) are not permission-exempt
  and prompt unless an operator-authored agent has pre-granted them in
  its approval allowlist. `write_file` is exempt because it is confined
  to its root by construction (relative paths only, `..` rejected).
  Turn-ending sentinels are pure argument parsing.

### Mission sandbox

sandboxd holds the Docker socket but runs read-only, all-caps-dropped,
no-new-privileges, distroless, network-isolated. Its API never accepts
container names, images, mounts, or arbitrary env; the mission ID is
shape-validated before any Docker call. Mission containers run as an
unprivileged user with capped memory, CPU, PIDs, and OOM sacrifice bias,
a deny-by-default env allowlist, and value-length limits. Each mission
container mounts only its own workspace directory, never the shared
workspace root (`Manager.missionMount` in `internal/sandboxd`). Their
rootfs is read-only (D-106), with writable space only on that mission
directory, the executor state volume, the optional toolchain and
package cache volumes (D-125, D-131), and size-bounded tmpfs at `/tmp`
and the sandbox HOME; nofile and fsize ulimits bound fd and single-file-size exhaustion,
and Docker's default seccomp profile applies, pinned by never setting a
`seccomp=` security option. Shell commands are
scored by a classifier that treats anything it cannot parse
(substitutions, `eval`, `sh -c`) as destructive and prompts. `write_file`
resolves symlinks before writing and rejects paths outside the workspace.
Mission file downloads force octet-stream with nosniff and collapse
not-found and out-of-bounds into the same 404.

- **Mitigated:** sandboxd hardening, narrow unauthenticated-but-
  unreachable API, per-mission resource caps, read-only rootfs with
  bounded tmpfs, nofile/fsize ulimits, default seccomp profile, env
  allowlist, symlink-safe writes, download containment (issue #437).
- **Mitigated:** per-mission workspace isolation. `Manager.missionMount`
  mounts the volume narrowed by Subpath to `missions/<kind>/<id>` and
  rejects a workdir that names another mission, so one mission cannot
  read or clobber another's files. Issue #749.
- **Mitigated (D-132):** several instances can share one Docker daemon.
  Each sandboxd labels its containers `timothy.owner` with its own
  compose project name, and list, remove and exec act only on
  containers carrying that label, so one instance's orphan sweep cannot
  delete another instance's live sandboxes. sandboxd refuses to start
  or exec in another instance's container. A container from before
  D-132 has no owner label: exec still reuses it for its own mission,
  but no sweep removes it; clear those by hand. Instances not run under
  Compose share a fixed default owner, so give each instance its own
  compose project. Issue #1036.
- **Accepted:** the shell classifier is a best-effort regex, not a
  boundary; the container is the boundary. Stated in code and here.
- **Accepted (D-129):** in a session with a registered mission sandbox,
  the native worker's shell guard relaxes to match what the delegated
  executors already do in the same container (claude `dontAsk`, codex
  bypass, opencode `allow`). Language package installs (pip, gem,
  cargo, global npm) no longer prompt; env files inside the worktree
  can be read and written; committed repo files that look like
  credentials or keys (`.npmrc`, `config/secrets.yml`, test `*.pem`)
  can be read; read-only commands can name `/usr`, `/opt`, `/tmp`,
  `/etc/os-release` and the HOME subdirs `.mise`, `.cache` and
  `.local`. Any command can write under `/tmp`, which is the
  container's own size-bounded tmpfs, not a host path, and where the
  delegated executors already write; paths are cleaned first, so
  `/tmp/../etc` stays denied.
  The container runs as uid 65534 on a read-only rootfs and
  never receives host secrets, so these reach nothing the worker could
  not already reach. The one secret the container does hold, the
  claude CLI auth state under `/home/sandbox/.claude`, stays excluded:
  HOME itself is not a read path, so a recursive read of HOME cannot
  walk into it, and `.claude` is denied explicitly as well. System
  package managers, sudo, docker, pipe-to-shell, ssh keys, home
  dotfiles, other paths outside the worktree, and every chat or host
  session keep the original rules.
- **Accepted (D-131):** package caches (npm, composer, pip, uv, Go,
  Maven, Gradle, yarn, bun) and mise toolchains live on the optional
  `sandbox-caches` and `sandbox-toolchains` volumes, shared read-write
  by every mission. A mission running a hostile repo can plant cache
  entries that a later mission's install reads. npm and Go check cached
  packages against the lockfile's integrity hash or `go.sum`, so a
  planted entry fails the install instead of running; composer, pip
  without `--require-hashes`, Maven and Gradle check less and can serve
  it. Remove the `sandbox-caches` mount from sandboxd to disable
  sharing: each mission then caches in its own workspace dir.
- **Accepted:** the executor state volume (`/home/sandbox/.claude`, the
  claude CLI auth state) is mounted read-write in every mission
  container. The native worker's shell guard denies it, but code a
  hostile repo runs in the container (build scripts, package hooks) or
  a delegated executor can read it.
- **Accepted:** sandbox containers on the default bridge reach the
  bridge gateway address (typically `172.17.0.1`) and through it any
  port the host publishes, including brain's `:8300`. The API token and
  the metrics token are never given to the sandbox, so this is
  defense-in-depth rather than an open door. Replacing bridge networking
  was scoped out of #437; the containment boundary is the container,
  and outbound internet access is a requirement (a coding mission runs
  `pip install` / `npm install`).

### Mission sandbox on Kubernetes

The Helm chart (`deploy/helm/timothy`) runs sandboxd with the
kubernetes backend (D-153, D-154). sandboxd holds no Docker socket;
its service account has a namespaced Role limited to pods (get, list,
watch, create, delete), `pods/exec` (create), `pods/log` (get),
resourcequotas (get, list) and reading its own namespace. A mission pod
runs as uid 65534 with a read-only rootfs, all capabilities dropped,
the RuntimeDefault seccomp profile, no service account token, memory
and CPU limits, size-bounded memory-backed `/tmp` and HOME, and an
`activeDeadlineSeconds` TTL. The workspace is one ReadWriteMany
volume; brain mounts the root and each mission pod mounts only its
`missions/<kind>/<id>` subPath (D-155), the same boundary as the
Docker mount.

- **Mitigated:** the Docker socket and the bridge-gateway path listed
  above do not exist on Kubernetes. The chart's NetworkPolicies default
  to deny in the release namespace, allow only the service graph
  (web to brain, brain to the internal services, gateway and memoryd to
  postgres), and give mission pods DNS plus TCP 443 to the internet
  with private ranges, CGNAT and the cloud metadata address
  (`169.254.169.254`) excluded. A mission pod cannot reach brain, the
  database, sandboxd, the API server or the node network. Verified on
  kind by exec probes from a sandbox pod.
- **Mitigated:** the optional separate sandbox namespace
  (`sandbox.namespace`) enforces Pod Security `restricted`, a
  ResourceQuota, a LimitRange and its own default deny; a RuntimeClass
  (`gvisor`, `kata`) and a dedicated node pool are a values change.
- **Accepted:** NetworkPolicy isolation requires a CNI that enforces
  policies. On a cluster without enforcement every policy above is
  inert and a mission pod can reach the other services. Documented as
  a requirement on the docs site.
- **Accepted:** the subPath mount is a boundary, not encryption. Anyone
  who can mount the workspace volume or read the node's filesystem
  sees every mission. The RWX filesystem (EFS, Azure Files, Filestore)
  is as private as its own access controls.
- **Accepted:** `pods/exec` is the exec transport. A principal that
  obtains sandboxd's service account token can exec into any sandbox
  pod of that namespace; the Role does not reach other namespaces or
  other workloads. sandboxd labels its pods `timothy.owner=<release>`
  and acts only on its own (D-132 carried over).
- **Accepted:** the TTL and the orphan sweep rely on brain. A pod
  survives a brain outage until `activeDeadlineSeconds` ends it.

### Sidecars

markitdown, ocr, whisper, and pdfgen are internal-only FastAPI services
that parse attacker-influenceable bytes. markitdown loads no third-party
converters. pdfgen writes document content to separate files and pulls it
in with Typst `read()` rather than interpolating, and escapes titles, so
Typst injection is handled; it shells out with an argv list and no shell.

- **Mitigated:** markitdown converter isolation, pdfgen content/argument
  separation, URL fetch kept in brain behind netguard (sidecars convert
  bytes only).
- **Mitigated:** each sidecar caps its request body (`MAX_BODY_BYTES`
  in each `*-svc/main.py`) and returns 413, refusing an oversized
  Content-Length before reading any body. pdfgen's Typst compile runs
  with a 90 s timeout and returns 504 past it, and its stderr is
  scrubbed of temp paths (`_scrub_stderr`). markitdown, ocr, pdfgen,
  whisper, and searxng carry `mem_limit`, `cpus`, and `pids_limit` in
  both compose files. Issue #434.

### Web UI

The SPA is served same-origin and proxies `/v1` to brain, so there is no
CORS surface. Model markdown is rendered through `rehypeRaw` then
`rehypeSanitize` with the unmodified default schema, which strips scripts,
event handlers, and `javascript:` URLs. Attachment MIME types come from a
server-side sniff against an image/audio/video allowlist, never the client
header.

- **Mitigated:** same-origin proxy, default-schema sanitization,
  server-side MIME sniffing with allowlist.
- **Mitigated:** nginx sends a Content-Security-Policy (`script-src
  'self'`, `object-src 'none'`, `frame-ancestors 'none'`),
  `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, and
  `Referrer-Policy` on every response
  (`web/nginx/default.conf.template`). Mermaid, whose SVG reaches the
  DOM through `dangerouslySetInnerHTML` outside the sanitize pipeline,
  is initialized with `securityLevel: 'strict'` (`MermaidBlock.tsx`,
  pinned by its test). Issue #432.
- **Accepted:** the API token stays in `localStorage`, so an XSS that
  gets past the CSP is a full token compromise. Moving it was scoped
  out of #432. HSTS is left to the TLS-terminating proxy.

### Data at rest and recovery

PostgreSQL is unpublished. Only the secret columns are encrypted;
transcripts, memories, and KB content are plaintext in the volume.

- **Accepted for now:** no database-level encryption at rest. Single-
  operator, single-host; the mitigation is host access control.
- **Mitigated:** `scripts/backup-db.sh` writes rotated, gzipped
  whole-database dumps outside the `pgdata` volume (cron-able, and it
  refuses a dump missing the `secrets` table); the restore procedure is
  in README.md under "Restoring onto a fresh host". Issue #436.
- **Accepted for now:** running the backup on a schedule and keeping a
  copy off-host is the operator's responsibility; the repo ships the
  tooling, not the cron entry. `TIMOTHY_MASTER_KEY` must be backed up
  separately or the dump's secrets stay unreadable.

### Build and release

Images publish to GHCR with per-job scoped write permissions. The release
compose digest-pins both third-party images (searxng, postgres). Every
freshly built image carries a GitHub build provenance attestation pushed
to the registry alongside it, verifiable with `gh attestation verify
oci://ghcr.io/timothy-agent/timothy-<service>:<version> --repo
timothy-agent/timothy`. Release notes carry SHA-256 checksums for the
published compose and env assets, and `install.sh` downloads them into a
temp dir, verifies them against the release's `checksums.txt`, and
refuses to install on a mismatch.

- **Mitigated:** third-party image digest pins, build provenance
  attestations on published images, checksummed release assets verified
  by the installer before use. Issue #435.
- **Accepted:** Timothy's own images stay tagged by version in the
  release compose rather than digest-pinned, because the tag is
  published by this repo's own workflow and every digest is attested;
  digest-pinning them would mean rewriting the compose file per release
  for no added guarantee. `checksums.txt` is served by the same GitHub
  release as the assets, so it binds an asset to its release, not to a
  signing key; it stops a truncated or swapped asset, not a compromised
  GitHub account. `install.sh` itself is unverified at fetch time (it is
  the verifier).
- **Open:** no SBOM is generated per image. Future work, scoped out of
  #435.

## Open-risk summary

| Risk | Severity | Issue |
|------|----------|-------|
| Exempt read/fetch tools give injected content a prompt-free exfil path; the fence (#430) is advisory | High | none (exemptions scoped out of #430) |
| No per-image SBOM (rest of release integrity mitigated) | Low | none (scoped out of #435) |

Issues #430, #431, #432, #433, #434, #435, #437, #438, and #749 are
closed; their mitigations are described in the sections above.

Accepted risks (single-operator posture, documented deliberately): one
token equals administrative access; no TLS without an external proxy;
the API token lives in `localStorage`; the shell classifier is
advisory; no database-level encryption at rest on a single host;
fixed-vendor, sidecar, CalDAV, and IMAP clients skip netguard by
design; on Compose, sandboxes reach host-published ports through the
bridge gateway; package caches, toolchains, and the executor auth state
are shared across missions; on Kubernetes, isolation of mission pods
depends on a policy-enforcing CNI and the shared workspace volume's
own access controls.
