#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
IMAGE="${IMAGE:-workload-http:local}"
BUILDER="${BUILDER:-docker}"

"${BUILDER}" build -t "${IMAGE}" "${ROOT_DIR}/cmd/workload-http"
