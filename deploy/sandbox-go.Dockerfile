# syntax=docker/dockerfile:1

# Go environment variant (tag timothy-sandbox-go): adds the Go
# toolchain to the base mission sandbox — missions writing Go need
# `go build`/`go test` for their verify_cmd.
ARG SANDBOX_BASE=timothy-sandbox-base:latest
FROM golang:1.26.6 AS go-dist

FROM ${SANDBOX_BASE}

USER root

# Go toolchain from the same pinned image the repo's own containerized
# builds use. GOPATH lands in the writable home base already set up.
COPY --from=go-dist /usr/local/go /usr/local/go
ENV GOPATH=/home/sandbox/go
# CRITICAL LESSON: symlinked into /usr/local/bin (not only ENV PATH) —
# sandboxd creates containers with a fixed minimal PATH that ignores
# image ENV, so a PATH-only install exists in the image yet 127s at
# exec time. Every toolchain binary added by a variant must be
# reachable this way.
RUN ln -s /usr/local/go/bin/go /usr/local/bin/go && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt
ENV PATH="/home/sandbox/go/bin:${PATH}"

USER 65534:65534
