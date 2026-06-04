#!/usr/bin/env bash
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE="${NAMESPACE:-e3-load}"
LABEL="${1:-snapshot}"
RESULT_DIR="${RESULT_DIR:-$(pwd)/e3-k6-results}"

mkdir -p "${RESULT_DIR}"
"${KUBECTL}" get nodes -o wide > "${RESULT_DIR}/nodes-${LABEL}.txt" 2>&1 || true
"${KUBECTL}" get nodes -o json > "${RESULT_DIR}/nodes-${LABEL}.json" 2>&1 || true
"${KUBECTL}" get pods -n "${NAMESPACE}" -o wide > "${RESULT_DIR}/pods-${LABEL}.txt" 2>&1 || true
"${KUBECTL}" get pods -n "${NAMESPACE}" -o json > "${RESULT_DIR}/pods-${LABEL}.json" 2>&1 || true
"${KUBECTL}" top nodes > "${RESULT_DIR}/top-nodes-${LABEL}.txt" 2>&1 || true
"${KUBECTL}" top pods -n "${NAMESPACE}" > "${RESULT_DIR}/top-pods-${LABEL}.txt" 2>&1 || true
"${KUBECTL}" get events -n "${NAMESPACE}" --sort-by=.lastTimestamp > "${RESULT_DIR}/events-${LABEL}.txt" 2>&1 || true
