#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../../.." && pwd)"

: "${IMAGE:?set IMAGE to an immutable descheduler image tag}"

docker build \
  -f "$REPO_ROOT/Dockerfile.experiment" \
  --build-arg VERSION="${VERSION:-experiment}" \
  -t "$IMAGE" \
  "$REPO_ROOT"
docker push "$IMAGE"
