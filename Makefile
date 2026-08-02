GO_IMAGE   := golang:1.26.5
LINT_IMAGE := golangci/golangci-lint:v2.12.2
COMPOSE    := docker compose -f deploy/docker-compose.yml

# Go toolchain runs containerized: no host Go install required, same
# version everywhere. Named volumes cache modules and builds.
GO_RUN := docker run --rm -v $(CURDIR):/src -w /src \
	-v timothy-go-mod:/go/pkg/mod -v timothy-go-cache:/root/.cache/go-build \
	-e GOFLAGS=-buildvcs=false $(GO_IMAGE)

.PHONY: build test test-integration test-live vet lint tidy skills-validate up down logs \
	brain gateway memoryd web markitdown sandboxd dev canary canary-coding sandbox-image

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

up:
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

# Golden-mission regression gate: runs one research mission end-to-end
# against the live stack and asserts it completes unattended (no parks,
# harness-verified artifacts, bounded turns). Needs the stack up.
canary:
	./scripts/canary-mission.sh

# Same gate for the coding path: worktree provisioning, LLM review,
# artifact verified inside the worktree. Needs the stack up. If
# MISSION_SANDBOX_IMAGE is set in deploy/.env, run `make sandbox-image`
# first — otherwise mission shell calls fail opaquely.
canary-coding:
	./scripts/canary-coding.sh

# Builds the per-mission sandbox image (python3/node/git/bash — see
# deploy/sandbox.Dockerfile). Not a compose service: sandboxes are
# containers brain creates dynamically via the Docker Go SDK, not
# something `docker compose up` runs on its own. Only needed when
# MISSION_SANDBOX_IMAGE is set.
sandbox-image:
	docker build -f deploy/sandbox.Dockerfile -t timothy-sandbox:latest .
