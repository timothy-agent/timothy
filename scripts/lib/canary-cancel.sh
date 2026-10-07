#!/usr/bin/env bash
# Sourced by the canary scripts. Cancels the canary's mission through
# the API when the script exits without PASS (timeout, failure, parked
# mission, INT/TERM), so a dead run does not keep a model busy.
#
# Needs these globals set by the caller before canary_cancel_arm runs:
# BASE_URL, auth (curl header array), id (mission id).
# Call canary_cancel_arm right after the mission is created and
# canary_cancel_disarm at the PASS point, before the PASS cleanup.
# shellcheck disable=SC2154

CANARY_CANCEL_ARMED=0

_canary_cancel_on_exit() {
  local rc=$?
  trap - EXIT INT TERM
  if (( CANARY_CANCEL_ARMED )); then
    CANARY_CANCEL_ARMED=0
    local code
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST \
      "${auth[@]}" "${BASE_URL}/v1/missions/${id}/cancel" || true)"
    # 409: the mission is already terminal, nothing left to cancel.
    case "${code}" in
      2??|409) echo "canary: cancelled mission ${id}" >&2 ;;
      *) echo "canary: WARNING - cancel of mission ${id} failed (http ${code:-none}); cancel it manually" >&2
         echo "${id}" >&2 ;;
    esac
  fi
  exit "${rc}"
}

canary_cancel_arm() {
  CANARY_CANCEL_ARMED=1
  trap _canary_cancel_on_exit EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
}

canary_cancel_disarm() {
  CANARY_CANCEL_ARMED=0
}
