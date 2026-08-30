# syntax=docker/dockerfile:1
# Production web image: Vite build served by unprivileged nginx.
# The HugeIcons Pro registry token is consumed as a BuildKit secret so
# it never lands in an image layer or the build history.
# --platform=$BUILDPLATFORM: the Vite dist output is arch-independent,
# so the build stage always runs natively. Running node under qemu for
# the arm64 leg crashed with illegal-instruction core dumps and hung
# release builds for hours.
FROM --platform=$BUILDPLATFORM node:24.18.0-alpine AS build
ARG GIT_SHA
ARG APP_VERSION
ENV GIT_SHA=${GIT_SHA}
ENV APP_VERSION=${APP_VERSION}
WORKDIR /app
COPY package.json package-lock.json .npmrc ./
RUN --mount=type=secret,id=hugeicons_token \
    HUGEICONS_TOKEN="$(cat /run/secrets/hugeicons_token)" npm ci
COPY . .
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.29-alpine
# The base image's OS packages lag their alpine repo fixes; upgrade so
# the Trivy CI gate (CRITICAL/HIGH, fixed-only) stays green.
USER root
RUN apk upgrade --no-cache
USER 101
# Same-origin /v1 in the browser; nginx forwards to brain (the image
# entrypoint envsubsts ${BRAIN_URL} into the template at startup).
ENV BRAIN_URL=http://brain:8080
COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /app/dist /usr/share/nginx/html
