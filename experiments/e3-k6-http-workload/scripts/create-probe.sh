#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE="${NAMESPACE:-e3-load}"

"${KUBECTL}" -n "${NAMESPACE}" delete pod fragmentation-probe --ignore-not-found
"${KUBECTL}" apply -f "${ROOT_DIR}/k8s/probe.yaml"
"${KUBECTL}" -n "${NAMESPACE}" get pod fragmentation-probe -o wide
