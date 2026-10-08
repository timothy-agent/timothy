#!/usr/bin/env bash
# Smoke-test the sandbox image (D-141, one image for every mission):
# build tools and -dev libraries, PHP 8.1 to 8.4 with their extensions,
# per-mission minor selection and composer, mise version, writable mise
# data dir, /tmp and HOME executable under sandboxd's mounts, shims on
# PATH, trusted config path, and that a toolchain installed into a named
# volume is reused by a second container. Package caches (D-131) point
# at the cache mount, are shared through a named volume, and survive
# parallel npm and mise installs. java@21, ruby@3.3 and rust@stable
# install under sandboxd's limits, two containers with different node
# pins share one toolchains volume, and no pin falls back to the image
# node. Needs network (mise runtimes, two small npm packages).
# SANDBOX_SMOKE_LARAVEL=1 also creates and tests a Laravel 10 app on
# 8.1 and a Laravel 12 app on 8.4 (slow).
# Usage: scripts/sandbox-smoke.sh [image]
set -euo pipefail

IMAGE="${1:-timothy-sandbox:latest}"
RUN=(docker run --rm -u 65534:65534 "$IMAGE")
MISE_DIR=/home/sandbox/.mise

out="$("${RUN[@]}" sh -c 'gcc --version | head -n 1 && make --version | head -n 1 && pg_config --version &&
  pkg-config --exists libpq sqlite3 openssl zlib libxml-2.0 yaml-0.1 libffi oniguruma libzip && echo libs-ok')"
case "$out" in
  *libs-ok) echo "build tools ok: $(echo "$out" | head -n 1), $(echo "$out" | sed -n 3p)" ;;
  *) echo "FAIL: build tools or -dev libraries missing: $out" >&2; exit 1 ;;
esac

# PHP (D-127): 8.4 default, every baked minor with the extensions Laravel
# and common packages need, composer as the sandbox uid.
ver="$("${RUN[@]}" php -r 'echo PHP_VERSION;')"
case "$ver" in
  8.4.*) echo "default php ok: $ver" ;;
  *) echo "FAIL: expected default PHP 8.4.x, got $ver" >&2; exit 1 ;;
esac
for v in 8.1 8.2 8.3 8.4; do
  got="$("${RUN[@]}" "php$v" -r 'echo PHP_VERSION;')"
  case "$got" in
    "$v".*) ;;
    *) echo "FAIL: php$v reports $got" >&2; exit 1 ;;
  esac
  mods="$("${RUN[@]}" "php$v" -m | tr 'A-Z' 'a-z')"
  for ext in pdo_sqlite sqlite3 mbstring xml dom curl zip intl tokenizer ctype fileinfo openssl pdo json bcmath pdo_mysql pdo_pgsql gd redis; do
    if ! grep -qx "$ext" <<<"$mods"; then
      echo "FAIL: php$v missing extension: $ext" >&2
      exit 1
    fi
  done
  echo "php $got extensions ok"
done
"${RUN[@]}" composer -V --no-interaction
echo "composer ok (uid 65534)"

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

# Non-interactive, wide-output env (issue #1009): a Symfony console (php
# artisan) wraps at $COLUMNS, so a 300-char line must come through whole
# when php is present in the image.
cols="$("${RUN[@]}" sh -c 'echo "$COLUMNS"')"
ci="$("${RUN[@]}" sh -c 'echo "$CI"')"
if [ "$cols" != "200" ] || [ "$ci" != "1" ]; then
  echo "FAIL: COLUMNS='$cols' CI='$ci', want 200 and 1" >&2
  exit 1
fi
echo "non-interactive env ok: COLUMNS=$cols CI=$ci"
for v in MISE_AUTO_INSTALL MISE_EXEC_AUTO_INSTALL MISE_NOT_FOUND_AUTO_INSTALL; do
  val="$("${RUN[@]}" sh -c "echo \"\$$v\"")"
  [ "$val" = "false" ] || { echo "FAIL: $v = '$val', want false" >&2; exit 1; }
done
echo "mise auto-install off ok"

trusted="$("${RUN[@]}" sh -c 'echo "$MISE_TRUSTED_CONFIG_PATHS"')"
if [ "$trusted" != "/workspace" ]; then
  echo "FAIL: MISE_TRUSTED_CONFIG_PATHS = '$trusted', want /workspace" >&2
  exit 1
fi
echo "trusted config path ok"

vol="timothy-smoke-mise-$RANDOM$RANDOM"
docker volume create "$vol" >/dev/null
trap 'docker volume rm -f "$vol" >/dev/null 2>&1 || true' EXIT
# Same read-only rootfs and tmpfs options sandboxd gives a mission
# container (internal/sandboxd/manager.go createContainer); keep in sync.
SANDBOX_MOUNTS=(--read-only
  --tmpfs /tmp:rw,exec,nosuid,nodev,size=512m
  --tmpfs /home/sandbox:rw,exec,nosuid,nodev,uid=65534,gid=65534,size=1g)
VRUN=(docker run --rm -u 65534:65534 "${SANDBOX_MOUNTS[@]}" -v "$vol:$MISE_DIR" "$IMAGE")

"${VRUN[@]}" sh -c 'printf "#!/bin/sh\necho ok\n" > /tmp/s && chmod +x /tmp/s && /tmp/s >/dev/null \
  && mkdir -p ~/.local/bin && cp /tmp/s ~/.local/bin/s && ~/.local/bin/s >/dev/null' \
  || { echo "FAIL: /tmp or HOME is not writable and executable for uid 65534" >&2; exit 1; }
echo "tmp and home exec ok"

# Per-mission PHP minor (D-127) under the hardened mounts, the same links
# buildPHPSelectCmd in internal/brain/missions/toolchain.go makes.
SELECT_81='mkdir -p /home/sandbox/.local/bin; for b in php phar phar.phar; do if [ -x "/usr/bin/${b}8.1" ]; then ln -sf "/usr/bin/${b}8.1" "/home/sandbox/.local/bin/${b}"; fi; done'
got="$(docker run --rm -u 65534:65534 --cap-drop ALL --security-opt no-new-privileges "${SANDBOX_MOUNTS[@]}" -w /tmp "$IMAGE" \
  sh -c "$SELECT_81 && php -r 'echo PHP_VERSION;' && composer --version --no-interaction >/dev/null")"
case "$got" in
  8.1.*) echo "hardened select ok: php $got, composer runs on it" ;;
  *) echo "FAIL: selecting 8.1 under sandbox mounts gave $got" >&2; exit 1 ;;
esac
cols="$("${VRUN[@]}" php -r 'echo getenv("COLUMNS"), "/", getenv("CI");')"
[ "$cols" = "200/1" ] || { echo "FAIL: php sees COLUMNS/CI = '$cols', want 200/1" >&2; exit 1; }
echo "php sees wide non-interactive env ok"
if [ "${SANDBOX_SMOKE_LARAVEL:-0}" = "1" ]; then
  LRUN=(docker run --rm -u 65534:65534 "${SANDBOX_MOUNTS[@]}" -w /tmp "$IMAGE")
  "${LRUN[@]}" sh -c "$SELECT_81 && composer create-project 'laravel/laravel:^10.0' app --no-interaction --prefer-dist --quiet && cd app && php artisan test"
  echo "laravel 10 on php 8.1 ok"
  "${LRUN[@]}" sh -c "composer create-project 'laravel/laravel:^12.0' app --no-interaction --prefer-dist --quiet && cd app && php artisan test"
  echo "laravel 12 on php 8.4 ok"
fi

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

# Package caches (D-131): every cache env points under ~/.cache, which
# sandboxd mounts from the shared sandbox-caches volume.
CACHE_DIR=/home/sandbox/.cache
out="$("${RUN[@]}" sh -c 'echo "$npm_config_cache $COMPOSER_CACHE_DIR $PIP_CACHE_DIR $UV_CACHE_DIR $GOMODCACHE $GOCACHE $GRADLE_USER_HOME $YARN_CACHE_FOLDER $BUN_INSTALL_CACHE_DIR $MISE_CACHE_DIR $MISE_STATE_DIR"; npm config get cache')"
for word in $out; do
  case "$word" in
    "$CACHE_DIR"/*) ;;
    *) echo "FAIL: cache path outside $CACHE_DIR: $word (all: $out)" >&2; exit 1 ;;
  esac
done
echo "cache envs ok"

cvol="timothy-smoke-caches-$RANDOM$RANDOM"
tvol="timothy-smoke-mise-par-$RANDOM$RANDOM"
docker volume create "$cvol" >/dev/null
docker volume create "$tvol" >/dev/null
trap 'docker volume rm -f "$vol" "$cvol" "$tvol" >/dev/null 2>&1 || true; rm -rf "$fix"' EXIT
CRUN=(docker run --rm -u 65534:65534 "${SANDBOX_MOUNTS[@]}" -v "$cvol:$CACHE_DIR" "$IMAGE")
NPM_TRY='mkdir -p /tmp/p && cd /tmp/p && npm init -y >/dev/null && npm install --no-audit'

# A second container on the same volume installs offline: the first
# container's download is in the shared cache.
"${CRUN[@]}" sh -c "$NPM_TRY is-number@7.0.0 >/dev/null" \
  || { echo "FAIL: first npm install with the cache volume" >&2; exit 1; }
"${CRUN[@]}" sh -c "$NPM_TRY --offline is-number@7.0.0 >/dev/null" \
  || { echo "FAIL: second container missed the shared npm cache" >&2; exit 1; }
echo "shared npm cache hit ok"

# Two containers installing the same package into one cache at once:
# both succeed and the cache still verifies (cacache writes atomically).
"${CRUN[@]}" sh -c "$NPM_TRY is-odd@3.0.1 >/dev/null" & p1=$!
"${CRUN[@]}" sh -c "$NPM_TRY is-odd@3.0.1 >/dev/null" & p2=$!
wait "$p1" || { echo "FAIL: parallel npm install 1" >&2; exit 1; }
wait "$p2" || { echo "FAIL: parallel npm install 2" >&2; exit 1; }
"${CRUN[@]}" npm cache verify >/dev/null \
  || { echo "FAIL: npm cache corrupt after parallel installs" >&2; exit 1; }
echo "parallel npm installs ok"

# Two containers installing the same mise tool version into one
# toolchains volume at once. Bare mise is not safe here: both write the
# same download file and one fails with a size mismatch. The harness
# wraps its installs in flock on the data dir (miseLocked in
# internal/brain/missions/toolchain.go); keep this command in sync.
PRUN=(docker run --rm -u 65534:65534 "${SANDBOX_MOUNTS[@]}" -v "$tvol:$MISE_DIR" -v "$cvol:$CACHE_DIR" "$IMAGE")
LOCKED='(mkdir -p "${MISE_DATA_DIR:-/tmp}" && flock "${MISE_DATA_DIR:-/tmp}/.timothy-install.lock" mise install node@20.18.0)'
"${PRUN[@]}" sh -c "$LOCKED" >/dev/null 2>&1 & p1=$!
"${PRUN[@]}" sh -c "$LOCKED" >/dev/null 2>&1 & p2=$!
wait "$p1" || { echo "FAIL: parallel mise install 1" >&2; exit 1; }
wait "$p2" || { echo "FAIL: parallel mise install 2" >&2; exit 1; }
out="$("${PRUN[@]}" mise exec node@20.18.0 -- node --version)"
if [ "$out" != "v20.18.0" ]; then
  echo "FAIL: node after parallel mise installs = '$out', want v20.18.0" >&2
  exit 1
fi
echo "parallel mise installs ok"

# Two missions with different node pins on one toolchains volume (D-141):
# `mise use --global` writes each container's own HOME (a tmpfs), so
# each gets its own version while the installs are shared. A third
# container with no pin runs the image's node.
pins="$(mktemp -d)"
trap 'docker volume rm -f "$vol" "$cvol" "$tvol" >/dev/null 2>&1 || true; rm -rf "$fix" "$pins"' EXIT
pin() {
  "${PRUN[@]}" sh -c "(mkdir -p \"\${MISE_DATA_DIR:-/tmp}\" && flock \"\${MISE_DATA_DIR:-/tmp}/.timothy-install.lock\" mise use --global 'node@$1') >/dev/null 2>&1 && node --version"
}
pin 18 > "$pins/a" & p1=$!
pin 20.18.0 > "$pins/b" & p2=$!
wait "$p1" || { echo "FAIL: node 18 pin" >&2; exit 1; }
wait "$p2" || { echo "FAIL: node 20 pin" >&2; exit 1; }
a="$(cat "$pins/a")"
b="$(cat "$pins/b")"
case "$a/$b" in
  v18.*/v20.18.0) echo "per-mission node pins ok: $a and $b on one volume" ;;
  *) echo "FAIL: node pins gave '$a' and '$b', want v18.x and v20.18.0" >&2; exit 1 ;;
esac
out="$("${PRUN[@]}" node --version)"
case "$out" in
  v24.*) echo "no pin falls back to the image node: $out" ;;
  *) echo "FAIL: no-pin node = '$out', want the image's v24.x" >&2; exit 1 ;;
esac

# Runtimes the removed per-language images used to bake install through
# mise under sandboxd's limits (manager.go: memory, cpus, pids, nofile,
# fsize 256 MiB, no core dumps, all caps dropped); keep in sync. A JDK
# tarball is the largest single file and must fit under fsize.
rvol="timothy-smoke-runtimes-$RANDOM$RANDOM"
docker volume create "$rvol" >/dev/null
trap 'docker volume rm -f "$vol" "$cvol" "$tvol" "$rvol" >/dev/null 2>&1 || true; rm -rf "$fix" "$pins"' EXIT
LIMITS=(--memory 2g --cpus 2 --pids-limit 256 --cap-drop ALL --security-opt no-new-privileges
  --ulimit nofile=4096:4096 --ulimit fsize=268435456:268435456 --ulimit core=0:0)
out="$(docker run --rm -u 65534:65534 "${LIMITS[@]}" "${SANDBOX_MOUNTS[@]}" -v "$rvol:$MISE_DIR" -v "$cvol:$CACHE_DIR" "$IMAGE" sh -c '
  mise install java@21 ruby@3.3 rust@stable >/dev/null 2>&1 || { echo "mise install failed"; exit 1; }
  mise exec java@21 -- java -version 2>&1 | head -n 1
  mise exec ruby@3.3 -- ruby --version
  mise exec rust@stable -- rustc --version
  mise exec rust@stable -- cargo --version')"
case "$out" in
  *'version "21'*'ruby 3.3.'*'rustc '*'cargo '*) printf 'runtimes under sandbox limits ok:\n%s\n' "$out" ;;
  *) echo "FAIL: java@21 ruby@3.3 rust@stable under the sandbox limits: $out" >&2; exit 1 ;;
esac
