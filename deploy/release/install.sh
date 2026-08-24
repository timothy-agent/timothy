#!/bin/sh
# Timothy installer: downloads release assets into the current
# directory, generates secrets on first run, and starts the stack.
set -eu

TAG="__TIMOTHY_TAG__"

fail() {
  echo "error: $1" >&2
  exit 1
}

fetch() {
  # fetch <url> <dest>
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  else
    wget -q "$1" -O "$2"
  fi
}

# Release assets ship with TAG baked in. When this script is fetched
# straight from the repo instead, the placeholder is still present
# (the case pattern is a prefix so the release sed leaves it alone);
# resolve the newest release tag from the GitHub API.
case "$TAG" in
__TIMOTHY*)
  if command -v curl >/dev/null 2>&1; then
    releases=$(curl -fsSL "https://api.github.com/repos/timothy-agent/timothy/releases?per_page=1")
  else
    releases=$(wget -qO- "https://api.github.com/repos/timothy-agent/timothy/releases?per_page=1")
  fi
  TAG=$(printf '%s' "$releases" | grep -m1 '"tag_name"' | cut -d'"' -f4)
  TAG=${TAG#v}
  [ -n "$TAG" ] || fail "could not determine the latest release tag"
  ;;
esac

RELEASE_TAG="v${TAG}"
BASE_URL="https://github.com/timothy-agent/timothy/releases/download/${RELEASE_TAG}"

echo "Timothy installer (${RELEASE_TAG})"

# --- Preflight ---
command -v docker >/dev/null 2>&1 || fail "docker not found on PATH. Install Docker first: https://docs.docker.com/get-docker/"
docker compose version >/dev/null 2>&1 || fail "'docker compose' plugin not available. Install/update Docker to a version with Compose v2."
command -v openssl >/dev/null 2>&1 || fail "openssl not found on PATH. Install it to generate secrets."
if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
  fail "neither curl nor wget found on PATH."
fi

# --- Install directory ---
# Operate in the current directory when it already holds a Timothy
# install (in-place upgrade); otherwise use TIMOTHY_HOME (default
# ~/timothy) so the one-liner works from anywhere.
if [ -f .env ] && [ -f docker-compose.yml ]; then
  echo "Existing install detected in $(pwd)."
else
  TIMOTHY_HOME="${TIMOTHY_HOME:-$HOME/timothy}"
  mkdir -p "$TIMOTHY_HOME"
  cd "$TIMOTHY_HOME"
  echo "Installing into ${TIMOTHY_HOME}"
fi

# --- Download assets ---
echo "Downloading release assets..."
fetch "${BASE_URL}/docker-compose.yml" docker-compose.yml
fetch "${BASE_URL}/env.example" env.example
mkdir -p searxng
fetch "${BASE_URL}/searxng-settings.yml" searxng/settings.yml

# --- .env ---
if [ -f .env ]; then
  echo "Existing .env found (upgrade path): keeping your secrets, bumping TIMOTHY_VERSION to ${TAG}."
  sed -i.bak "s#^TIMOTHY_VERSION=.*#TIMOTHY_VERSION=${TAG}#" .env && rm -f .env.bak
else
  echo "Generating .env with fresh secrets..."
  cp env.example .env

  postgres_password=$(openssl rand -hex 24)
  master_key=$(openssl rand -base64 32)
  api_token=$(openssl rand -hex 32)

  if [ "$(uname -s)" = "Linux" ] && [ -S /var/run/docker.sock ]; then
    sock_gid=$(stat -c '%g' /var/run/docker.sock)
  else
    sock_gid=0
  fi

  # Portable in-place sed edit (BSD/macOS requires an extension arg to -i).
  sed_inplace() {
    sed -i.bak "$1" .env && rm -f .env.bak
  }

  sed_inplace "s#^POSTGRES_PASSWORD=.*#POSTGRES_PASSWORD=${postgres_password}#"
  sed_inplace "s#^TIMOTHY_MASTER_KEY=.*#TIMOTHY_MASTER_KEY=${master_key}#"
  sed_inplace "s#^TIMOTHY_API_TOKEN=.*#TIMOTHY_API_TOKEN=${api_token}#"
  sed_inplace "s#^DOCKER_SOCK_GID=.*#DOCKER_SOCK_GID=${sock_gid}#"
  sed_inplace "s#^TIMOTHY_VERSION=.*#TIMOTHY_VERSION=${TAG}#"
  sed_inplace "s#__TIMOTHY_TAG__#${TAG}#"

  echo ".env generated. Secrets were not printed to the terminal."
fi

# --- Start ---
echo "Pulling images..."
docker compose pull

# compose pull only covers services; the mission sandbox image is an
# env var handed to sandboxd (which pulls per-mission containers via
# the Docker socket, not through compose), so pull it explicitly here,
# same TIMOTHY_VERSION tag as everything else, read back from .env.
timothy_version=$(sed -n 's/^TIMOTHY_VERSION=//p' .env | head -n1)
echo "Pulling mission sandbox image..."
docker pull "ghcr.io/timothy-agent/timothy-sandbox:${timothy_version}"

echo "Starting Timothy..."
docker compose up -d

# --- Wait for web ---
web_port=$(sed -n 's/^WEB_PORT=//p' .env | head -n1)
web_port=${web_port:-3300}

echo "Waiting for the web UI to come up on port ${web_port}..."
i=0
until curl -fsS "http://localhost:${web_port}/" >/dev/null 2>&1 || wget -q -O /dev/null "http://localhost:${web_port}/" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -ge 30 ]; then
    echo "warning: web UI did not respond after ~60s; check 'docker compose logs -f'" >&2
    break
  fi
  sleep 2
done

api_token=$(sed -n 's/^TIMOTHY_API_TOKEN=//p' .env | head -n1)

echo ""
echo "================================================================"
echo " Timothy is up."
echo ""
echo " Install dir: $(pwd)"
echo ""
echo " Sign in:  http://localhost:${web_port}/#token=${api_token}"
echo " (this magic link signs the web UI in automatically)"
echo ""
echo " Follow logs:  docker compose logs -f"
echo "================================================================"
