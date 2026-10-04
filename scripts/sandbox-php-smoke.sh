#!/usr/bin/env bash
# Smoke-test the PHP sandbox image: PHP 8.4, required extensions, and
# composer as the sandbox uid. SANDBOX_PHP_SMOKE_LARAVEL=1 also runs a
# Laravel create-project + test (slow, needs network).
# Usage: scripts/sandbox-php-smoke.sh [image]
set -euo pipefail

IMAGE="${1:-timothy-sandbox-php:latest}"
RUN=(docker run --rm -u 65534:65534 "$IMAGE")

ver="$("${RUN[@]}" php -r 'echo PHP_VERSION;')"
case "$ver" in
  8.4.*) echo "php version ok: $ver" ;;
  *) echo "FAIL: expected PHP 8.4.x, got $ver" >&2; exit 1 ;;
esac

mods="$("${RUN[@]}" php -m | tr 'A-Z' 'a-z')"
for ext in pdo_sqlite sqlite3 mbstring xml dom curl zip intl tokenizer ctype fileinfo openssl pdo json bcmath; do
  if ! grep -qx "$ext" <<<"$mods"; then
    echo "FAIL: missing php extension: $ext" >&2
    exit 1
  fi
done
echo "php extensions ok"

"${RUN[@]}" composer --version --no-interaction
echo "composer ok (uid 65534)"

if [ "${SANDBOX_PHP_SMOKE_LARAVEL:-0}" = "1" ]; then
  docker run --rm -u 65534:65534 -w /tmp "$IMAGE" sh -c \
    'composer create-project laravel/laravel app --no-interaction --prefer-dist --quiet && cd app && php artisan test'
  echo "laravel smoke ok"
fi
