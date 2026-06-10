#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NS="${NS:-actual-usage-exp}"
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
OUTPUT_DIR="results/walkthrough/r1-load/$TIMESTAMP"
mkdir -p "$OUTPUT_DIR"
export OUTPUT_DIR NS

API_RPS="${API_RPS:-8}"
API_CPU_UNITS="${API_CPU_UNITS:-900}"
API_DURATION="${API_DURATION:-8m}"
API_VUS="${API_VUS:-60}"
API_MAX_VUS="${API_MAX_VUS:-160}"
STABILIZE_SECONDS="${STABILIZE_SECONDS:-30}"
POST_EVENT_SECONDS="${POST_EVENT_SECONDS:-30}"
THRESHOLD="${THRESHOLD:-0.80}"

K6_PID=""
WATCH_PID=""

log() {
  printf '[walkthrough-r1] %s %s\n' "$(date +%H:%M:%S)" "$*"
}

stop_process() {
  local pid="${1:-}" signal="${2:-TERM}" timeout="${3:-10}"
  [[ -n "$pid" ]] || return 0
  if kill -0 "$pid" >/dev/null 2>&1; then
    kill "-$signal" "$pid" >/dev/null 2>&1 || true
    local deadline=$((SECONDS + timeout))
    while kill -0 "$pid" >/dev/null 2>&1; do
      (( SECONDS >= deadline )) && { kill -KILL "$pid" >/dev/null 2>&1 || true; break; }
      sleep 1
    done
    wait "$pid" 2>/dev/null || true
  fi
}

cleanup() {
  log "cleaning up"
  stop_process "${K6_PID:-}" INT 15
  stop_process "${WATCH_PID:-}" TERM 5
}
trap cleanup EXIT

: "${DESCHEDULER_IMAGE:?DESCHEDULER_IMAGE is required}"
: "${WORKLOAD_IMAGE:=docker.io/matthewhjt/workload-http:actual-usage-v1}"
export DESCHEDULER_IMAGE WORKLOAD_IMAGE

log "step 1/10: cleanup previous state"
"$ROOT/scripts/cleanup.sh"

log "step 2/10: preflight"
"$ROOT/scripts/preflight.sh" | tee "$OUTPUT_DIR/preflight.txt"

log "step 3/10: deploy layout"
"$ROOT/scripts/setup-layout.sh" | tee "$OUTPUT_DIR/setup.log"

source_node="$(cat "$OUTPUT_DIR/source-node.txt")"
api_pod="$(cat "$OUTPUT_DIR/api-pod.txt")"
source_ip="$(kubectl get node "$source_node" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')"
api_port="$(kubectl -n "$NS" get svc workload-api -o jsonpath='{.spec.ports[0].nodePort}')"
api_url="http://${API_HOST:-$source_ip}:${api_port}"

log "source node: $source_node"
log "api pod: $api_pod"
log "api url: $api_url"

log "step 4/10: before snapshot"
"$ROOT/scripts/snapshot.sh" before "$OUTPUT_DIR"

log "step 5/10: start k6 load (${API_RPS} RPS, ${API_DURATION})"
python3 "$ROOT/scripts/watch-pods.py" --namespace "$NS" --output "$OUTPUT_DIR/pod-lifecycle.tsv" &
WATCH_PID=$!

API_URL="$api_url" \
API_DURATION="$API_DURATION" \
API_RPS="$API_RPS" \
API_CPU_UNITS="$API_CPU_UNITS" \
API_VUS="$API_VUS" \
API_MAX_VUS="$API_MAX_VUS" \
k6 run \
  --out "json=$OUTPUT_DIR/api-load.json" \
  --summary-export "$OUTPUT_DIR/api-load-summary.json" \
  "$ROOT/k6/api-load.js" > "$OUTPUT_DIR/api-load.log" 2>&1 &
K6_PID=$!
log "k6 started (PID $K6_PID)"

log "step 6/10: stabilizing load for ${STABILIZE_SECONDS}s"
sleep "$STABILIZE_SECONDS"

log "api pod metrics after stabilization:"
kubectl -n "$NS" top pod -l app=workload-api 2>&1 | tee "$OUTPUT_DIR/api-pod-metrics.txt"

log "step 7/10: waiting for CPU ratio > ${THRESHOLD}"
python3 "$ROOT/scripts/wait-threshold.py" \
  --namespace "$NS" \
  --pod "$api_pod" \
  --resource cpu \
  --threshold "$THRESHOLD" \
  --consecutive 2 \
  --interval 10 \
  --timeout 120 \
  --output "$OUTPUT_DIR/threshold-samples.tsv"

log "CPU threshold confirmed (2 consecutive samples above ${THRESHOLD})"
tail -3 "$OUTPUT_DIR/threshold-samples.tsv"

log "step 8/10: run descheduler R1"
date -Ins > "$OUTPUT_DIR/event-time.txt"
"$ROOT/scripts/run-descheduler.sh" "$ROOT/policies/r1-rdc2-actual.yaml" "$OUTPUT_DIR"

log "descheduler logs:"
grep -E "(Evicted pod|Eviction decision|pod selected|Processing node|BLOCKED|ALLOWED)" "$OUTPUT_DIR/descheduler.log" || true

log "step 9/10: waiting ${POST_EVENT_SECONDS}s for disruption capture"
sleep "$POST_EVENT_SECONDS"

log "api pod after eviction:"
kubectl -n "$NS" get pods -o wide --sort-by=.spec.nodeName

log "after snapshot"
"$ROOT/scripts/snapshot.sh" after "$OUTPUT_DIR"

log "step 10/10: stop k6 and generate summary"
stop_process "$K6_PID" INT 15
K6_PID=""
stop_process "$WATCH_PID" TERM 5
WATCH_PID=""
trap - EXIT

log "=== RESULTS ==="

echo ""
echo "--- Eviction Target ---"
grep -E "(Evicted pod|Eviction decision)" "$OUTPUT_DIR/descheduler.log" || echo "(no eviction)"

echo ""
echo "--- k6 Summary ---"
if [[ -f "$OUTPUT_DIR/api-load-summary.json" ]]; then
  python3 -c "
import json, sys
with open('$OUTPUT_DIR/api-load-summary.json') as f:
    m = json.load(f)['metrics']
dur = m.get('http_req_duration', {})
fail = m.get('http_req_failed', {})
reqs = m.get('http_reqs', {})
reqs_count = reqs.get('count', '?')
reqs_rate = reqs.get('rate', None)
dur_p95 = dur.get('p(95)', None)
fail_rate = fail.get('value', None)
print(f\"  requests total : {reqs_count}\")
print(f\"  request rate   : {reqs_rate:.1f}/s\" if isinstance(reqs_rate, (int, float)) else \"  request rate   : ?\")
print(f\"  p95 latency    : {dur_p95:.1f}ms\" if isinstance(dur_p95, (int, float)) else \"  p95 latency    : ?\")
print(f\"  failure rate   : {fail_rate:.2%}\" if isinstance(fail_rate, (int, float)) else \"  failure rate   : ?\")
"
else
  echo "  (no k6 summary found)"
fi

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
