#!/usr/bin/env bash
# Sourced by canary-ecosystems.sh: fork reset and cleanup through the
# GitHub API (gh), and the daily osv-scanner offline database refresh.
# Every argument is validated and passed as argv, never through eval.

matrix_valid_repo() {
  [[ "$1" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]
}

matrix_valid_branch() {
  [[ "$1" =~ ^[A-Za-z0-9._/-]+$ && "$1" != *..* && "$1" != /* && "$1" != */ && "$1" != -* ]]
}

matrix_valid_sha() {
  [[ "$1" =~ ^[0-9a-f]{40}$ ]]
}

# matrix_reset_fork moves repo's branch to sha with a forced ref update
# through the API; never a git push.
matrix_reset_fork() {
  local repo="$1" branch="$2" sha="$3"
  if ! matrix_valid_repo "${repo}" || ! matrix_valid_branch "${branch}" || ! matrix_valid_sha "${sha}"; then
    echo "matrix: refusing reset with repo='${repo}' branch='${branch}' sha='${sha}'" >&2
    return 2
  fi
  gh api -X PATCH "repos/${repo}/git/refs/heads/${branch}" -f "sha=${sha}" -F force=true --silent
}

# matrix_cleanup_branch closes every open PR whose head is branch and
# deletes the branch when it exists. Refuses the fork's default branch.
matrix_cleanup_branch() {
  local repo="$1" branch="$2" default_branch="$3" numbers n rc=0
  if ! matrix_valid_repo "${repo}" || ! matrix_valid_branch "${branch}"; then
    echo "matrix: refusing cleanup with repo='${repo}' branch='${branch}'" >&2
    return 2
  fi
  if [[ "${branch}" == "${default_branch}" ]]; then
    echo "matrix: refusing to delete default branch ${branch} of ${repo}" >&2
    return 2
  fi
  numbers="$(gh api "repos/${repo}/pulls?state=open&head=${repo%%/*}:${branch}" --jq '.[].number')" || return 1
  for n in ${numbers}; do
    [[ "${n}" =~ ^[0-9]+$ ]] || continue
    gh api -X PATCH "repos/${repo}/pulls/${n}" -f state=closed --silent || rc=1
  done
  if gh api "repos/${repo}/git/ref/heads/${branch}" --silent 2>/dev/null; then
    gh api -X DELETE "repos/${repo}/git/refs/heads/${branch}" --silent || rc=1
  fi
  return "${rc}"
}

# MATRIX_OSV_FETCH runs inside the sandbox image as sh -c with args
# <cache root> <day> <ecosystem>...: it downloads each ecosystem's
# database to <cache root>/osv-scanner/<ecosystem>/all.zip (osv-scanner's
# offline layout under XDG_CACHE_HOME) unless already fetched that day.
# shellcheck disable=SC2016
MATRIX_OSV_FETCH='set -eu
root="$1/osv-scanner"; day="$2"; shift 2
for eco in "$@"; do
  d="$root/$eco"
  if [ "$(cat "$d/.day" 2>/dev/null || true)" = "$day" ]; then
    echo "osv db: $eco already fetched for $day"
    continue
  fi
  mkdir -p "$d"
  curl -fsSL --retry 3 -o "$d/all.zip.part" "https://osv-vulnerabilities.storage.googleapis.com/$eco/all.zip"
  mv "$d/all.zip.part" "$d/all.zip"
  printf "%s\n" "$day" > "$d/.day"
  echo "osv db: $eco fetched for $day"
done'

# matrix_refresh_osv_db fetches the databases into the sandbox caches
# volume, which sandboxd mounts at XDG_CACHE_HOME in mission containers.
matrix_refresh_osv_db() {
  local volume="$1" image="$2" day="$3" eco
  shift 3
  for eco in "$@"; do
    if [[ ! "${eco}" =~ ^[A-Za-z0-9.:_-]+$ ]]; then
      echo "matrix: refusing osv ecosystem '${eco}'" >&2
      return 2
    fi
  done
  if [[ ! "${day}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
    echo "matrix: refusing osv day '${day}'" >&2
    return 2
  fi
  docker run --rm -v "${volume}:/cache" "${image}" sh -c "${MATRIX_OSV_FETCH}" sh /cache "${day}" "$@"
}
