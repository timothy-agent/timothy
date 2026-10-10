#!/bin/sh
# Merges the capability-manifest pages and (optionally) the docs pages
# into one self-docs directory with a single manifest.json:
#   {version, sources, files (sorted), sha256 over file contents in
#   sorted order}. Used by `make selfdocs` and by release.yml.
#
# usage: selfdocs-bundle.sh --docs <dir|empty> --manifest <dir> --out <dir> --version <v>
set -eu

docs=""
manifest=""
out=""
version=""
while [ $# -gt 0 ]; do
  case "$1" in
    --docs) docs="${2-}"; shift 2 ;;
    --manifest) manifest="${2-}"; shift 2 ;;
    --out) out="${2-}"; shift 2 ;;
    --version) version="${2-}"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
if [ -z "$manifest" ] || [ -z "$out" ] || [ -z "$version" ]; then
  echo "usage: $0 --docs <dir|empty> --manifest <dir> --out <dir> --version <v>" >&2
  exit 2
fi
[ -d "$manifest" ] || { echo "manifest dir not found: $manifest" >&2; exit 1; }
if [ -n "$docs" ] && [ ! -d "$docs" ]; then
  echo "docs dir not found: $docs" >&2
  exit 1
fi

# Build into a temp dir beside the target so --out may equal --manifest
# and a failure leaves the previous bundle untouched.
out="${out%/}"
tmp="$(mktemp -d "${out}.tmp.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

sources='"manifest"'
for src in "$manifest" $docs; do
  for f in "$src"/*.md; do
    [ -e "$f" ] || continue
    name="$(basename "$f")"
    if [ -e "$tmp/$name" ]; then
      echo "duplicate page name: $name" >&2
      exit 1
    fi
    cp "$f" "$tmp/$name"
  done
  [ "$src" = "$manifest" ] || sources='"manifest","docs"'
done

names="$(cd "$tmp" && LC_ALL=C ls -1 | LC_ALL=C sort)"
if [ -z "$names" ]; then
  echo "no pages found" >&2
  exit 1
fi

hash_stdin() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | cut -d' ' -f1
  else
    shasum -a 256 | cut -d' ' -f1
  fi
}
sum="$(cd "$tmp" && echo "$names" | while IFS= read -r n; do cat "$n"; done | hash_stdin)"

list="$(echo "$names" | sed 's/.*/    "&"/' | sed '$!s/$/,/')"
printf '{\n  "version": "%s",\n  "sources": [%s],\n  "files": [\n%s\n  ],\n  "sha256": "%s"\n}\n' \
  "$version" "$sources" "$list" "$sum" > "$tmp/manifest.json"

rm -rf "$out"
mv "$tmp" "$out"
chmod 755 "$out"
trap - EXIT
echo "ok: $(echo "$names" | wc -l | tr -d ' ') pages, sha256 $sum"
