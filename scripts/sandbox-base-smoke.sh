#!/usr/bin/env bash
# Smoke-test the base sandbox image: mise version, writable mise data dir,
# shims on PATH, trusted config path, and that a toolchain installed into
# a named volume is reused by a second container. Needs network (installs
# python 3.10 and node 18 via mise).
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

# Repo version file under /workspace (the trusted path): mise does not
# read .python-version for shims, so the harness pins the detected
# version globally (issue #991) and the shim then resolves it there.
fix="$(mktemp -d)"
trap 'docker volume rm -f "$vol" >/dev/null 2>&1 || true; rm -rf "$fix"' EXIT
chmod 755 "$fix"
echo "3.10" > "$fix/.python-version"
out="$(docker run --rm -u 65534:65534 -v "$vol:$MISE_DIR" -v "$fix:/workspace/fixture" -w /workspace/fixture "$IMAGE" \
  sh -c 'mise use -g "python@$(cat .python-version)" >/dev/null && python --version')"
case "$out" in
  "Python 3.10."*) echo "version file shim ok: $out" ;;
  *) echo "FAIL: .python-version fixture expected Python 3.10.x, got $out" >&2; exit 1 ;;
esac

# A pinned node must not hijack the executor CLIs (D-126): they keep
# running on the image's own node.
out="$("${VRUN[@]}" sh -c 'mise use -g node@18 >/dev/null && node --version')"
case "$out" in
  v18.*) echo "pinned node active: $out" ;;
  *) echo "FAIL: expected node v18.x after pin, got $out" >&2; exit 1 ;;
esac
out="$("${VRUN[@]}" sh -c '
  mise use -g node@18 >/dev/null
  for c in claude codex pi opencode; do
    f="$(readlink -f "$(command -v $c)")"
    first="$(head -n 1 "$f" | tr -d "\000-\010")"
    case "$first" in
      "#!"*) [ "$first" = "#!/usr/local/bin/node" ] || { echo "$c:$first"; exit 1; } ;;
    esac
    "$c" --version >/dev/null || { echo "$c: --version failed"; exit 1; }
  done
  echo ok')"
if [ "$out" != "ok" ]; then
  echo "FAIL: executor CLIs affected by node pin: $out" >&2
  exit 1
fi
echo "executors unaffected by node pin ok"
