#!/usr/bin/env bash
# Smoke-test the PHP sandbox image: PHP 8.1 to 8.4 with the required
# extensions, 8.4 as default, per-mission minor selection under the
# hardened sandbox mounts (D-127), and composer as the sandbox uid.
# SANDBOX_PHP_SMOKE_LARAVEL=1 also creates and tests a Laravel 10 app
# on 8.1 and a Laravel 12 app on 8.4 (slow, needs network).
# Usage: scripts/sandbox-php-smoke.sh [image]
set -euo pipefail

IMAGE="${1:-timothy-sandbox-php:latest}"
MINORS=(8.1 8.2 8.3 8.4)
RUN=(docker run --rm -u 65534:65534 "$IMAGE")
# Mirrors sandboxd: read-only rootfs, tmpfs /tmp and HOME, fixed PATH.
SANDBOX_PATH=/home/sandbox/.mise/shims:/home/sandbox/.local/bin:/home/sandbox/.npm-global/bin:/home/sandbox/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
VRUN=(docker run --rm -u 65534:65534 --read-only --cap-drop ALL --security-opt no-new-privileges
  --tmpfs /tmp:rw,exec,nosuid,nodev,size=512m
  --tmpfs /home/sandbox:rw,exec,nosuid,nodev,uid=65534,gid=65534,size=1g
  -e "PATH=$SANDBOX_PATH" -e HOME=/home/sandbox -w /tmp "$IMAGE")
# Same links as buildPHPSelectCmd in internal/brain/missions/environment.go.
select_php() {
  printf 'mkdir -p /home/sandbox/.local/bin; for b in php phar phar.phar; do if [ -x "/usr/bin/${b}%s" ]; then ln -sf "/usr/bin/${b}%s" "/home/sandbox/.local/bin/${b}"; fi; done' "$1" "$1"
}

ver="$("${RUN[@]}" php -r 'echo PHP_VERSION;')"
case "$ver" in
  8.4.*) echo "default php ok: $ver" ;;
  *) echo "FAIL: expected default PHP 8.4.x, got $ver" >&2; exit 1 ;;
esac

for v in "${MINORS[@]}"; do
  got="$("${RUN[@]}" "php$v" -r 'echo PHP_VERSION;')"
  case "$got" in
    "$v".*) ;;
    *) echo "FAIL: php$v reports $got" >&2; exit 1 ;;
  esac
  mods="$("${RUN[@]}" "php$v" -m | tr 'A-Z' 'a-z')"
  for ext in pdo_sqlite sqlite3 mbstring xml dom curl zip intl tokenizer ctype fileinfo openssl pdo json bcmath; do
    if ! grep -qx "$ext" <<<"$mods"; then
      echo "FAIL: php$v missing extension: $ext" >&2
      exit 1
    fi
  done
  echo "php $got extensions ok"
done

"${RUN[@]}" composer --version --no-interaction
echo "composer ok (uid 65534)"

got="$("${VRUN[@]}" sh -c "$(select_php 8.1) && php -r 'echo PHP_VERSION;' && composer --version --no-interaction >/dev/null")"
case "$got" in
  8.1.*) echo "hardened select ok: php $got, composer runs on it" ;;
  *) echo "FAIL: selecting 8.1 under sandbox mounts gave $got" >&2; exit 1 ;;
esac

if [ "${SANDBOX_PHP_SMOKE_LARAVEL:-0}" = "1" ]; then
  "${VRUN[@]}" sh -c "$(select_php 8.1) && composer create-project 'laravel/laravel:^10.0' app --no-interaction --prefer-dist --quiet && cd app && php artisan test"
  echo "laravel 10 on php 8.1 ok"
  "${VRUN[@]}" sh -c "composer create-project 'laravel/laravel:^12.0' app --no-interaction --prefer-dist --quiet && cd app && php artisan test"
  echo "laravel 12 on php 8.4 ok"
fi
