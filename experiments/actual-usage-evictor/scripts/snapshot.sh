#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LABEL="$1"
OUTPUT_DIR="$2"
NS="${NS:-actual-usage-exp}"

kubectl get nodes -o json > "$OUTPUT_DIR/nodes-${LABEL}.json"
kubectl get pods -A -o json > "$OUTPUT_DIR/pods-${LABEL}.json"
kubectl top nodes > "$OUTPUT_DIR/top-nodes-${LABEL}.txt" 2>&1 || true
kubectl top pods -n "$NS" --containers > "$OUTPUT_DIR/top-pods-${LABEL}.txt" 2>&1 || true
kubectl -n "$NS" get pods -o wide --sort-by=.spec.nodeName > "$OUTPUT_DIR/layout-${LABEL}.txt"
python3 "$ROOT/scripts/cluster-metrics.py" \
  --nodes "$OUTPUT_DIR/nodes-${LABEL}.json" \
  --pods "$OUTPUT_DIR/pods-${LABEL}.json" \
  --namespace "$NS" \
  --label "$LABEL" > "$OUTPUT_DIR/cluster-metrics-${LABEL}.json"
