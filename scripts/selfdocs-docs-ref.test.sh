#!/bin/sh
# Real-shell round trip for selfdocs-docs-ref.sh: tag when it exists,
# main otherwise. Needs the git CLI.
set -eu

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="${REPO_ROOT}/scripts/selfdocs-docs-ref.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
failures=0

check() {
  if [ "$2" = "$3" ]; then
    echo "ok   $1"
  else
    echo "FAIL $1: got '$2', want '$3'" >&2
    failures=$((failures + 1))
  fi
}

git init -q "${work}/repo"
git -C "${work}/repo" -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m init
git -C "${work}/repo" tag v1.0.0-alpha.1

check "existing tag" "$(/bin/sh "${SCRIPT}" "${work}/repo" 1.0.0-alpha.1)" "v1.0.0-alpha.1"
check "new version uses main" "$(/bin/sh "${SCRIPT}" "${work}/repo" 1.0.0-alpha.2)" "main"

if [ "${failures}" -ne 0 ]; then
  echo "${failures} failure(s)" >&2
  exit 1
fi
echo "all selfdocs-docs-ref checks passed"
