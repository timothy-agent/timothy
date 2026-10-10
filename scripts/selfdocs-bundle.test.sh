#!/usr/bin/env bash
# Real-shell round trip for selfdocs-bundle.sh, run under /bin/sh.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${REPO_ROOT}/scripts/selfdocs-bundle.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
failures=0

ok() { echo "ok   $1"; }
fail() { echo "FAIL $1" >&2; failures=$((failures + 1)); }
expect() { if eval "$2"; then ok "$1"; else fail "$1"; fi; }

mkdir -p "${work}/manifest" "${work}/docs" "${work}/dup"
printf 'm1\n' > "${work}/manifest/cap__b.md"
printf 'm2\n' > "${work}/manifest/cap__a.md"
printf '{"source":"manifest"}\n' > "${work}/manifest/manifest.json"
printf 'd1\n' > "${work}/docs/guide__x.md"
printf '{"source":"docs"}\n' > "${work}/docs/manifest.json"
printf 'clash\n' > "${work}/dup/cap__a.md"

run() { /bin/sh "${SCRIPT}" "$@"; }

run --docs "" --manifest "${work}/manifest" --out "${work}/o1" --version 1.2.3 >/dev/null
expect "manifest only: pages copied" '[ -f "${work}/o1/cap__a.md" ] && [ -f "${work}/o1/cap__b.md" ]'
expect "manifest only: sources" 'grep -q "\"sources\": \[\"manifest\"\]" "${work}/o1/manifest.json"'
expect "manifest only: version" 'grep -q "\"version\": \"1.2.3\"" "${work}/o1/manifest.json"'
expect "manifest only: no source manifest.json leak" '! grep -q "\"source\":" "${work}/o1/manifest.json"'
want="$(cat "${work}/manifest/cap__a.md" "${work}/manifest/cap__b.md" | (sha256sum 2>/dev/null || shasum -a 256) | cut -d' ' -f1)"
expect "manifest only: sha256 over sorted contents" 'grep -q "\"sha256\": \"${want}\"" "${work}/o1/manifest.json"'

run --docs "${work}/docs" --manifest "${work}/manifest" --out "${work}/o2" --version 1.2.3 >/dev/null
expect "with docs: all pages" '[ -f "${work}/o2/guide__x.md" ] && [ -f "${work}/o2/cap__a.md" ]'
expect "with docs: sources" 'grep -q "\"sources\": \[\"manifest\",\"docs\"\]" "${work}/o2/manifest.json"'
expect "with docs: files sorted" '[ "$(grep -o "\"[a-z_]*\.md\"" "${work}/o2/manifest.json" | tr -d "\n")" = "\"cap__a.md\"\"cap__b.md\"\"guide__x.md\"" ]'

if run --docs "${work}/dup" --manifest "${work}/manifest" --out "${work}/o3" --version 1.2.3 2>"${work}/err"; then
  fail "duplicate name must fail"
else
  ok "duplicate name fails"
fi
expect "duplicate name: reported" 'grep -q "duplicate page name: cap__a.md" "${work}/err"'
expect "duplicate name: no output dir" '[ ! -e "${work}/o3" ]'

run --docs "${work}/docs" --manifest "${work}/manifest" --out "${work}/o4" --version 1.2.3 >/dev/null
expect "hash stable across runs" '[ "$(cat "${work}/o2/manifest.json")" = "$(cat "${work}/o4/manifest.json")" ]'
run --docs "${work}/docs" --manifest "${work}/manifest" --out "${work}/o4" --version 1.2.3 >/dev/null
expect "rerun over existing out is stable" '[ "$(cat "${work}/o2/manifest.json")" = "$(cat "${work}/o4/manifest.json")" ]'

if run --manifest "${work}/missing" --out "${work}/o5" --version 1 2>/dev/null; then
  fail "missing manifest dir must fail"
else
  ok "missing manifest dir fails"
fi

if [ "${failures}" -ne 0 ]; then
  echo "${failures} failure(s)" >&2
  exit 1
fi
echo "all selfdocs-bundle checks passed"
