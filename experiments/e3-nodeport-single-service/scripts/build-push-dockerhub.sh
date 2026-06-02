#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
IMAGE="${IMAGE:-docker.io/matthewhjt/workload-http:manual-v1}"
BUILDER="${BUILDER:-docker}"

"${BUILDER}" build -t "${IMAGE}" "${ROOT_DIR}/e3-k6-http-workload/cmd/workload-http"
"${BUILDER}" push "${IMAGE}"

