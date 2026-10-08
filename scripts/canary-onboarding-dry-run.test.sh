#!/usr/bin/env bash
# Checks the commands canary-onboarding.sh would run: the right project,
# ports and image, and no secret value anywhere in the output.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${REPO_ROOT}/scripts/canary-onboarding.sh"
fake_key="sk-dry-run-key-$$"
fake_token="dry-run-token-$$"
failures=0

check() {
  local desc="$1" pattern="$2" out="$3"
  if grep -qF -- "${pattern}" <<<"${out}"; then
    echo "ok   ${desc}"
  else
    echo "FAIL ${desc}: missing '${pattern}'" >&2
    failures=$((failures + 1))
  fi
}

check_absent() {
  local desc="$1" pattern="$2" out="$3"
  if grep -qF -- "${pattern}" <<<"${out}"; then
    echo "FAIL ${desc}: found '${pattern}'" >&2
    failures=$((failures + 1))
  else
    echo "ok   ${desc}"
  fi
}

out="$(TIMOTHY_API_TOKEN="${fake_token}" CANARY_PROVIDER_PRESET=openai CANARY_PROVIDER_KEY="${fake_key}" \
  "${SCRIPT}" --dry-run 2>&1)"
check "compose project" "docker compose -p timothy-onboarding" "${out}"
check "brain port" "BRAIN_PORT=8310" "${out}"
check "web port" "WEB_PORT=3310" "${out}"
check "build by default" "up -d --build" "${out}"
check "health on canary port" "http://localhost:8310/health" "${out}"
check "playwright image" "mcr.microsoft.com/playwright:v1.63.0-noble" "${out}"
check "web base url on canary port" ":3310" "${out}"
check "key passed by name" "-e CANARY_PROVIDER_KEY " "${out}"
check "token passed by name" "-e TIMOTHY_API_TOKEN " "${out}"
check "teardown" "down -v --remove-orphans" "${out}"
check_absent "key value" "${fake_key}" "${out}"
check_absent "token value" "${fake_token}" "${out}"
check_absent "dev stack port" ":8300" "${out}"
check_absent "dev project" "-p timothy " "${out}"

out="$(TIMOTHY_API_TOKEN="${fake_token}" CANARY_PROVIDER_PRESET=ollama CANARY_ONBOARDING_NO_BUILD=1 \
  "${SCRIPT}" --dry-run 2>&1)"
check "ollama needs no key" "preset=ollama" "${out}"
check_absent "no build when asked" "--build" "${out}"

if out="$(TIMOTHY_API_TOKEN="${fake_token}" CANARY_PROVIDER_PRESET=openai CANARY_PROVIDER_KEY='' \
  "${SCRIPT}" --dry-run 2>&1)"; then
  echo "FAIL missing key must exit non-zero" >&2
  failures=$((failures + 1))
else
  check "missing key named" "CANARY_PROVIDER_KEY is required" "${out}"
fi

if (( failures > 0 )); then
  echo "canary-onboarding dry run: ${failures} check(s) failed" >&2
  exit 1
fi
echo "canary-onboarding dry run: PASS"
