#!/usr/bin/env bash
# Records a claude-cli stream-json fixture for internal/brain/missions/executor
# testdata, then redacts session ids, uuids, and absolute paths before they
# are committed. Costs a few cents per run (haiku, trivial prompts).
#
# Usage: scripts/record-executor-fixture.sh <scenario> <workdir> -- <claude-args...>
#
# Example:
#   scripts/record-executor-fixture.sh basic /tmp/fixture-work/basic -- \
#     -p 'Create a file named hello.txt containing the word hello, then report done.' \
#     --output-format stream-json --verbose --model haiku \
#     --permission-mode dontAsk --allowedTools Write
set -euo pipefail

if [ "$#" -lt 4 ] || [ "$3" != "--" ]; then
  echo "usage: $0 <scenario> <workdir> -- <claude-args...>" >&2
  exit 1
fi

scenario="$1"
workdir="$2"
shift 3

dest_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/internal/brain/missions/executor/testdata/claude-2.1.223"
mkdir -p "$workdir" "$dest_dir"

raw="$workdir/$scenario.raw.ndjson"
(cd "$workdir" && claude "$@" >"$raw" 2>"$workdir/$scenario.stderr") || true

# Redact: session/uuid fields -> stable placeholders, absolute paths -> a
# placeholder. Costs/usage numbers are left intact (shape, not secrets).
sed \
  -e "s#$workdir#\$WORK#g" \
  -e "s#$HOME/[^\"]*#\$HOME_PATH#g" \
  -e 's/"session_id":"[^"]*"/"session_id":"SESSION_ID"/g' \
  -e 's/"uuid":"[^"]*"/"uuid":"UUID"/g' \
  -e 's/"hook_id":"[^"]*"/"hook_id":"HOOK_ID"/g' \
  -e 's/"id":"msg_[^"]*"/"id":"MSG_ID"/g' \
  -e 's/"id":"toolu_[^"]*"/"id":"TOOL_USE_ID"/g' \
  -e 's/"tool_use_id":"toolu_[^"]*"/"tool_use_id":"TOOL_USE_ID"/g' \
  -e 's/"request_id":"req_[^"]*"/"request_id":"REQUEST_ID"/g' \
  "$raw" > "$dest_dir/$scenario.ndjson"

echo "wrote $dest_dir/$scenario.ndjson"
