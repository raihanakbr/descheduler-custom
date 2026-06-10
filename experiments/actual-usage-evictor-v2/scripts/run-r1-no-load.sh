#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NS="${NS:-actual-usage-exp}"
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
OUTPUT_DIR="results/walkthrough/r1-no-load/$TIMESTAMP"
mkdir -p "$OUTPUT_DIR"
export OUTPUT_DIR NS

WAIT_METRICS_SECONDS="${WAIT_METRICS_SECONDS:-30}"
POST_EVENT_SECONDS="${POST_EVENT_SECONDS:-15}"

log() {
  printf '[walkthrough-r1-no-load] %s %s\n' "$(date +%H:%M:%S)" "$*"
}

: "${DESCHEDULER_IMAGE:?DESCHEDULER_IMAGE is required}"
: "${WORKLOAD_IMAGE:=docker.io/matthewhjt/workload-http:actual-usage-v1}"
export DESCHEDULER_IMAGE WORKLOAD_IMAGE

log "step 1/7: cleanup previous state"
"$ROOT/scripts/cleanup.sh"

log "step 2/7: preflight"
"$ROOT/scripts/preflight.sh" | tee "$OUTPUT_DIR/preflight.txt"

log "step 3/7: deploy layout"
"$ROOT/scripts/setup-layout.sh" | tee "$OUTPUT_DIR/setup.log"

source_node="$(cat "$OUTPUT_DIR/source-node.txt")"
api_pod="$(cat "$OUTPUT_DIR/api-pod.txt")"

log "source node: $source_node"
log "api pod: $api_pod"

log "step 4/7: before snapshot"
"$ROOT/scripts/snapshot.sh" before "$OUTPUT_DIR"

log "step 5/7: waiting ${WAIT_METRICS_SECONDS}s for metrics server"
sleep "$WAIT_METRICS_SECONDS"

log "api pod metrics (should be idle, no load):"
kubectl -n "$NS" top pod -l app=workload-api 2>&1 | tee "$OUTPUT_DIR/api-pod-metrics.txt"

log "step 6/7: run descheduler R1 (with ActualUsageEvictor)"
date -Ins > "$OUTPUT_DIR/event-time.txt"
"$ROOT/scripts/run-descheduler.sh" "$ROOT/policies/r1-rdc2-actual.yaml" "$OUTPUT_DIR"

log "descheduler logs:"
grep -E "(Evicted pod|Eviction decision|pod selected|Processing node|BLOCKED|ALLOWED)" "$OUTPUT_DIR/descheduler.log" || true

log "step 7/7: post-event capture and summary"
sleep "$POST_EVENT_SECONDS"

log "api pod after eviction:"
kubectl -n "$NS" get pods -o wide --sort-by=.spec.nodeName

log "after snapshot"
"$ROOT/scripts/snapshot.sh" after "$OUTPUT_DIR"

log "=== RESULTS ==="

echo ""
echo "--- Eviction Target ---"
grep -E "(Evicted pod|Eviction decision)" "$OUTPUT_DIR/descheduler.log" || echo "(no eviction)"

echo ""
echo "--- ActualUsageEvictor Decision ---"
grep -E "(ActualUsageEvictor|ALLOWED|BLOCKED)" "$OUTPUT_DIR/descheduler.log" || echo "(no ActualUsageEvictor check)"

echo ""
echo "--- Stranding ---"
echo "Before:"
python3 -c "
import json
with open('$OUTPUT_DIR/cluster-metrics-before.json') as f:
    d = json.load(f)
print(f\"  stranding: {d.get('S', '?')}\")
" 2>/dev/null || echo "  (not available)"
echo "After:"
python3 -c "
import json
with open('$OUTPUT_DIR/cluster-metrics-after.json') as f:
    d = json.load(f)
print(f\"  stranding: {d.get('S', '?')}\")
" 2>/dev/null || echo "  (not available)"

echo ""
echo "--- Final Layout ---"
cat "$OUTPUT_DIR/layout-after.txt" 2>/dev/null || true

log "output saved to $OUTPUT_DIR"
log "done"
