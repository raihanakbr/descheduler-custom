#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${IMAGE:-docker.io/matthewhjt/workload-http:actual-usage-v1}"
BUILDER="${BUILDER:-docker}"

"$BUILDER" build -t "$IMAGE" "$ROOT/cmd/workload-http"
"$BUILDER" push "$IMAGE"
echo "Published $IMAGE"
