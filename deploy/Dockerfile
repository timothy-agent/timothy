# syntax=docker/dockerfile:1

FROM golang:1.26.6 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/

ARG SERVICE
# APP_VERSION is baked into the binary so /health reports the release
# it was built from. The web footer reads that, instead of a version
# baked into a web image release.yml may have copied forward unchanged.
# Unset (local `make build`) links an empty string, which /health omits.
ARG APP_VERSION
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
    -ldflags "-X github.com/SumonMSelim/timothy/internal/platform/service.Version=${APP_VERSION}" \
    -o /out/app ./cmd/${SERVICE}

# Brain's shell tool needs /bin/sh; alpine provides busybox sh while
# staying minimal. Select with build target runtime-shell. Everything
# else keeps the distroless default (last stage). curl is installed
# explicitly: busybox wget is present by default, but plan/check_cmd
# content (model-authored) reaches for curl as the more common default,
# and a missing binary fails a check_cmd with a misleading exit code
# instead of a clear "not found". git is required by coding missions:
# the missions engine execs `git worktree add`, `git diff`, rollback,
# and teardown inside this container. openssh-keygen provides
# ssh-keygen, which git invokes for SSH commit signing/verification
# (`git commit -S`/`git verify-commit` with gpg.format=ssh).
FROM alpine:3.22.2 AS runtime-shell
RUN apk add --no-cache curl git openssh-keygen
COPY --from=build /out/app /app
# Skill packs ship inside the brain image; release.yml's change filter
# lists skills/ so a pack edit rebuilds it instead of copying forward.
COPY skills/ /skills/
RUN mkdir -p /workspace /attachments && chown nobody:nobody /workspace /attachments
USER nobody
ENTRYPOINT ["/app"]

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
