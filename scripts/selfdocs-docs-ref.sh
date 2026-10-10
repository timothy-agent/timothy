#!/bin/sh
# Prints the docs ref a release build must use: v<version> when that tag
# already exists in the docs checkout (a rebuild of a released version),
# otherwise main.
#
# usage: selfdocs-docs-ref.sh <docs-checkout> <version>
set -eu

if [ $# -ne 2 ] || [ -z "$1" ] || [ -z "$2" ]; then
  echo "usage: $0 <docs-checkout> <version>" >&2
  exit 2
fi

if git -C "$1" rev-parse -q --verify "refs/tags/v$2^{commit}" >/dev/null; then
  echo "v$2"
else
  echo "main"
fi
