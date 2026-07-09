GO_IMAGE   := golang:1.26.5
LINT_IMAGE := golangci/golangci-lint:v2.12.2
COMPOSE    := docker compose -f deploy/docker-compose.yml

# Go toolchain runs containerized: no host Go install required, same
# version everywhere. Named volumes cache modules and builds.
GO_RUN := docker run --rm -v $(CURDIR):/src -w /src \
	-v timothy-go-mod:/go/pkg/mod -v timothy-go-cache:/root/.cache/go-build \
	-e GOFLAGS=-buildvcs=false $(GO_IMAGE)

.PHONY: build test test-integration vet lint tidy up down logs

build:
	$(GO_RUN) go build ./...

test:
	$(GO_RUN) go test -race ./...

# Needs the compose stack up and POSTGRES_PASSWORD exported (or in the
# calling environment); runs inside the compose network against the
# real postgres.
test-integration:
	docker run --rm -v $(CURDIR):/src -w /src \
		-v timothy-go-mod:/go/pkg/mod -v timothy-go-cache:/root/.cache/go-build \
		-e GOFLAGS=-buildvcs=false --network timothy_timothy \
		-e DATABASE_URL=postgres://timothy:$(POSTGRES_PASSWORD)@postgres:5432/timothy \
		$(GO_IMAGE) go test -race -tags integration ./internal/platform/...

vet:
	$(GO_RUN) go vet ./...

lint:
	docker run --rm -v $(CURDIR):/src -w /src \
		-v timothy-go-mod:/go/pkg/mod -v timothy-lint-cache:/root/.cache \
		-e GOFLAGS=-buildvcs=false $(LINT_IMAGE) golangci-lint run

tidy:
	$(GO_RUN) go mod tidy

up:
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f
