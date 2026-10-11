#!/usr/bin/env bash
# Renders the chart with deploy/helm/tests/values-golden.yaml and diffs
# the output against deploy/helm/tests/golden/default.yaml, so a chart
# change shows up as a reviewable manifest diff. UPDATE=1 rewrites the
# golden file. The golden values name an existing Secret: generated
# secrets would differ on every render. Blank lines are dropped on
# both sides: helm 3 and helm 4 differ only in the blank lines they
# emit between documents.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="${REPO_ROOT}/deploy/helm/timothy"
VALUES="${REPO_ROOT}/deploy/helm/tests/values-golden.yaml"
GOLDEN="${REPO_ROOT}/deploy/helm/tests/golden/default.yaml"

render() {
  helm template timothy "${CHART}" --namespace timothy -f "${VALUES}" --kube-version 1.31.0 | sed '/^$/d'
}

if [[ "${UPDATE:-0}" == "1" ]]; then
  mkdir -p "$(dirname "${GOLDEN}")"
  render > "${GOLDEN}"
  echo "helm-golden: wrote ${GOLDEN#"${REPO_ROOT}"/}"
  exit 0
fi

if diff -u <(sed '/^$/d' "${GOLDEN}") <(render); then
  echo "helm-golden: PASS"
else
  echo "helm-golden: FAIL: rendered output differs from the golden file; review the diff and run UPDATE=1 make helm-golden" >&2
  exit 1
fi
