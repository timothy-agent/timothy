GO_IMAGE   := golang:1.26.9
LINT_IMAGE := golangci/golangci-lint:v2.12.2
COMPOSE    := docker compose -f deploy/docker-compose.yml

# Local web builds get the same version/sha the release pipeline
# injects, so the sidebar never falls back to package.json's stale
# version and an "unknown" sha. -dev marks an image as locally built;
# ?= lets an explicit environment override win (the release path).
export GIT_SHA     ?= $(shell git rev-parse --short HEAD 2>/dev/null)
export APP_VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null)-dev

# Go toolchain runs containerized: no host Go install required, same
# version everywhere. Named volumes cache modules and builds.
GO_RUN := docker run --rm -v $(CURDIR):/src -w /src \
	-v timothy-go-mod:/go/pkg/mod -v timothy-go-cache:/root/.cache/go-build \
	-e GOFLAGS=-buildvcs=false $(GO_IMAGE)

.PHONY: build test test-integration test-live vet lint tidy skills-validate up down logs \
	brain gateway memoryd web markitdown pdfgen ocr sandboxd dev canary canary-coding canary-two-unit canary-research canary-executor canary-impossible canary-onboarding test-scripts canary-ecosystems kb-eval sandbox-image sandbox-smoke

build:
	$(GO_RUN) go build ./...

test:
	$(GO_RUN) go test -race ./...

# Needs the compose stack up; reads POSTGRES_PASSWORD from deploy/.env
# via --env-file (values never enter the make output). -count=1: test
# caching must never mask a database-state change. -p 1: packages share
# one database; a package's intentionally-invalid provider fixture row
# can fail every concurrent Store.Load in other packages, so run
# sequentially.
test-integration:
	docker run --rm -v $(CURDIR):/src -w /src \
		-v timothy-go-mod:/go/pkg/mod -v timothy-go-cache:/root/.cache/go-build \
		-e GOFLAGS=-buildvcs=false --network timothy_timothy \
		--env-file deploy/.env \
		$(GO_IMAGE) sh -c 'DATABASE_URL="postgres://timothy:$${POSTGRES_PASSWORD}@postgres:5432/timothy" go test -race -count=1 -tags integration -p 1 ./internal/...'

# Streams one real completion per provider whose credentials are in
# the calling environment; absent providers skip.
test-live:
	docker run --rm -v $(CURDIR):/src -w /src \
		-v timothy-go-mod:/go/pkg/mod -v timothy-go-cache:/root/.cache/go-build \
		-e GOFLAGS=-buildvcs=false \
		-e ANTHROPIC_API_KEY -e ANTHROPIC_TEST_MODEL \
		-e OPENAICOMPAT_TEST_BASE_URL -e OPENAICOMPAT_TEST_API_KEY -e OPENAICOMPAT_TEST_MODEL \
		$(GO_IMAGE) go test -race -tags live -v -run TestLive ./internal/gateway/provider/

vet:
	$(GO_RUN) go vet ./...

lint:
	docker run --rm -v $(CURDIR):/src -w /src \
		-v timothy-go-mod:/go/pkg/mod -v timothy-lint-cache:/root/.cache \
		-e GOFLAGS=-buildvcs=false $(LINT_IMAGE) golangci-lint run

tidy:
	$(GO_RUN) go mod tidy

# Validates skill packs. With the stack up, the embedding-similarity
# check runs against the gateway; otherwise it is skipped with a note.
skills-validate:
	docker run --rm -v $(CURDIR):/src -w /src \
		-v timothy-go-mod:/go/pkg/mod -v timothy-go-cache:/root/.cache/go-build \
		-e GOFLAGS=-buildvcs=false --network timothy_timothy \
		-e GATEWAY_URL=http://gateway:8081 \
		$(GO_IMAGE) go run ./cmd/skills-validate -dir skills

# sandbox-image first: sandboxd is mandatory infrastructure and fails
# to boot (NewManager errors on a missing image) without it — a fresh
# clone's first `make up` must not crash-loop waiting on a manual step.
up: sandbox-image
	$(COMPOSE) up -d --build

# Per-service rebuild+restart for when only one service changed:
#   make brain / make gateway / make memoryd / make web / make markitdown / make pdfgen / make ocr / make whisper / make sandboxd
# Rolling brain and sandboxd separately: restart sandboxd first — the
# API between them is additive-only, so an older brain against a newer
# sandboxd (or vice versa, briefly) stays compatible either order, but
# sandboxd-first avoids brain's own restart racing against sandboxd's
# health check on its way up.
brain gateway memoryd web markitdown pdfgen ocr whisper sandboxd:
	$(COMPOSE) up -d --build $@

# Vite dev server with hot reload on :3301 (proxies /v1 to brain).
dev:
	$(COMPOSE) --profile dev up -d web-dev

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f

# Golden-mission regression gate: runs one explore mission end-to-end
# against the live stack and asserts it completes unattended (no parks,
# harness-verified artifacts, bounded turns). Needs the stack up.
canary:
	./scripts/canary-mission.sh

# Same gate for the coding path: worktree provisioning, LLM review,
# artifact verified inside the worktree. Needs the stack up and
# `make sandbox-image` run first — mission shell calls fail opaquely
# without it.
canary-coding:
	./scripts/canary-coding.sh

# Same coding gate with a two-file goal planned as two units (D-096,
# issue #524): asserts exactly one review round ran once both units were
# harness-passed and both artifacts landed in the worktree.
canary-two-unit:
	CANARY_TWO_UNIT=1 ./scripts/canary-coding.sh

# Same gate for research work: the goal requires current web
# information and a cited markdown report, so it exercises
# web_search/web_fetch and the citations check that the trivial
# lookup goals in canary-mission.sh never touch. Needs the stack up.
# Optional LLM judge: set CANARY_JUDGE_ROUTE to a route different from
# whatever wrote the report; unset means deterministic checks only.
canary-research:
	./scripts/canary-research.sh

# Regression gate for the delegated-executor (D-052) path: pins a
# canary-executor route to a single claude-cli chain entry, no
# fallback, so a broken executor fails loudly instead of silently
# passing via native failover. Needs the stack up, a wire-compatible
# provider configured (driver=anthropic or options.anthropic_base_url),
# and the sandbox image built with the claude CLI (`make sandbox-image`).
canary-executor:
	./scripts/canary-executor.sh

# Sycophancy gate: gives a coding mission an unfulfillable goal (a file
# path that does not exist in the fixture, workaround explicitly
# forbidden) and asserts the harness fails honestly instead of claiming
# success or fabricating the file. PASS means phase=failed and the
# referenced file was never created, anywhere. Needs the stack up and
# `make sandbox-image` run first.
canary-impossible:
	./scripts/canary-impossible.sh

# Fresh-install onboarding gate (issue #1062): starts a second compose
# project (timothy-onboarding, ports 8310/3310, its own volumes) and
# walks it in Playwright from the token link through the welcome
# wizard, first chat and sample mission to the setup checklist, then
# tears it down. Manual, not CI: needs CANARY_PROVIDER_KEY for a hosted
# preset (CANARY_PROVIDER_PRESET, default openai) or a reachable host
# Ollama (CANARY_PROVIDER_PRESET=ollama CANARY_MODEL=<pulled model>).
# The dev stack on 8300/3300 is never touched.
canary-onboarding:
	./scripts/canary-onboarding.sh

# Script self-tests. Runs canary-onboarding.sh --dry-run in a bash
# container and checks the commands it would run (project, ports,
# image) carry no secret value.
test-scripts:
	docker run --rm -v $(CURDIR):/src -w /src bash:5.3 ./scripts/canary-onboarding-dry-run.test.sh

# Ecosystem smoke matrix (issue #1018), manual only: one dependency
# audit mission per pinned fork in scripts/ecosystem-matrix.txt, one at
# a time, per-stage results under canary-results/ecosystems/. Needs the
# stack up, gh with push access to the timothy-agent forks,
# CANARY_GITHUB_CONNECTOR_ID and CANARY_GITHUB_DESTINATION_ID.
# CANARY_ECOSYSTEM=<name> runs one row; brain with MISSION_OSV_OFFLINE
# set audits against the database refreshed once per run day.
canary-ecosystems:
	./scripts/canary-ecosystems.sh

# Retrieval regression gate (issue #412): ingests a curated fixture set
# into a dedicated collection, runs a fixed query set through the real
# hybrid retrieval path, scores recall@5 and MRR, then deletes the
# collection. Independent of make canary: the mission harness gate and
# the retrieval gate must be able to fail on their own. Needs the stack
# up. Override the pass threshold with KB_EVAL_MIN_RECALL (default 0.8).
kb-eval:
	./scripts/kb-eval/kb-eval.sh

# Builds the mission sandbox image, one for every mission (D-141,
# deploy/sandbox.Dockerfile). Not a compose service: sandboxes are
# containers brain creates dynamically via the Docker Go SDK, not
# something `docker compose up` runs on its own. Required before running
# any mission. SANDBOX_IMAGE tags a test build without replacing the
# image the running stack uses.
SANDBOX_IMAGE ?= timothy-sandbox:latest
sandbox-image:
	docker build -f deploy/sandbox.Dockerfile -t $(SANDBOX_IMAGE) .

# Checks PHP 8.1 to 8.4 with composer, build tools, mise, runtime installs under the sandbox limits, and the shared toolchain and cache volumes.
sandbox-smoke:
	./scripts/sandbox-smoke.sh $(SANDBOX_IMAGE)
