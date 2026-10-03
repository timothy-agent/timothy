#!/usr/bin/env bash
# Smoke-test the base sandbox image: mise version, writable mise data dir,
# shims on PATH, trusted config path, and that a toolchain installed into
# a named volume is reused by a second container. Needs network (installs
# python 3.10 via mise).
# Usage: scripts/sandbox-base-smoke.sh [image]
set -euo pipefail

IMAGE="${1:-timothy-sandbox-base:latest}"
RUN=(docker run --rm -u 65534:65534 "$IMAGE")
MISE_DIR=/home/sandbox/.local/share/mise

ver="$("${RUN[@]}" mise --version)"
case "$ver" in
  2026.10.1*) echo "mise version ok: $ver" ;;
  *) echo "FAIL: expected mise 2026.10.1, got $ver" >&2; exit 1 ;;
esac

"${RUN[@]}" sh -c "touch $MISE_DIR/.smoke"
echo "mise data dir writable"

path="$("${RUN[@]}" sh -c 'echo $PATH')"
case "$path" in
  *"$MISE_DIR/shims"*) echo "shims on PATH ok" ;;
  *) echo "FAIL: mise shims not on PATH: $path" >&2; exit 1 ;;
esac

trusted="$("${RUN[@]}" sh -c 'echo "$MISE_TRUSTED_CONFIG_PATHS"')"
if [ "$trusted" != "/workspace" ]; then
  echo "FAIL: MISE_TRUSTED_CONFIG_PATHS = '$trusted', want /workspace" >&2
  exit 1
fi
echo "trusted config path ok"

vol="timothy-smoke-mise-$RANDOM$RANDOM"
docker volume create "$vol" >/dev/null
trap 'docker volume rm -f "$vol" >/dev/null 2>&1 || true' EXIT
VRUN=(docker run --rm -u 65534:65534 -v "$vol:$MISE_DIR" "$IMAGE")

out="$("${VRUN[@]}" sh -c 'mise use -g python@3.10 >/dev/null && python --version')"
case "$out" in
  "Python 3.10."*) echo "first install ok: $out" ;;
  *) echo "FAIL: expected Python 3.10.x, got $out" >&2; exit 1 ;;
esac

start=$SECONDS
out="$("${VRUN[@]}" sh -c 'mise use -g python@3.10 >/dev/null && python --version')"
elapsed=$((SECONDS - start))
case "$out" in
  "Python 3.10."*) ;;
  *) echo "FAIL: second run expected Python 3.10.x, got $out" >&2; exit 1 ;;
esac
if [ "$elapsed" -ge 15 ]; then
  echo "FAIL: cached run took ${elapsed}s, want under 15s" >&2
  exit 1
fi
echo "cached run ok: $out in ${elapsed}s"
