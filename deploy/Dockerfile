# syntax=docker/dockerfile:1

FROM golang:1.26.5 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/

ARG SERVICE
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/app ./cmd/${SERVICE}

# Brain's shell tool needs /bin/sh; alpine provides busybox sh while
# staying minimal. Select with build target runtime-shell. Everything
# else keeps the distroless default (last stage). curl is installed
# explicitly: busybox wget is present by default, but plan/verify_cmd
# content (model-authored) reaches for curl as the more common default,
# and a missing binary fails a verify_cmd with a misleading exit code
# instead of a clear "not found".
FROM alpine:3.22.2 AS runtime-shell
RUN apk add --no-cache curl
COPY --from=build /out/app /app
COPY skills/ /skills/
RUN mkdir -p /workspace && chown nobody:nobody /workspace
USER nobody
ENTRYPOINT ["/app"]

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
