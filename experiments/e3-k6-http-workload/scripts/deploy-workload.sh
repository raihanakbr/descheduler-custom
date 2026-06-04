#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE="${NAMESPACE:-e3-load}"

"${KUBECTL}" apply -f "${ROOT_DIR}/k8s/workload.yaml"
"${KUBECTL}" apply -f "${ROOT_DIR}/k8s/ingress.yaml"
"${KUBECTL}" -n "${NAMESPACE}" rollout status deploy/workload-hot --timeout=180s
"${KUBECTL}" -n "${NAMESPACE}" rollout status deploy/workload-warm --timeout=180s
"${KUBECTL}" -n "${NAMESPACE}" rollout status deploy/workload-mem --timeout=180s
"${KUBECTL}" -n "${NAMESPACE}" rollout status deploy/workload-idle-overrequest --timeout=180s
