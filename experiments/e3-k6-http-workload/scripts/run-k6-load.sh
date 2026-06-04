#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
BASE_URL="${BASE_URL:-}"
RESULT_DIR="${RESULT_DIR:-${ROOT_DIR}/results/$(date -u +%Y%m%dT%H%M%SZ)}"

if [[ -z "${BASE_URL}" ]]; then
  echo "BASE_URL is required, for example: BASE_URL=http://10.0.1.20" >&2
  exit 2
fi

mkdir -p "${RESULT_DIR}"
BASE_URL="${BASE_URL}" k6 run \
  --summary-export "${RESULT_DIR}/k6-summary.json" \
  "${ROOT_DIR}/k6/scenario.js" | tee "${RESULT_DIR}/k6-output.txt"
