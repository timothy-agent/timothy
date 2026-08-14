GO_IMAGE   := golang:1.26.6
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
	brain gateway memoryd web markitdown sandboxd dev canary canary-coding canary-research canary-executor sandbox-image

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
#   make brain / make gateway / make memoryd / make web / make markitdown / make whisper / make sandboxd
# Rolling brain and sandboxd separately: restart sandboxd first — the
# API between them is additive-only, so an older brain against a newer
# sandboxd (or vice versa, briefly) stays compatible either order, but
# sandboxd-first avoids brain's own restart racing against sandboxd's
# health check on its way up.
brain gateway memoryd web markitdown whisper sandboxd:
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

# Builds the per-mission sandbox images: the base (python3/node/git/
# bash — deploy/sandbox-base.Dockerfile) plus one variant per
# "environment" (D-05x, sandboxd's image allowlist) FROM that base.
# Not a compose service: sandboxes are containers brain creates
# dynamically via the Docker Go SDK, not something `docker compose up`
# runs on its own. Required before running any mission. timothy-sandbox
# is tagged as an alias of the base image for back-compat with existing
# canary scripts/compose references that predate the environment axis.
sandbox-image:
	docker build -f deploy/sandbox-base.Dockerfile -t timothy-sandbox-base:latest .
	docker tag timothy-sandbox-base:latest timothy-sandbox:latest
	docker build -f deploy/sandbox-go.Dockerfile -t timothy-sandbox-go:latest .
	docker build -f deploy/sandbox-node.Dockerfile -t timothy-sandbox-node:latest .
	docker build -f deploy/sandbox-python.Dockerfile -t timothy-sandbox-python:latest .
	docker build -f deploy/sandbox-java.Dockerfile -t timothy-sandbox-java:latest .
	docker build -f deploy/sandbox-php.Dockerfile -t timothy-sandbox-php:latest .
