#!/usr/bin/env bash
set -euo pipefail

IMAGE="${IMAGE:-workload-http:local}"
WORKERS="${WORKERS:-}"
SSH_USER="${SSH_USER:-ubuntu}"
SSH_OPTS="${SSH_OPTS:-}"
BUILDER="${BUILDER:-docker}"
REMOTE_TAR="${REMOTE_TAR:-/tmp/workload-http-image.tar}"

if [[ -z "${WORKERS}" ]]; then
  echo "WORKERS is required, for example: WORKERS='10.0.1.11 10.0.1.12 10.0.1.13'" >&2
  exit 2
fi

tmp="$(mktemp -t workload-http-image.XXXXXX.tar)"
trap 'rm -f "${tmp}"' EXIT

"${BUILDER}" save "${IMAGE}" -o "${tmp}"

for worker in ${WORKERS}; do
  echo "Importing ${IMAGE} into ${worker}"
  scp ${SSH_OPTS} "${tmp}" "${SSH_USER}@${worker}:${REMOTE_TAR}"
  ssh ${SSH_OPTS} "${SSH_USER}@${worker}" "sudo ctr -n k8s.io images import ${REMOTE_TAR} && rm -f ${REMOTE_TAR}"
done
