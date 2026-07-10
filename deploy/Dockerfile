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
# else keeps the distroless default (last stage).
FROM alpine:3.22.2 AS runtime-shell
COPY --from=build /out/app /app
RUN mkdir -p /workspace && chown nobody:nobody /workspace
USER nobody
ENTRYPOINT ["/app"]

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
