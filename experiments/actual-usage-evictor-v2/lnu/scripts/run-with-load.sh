#!/usr/bin/env bash
set -euo pipefail

SYSTEM="${1:?usage: run-with-load.sh <L0|L1>}"
case "$SYSTEM" in L0|L1) ;; *) echo "invalid system: $SYSTEM" >&2; exit 2 ;; esac

LNU_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PARENT_ROOT="$(cd "$LNU_ROOT/.." && pwd)"
NS="actual-usage-exp"
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
OUTPUT_DIR="$LNU_ROOT/results/${SYSTEM,,}-load/$TIMESTAMP"
mkdir -p "$OUTPUT_DIR"
export OUTPUT_DIR NS

API_RPS="${API_RPS:-8}"
API_CPU_UNITS="${API_CPU_UNITS:-900}"
API_DURATION="${API_DURATION:-8m}"
API_VUS="${API_VUS:-60}"
API_MAX_VUS="${API_MAX_VUS:-160}"
STABILIZE_SECONDS="${STABILIZE_SECONDS:-30}"
POST_EVENT_SECONDS="${POST_EVENT_SECONDS:-45}"
THRESHOLD="${THRESHOLD:-0.80}"

K6_PID=""
WATCH_PID=""

log() {
  printf '[lnu-%s] %s %s\n' "${SYSTEM,,}" "$(date +%H:%M:%S)" "$*"
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

cleanup_processes() {
  stop_process "${K6_PID:-}" INT 15
  stop_process "${WATCH_PID:-}" TERM 5
}
trap cleanup_processes EXIT

: "${DESCHEDULER_IMAGE:=docker.io/matthewhjt/descheduler-custom:actual-usage-v2}"
: "${WORKLOAD_IMAGE:=docker.io/matthewhjt/workload-http:actual-usage-v1}"
export DESCHEDULER_IMAGE WORKLOAD_IMAGE

log "step 1/11: cleanup previous state"
"$PARENT_ROOT/scripts/cleanup.sh"

log "step 2/11: parent preflight"
"$PARENT_ROOT/scripts/preflight.sh" | tee "$OUTPUT_DIR/preflight.txt"

log "step 3/11: deploy and validate LNU layout"
"$LNU_ROOT/scripts/setup-layout.sh" | tee "$OUTPUT_DIR/setup.log"

source_node="$(cat "$OUTPUT_DIR/source-node.txt")"
api_pod="$(cat "$OUTPUT_DIR/api-pod.txt")"
api_uid="$(kubectl -n "$NS" get pod "$api_pod" -o jsonpath='{.metadata.uid}')"
source_ip="$(kubectl get node "$source_node" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')"
api_port="$(kubectl -n "$NS" get svc workload-api -o jsonpath='{.spec.ports[0].nodePort}')"
api_url="http://${API_HOST:-$source_ip}:${api_port}"

cat > "$OUTPUT_DIR/run.env" <<EOF
SYSTEM=$SYSTEM
SOURCE_NODE=$source_node
API_POD=$api_pod
API_UID=$api_uid
API_URL=$api_url
PRE_EVENT_SECONDS=30
POST_EVENT_SECONDS=$POST_EVENT_SECONDS
EOF

log "step 4/11: before snapshot"
"$PARENT_ROOT/scripts/snapshot.sh" before "$OUTPUT_DIR"

log "step 5/11: start lifecycle watcher and k6"
python3 "$PARENT_ROOT/scripts/watch-pods.py" \
  --namespace "$NS" --output "$OUTPUT_DIR/pod-lifecycle.tsv" &
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
  "$PARENT_ROOT/k6/api-load.js" > "$OUTPUT_DIR/api-load.log" 2>&1 &
K6_PID=$!

log "step 6/11: stabilize load for ${STABILIZE_SECONDS}s"
sleep "$STABILIZE_SECONDS"
kubectl -n "$NS" top pod -l app=workload-api 2>&1 | tee "$OUTPUT_DIR/api-pod-metrics.txt"

log "step 7/11: require two CPU samples at or above ${THRESHOLD}"
python3 "$PARENT_ROOT/scripts/wait-threshold.py" \
  --namespace "$NS" \
  --pod "$api_pod" \
  --resource cpu \
  --threshold "$THRESHOLD" \
  --consecutive 2 \
  --interval 10 \
  --timeout 120 \
  --output "$OUTPUT_DIR/threshold-samples.tsv"

log "step 8/11: capture event snapshot and run $SYSTEM"
date -Ins > "$OUTPUT_DIR/event-time.txt"
"$PARENT_ROOT/scripts/snapshot.sh" event "$OUTPUT_DIR"
if [[ "$SYSTEM" == "L0" ]]; then
  policy="$LNU_ROOT/policies/l0-lnu.yaml"
else
  policy="$LNU_ROOT/policies/l1-lnu-actual.yaml"
fi
"$PARENT_ROOT/scripts/run-descheduler.sh" "$policy" "$OUTPUT_DIR"
grep -E "(Evicted pod|blocking eviction|ActualUsageEvictor|Node has been classified)" \
  "$OUTPUT_DIR/descheduler.log" || true

log "step 9/11: wait ${POST_EVENT_SECONDS}s for replacements and traffic capture"
sleep "$POST_EVENT_SECONDS"
kubectl -n "$NS" wait --for=condition=available deployment --all --timeout=180s

log "step 10/11: after snapshot and result validation"
"$PARENT_ROOT/scripts/snapshot.sh" after "$OUTPUT_DIR"
python3 "$LNU_ROOT/scripts/validate-result.py" \
  --system "$SYSTEM" \
  --pods "$OUTPUT_DIR/pods-after.json" \
  --log "$OUTPUT_DIR/descheduler.log" \
  --sources "$OUTPUT_DIR/source-nodes.txt" \
  --destinations "$OUTPUT_DIR/destination-nodes.txt" \
  --namespace "$NS" \
  --api-uid "$api_uid" \
  | tee "$OUTPUT_DIR/result-validation.json"

log "step 11/11: stop load and generate summary"
cleanup_processes
K6_PID=""
WATCH_PID=""
trap - EXIT

python3 "$PARENT_ROOT/scripts/summarize-run.py" "$OUTPUT_DIR" \
  | tee "$OUTPUT_DIR/summary.json"

echo
echo "--- Result Validation ---"
cat "$OUTPUT_DIR/result-validation.json"
echo
echo "--- Cluster Metrics ---"
python3 - "$OUTPUT_DIR" <<'PY'
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
for phase in ("before", "after"):
    data = json.loads((root / f"cluster-metrics-{phase}.json").read_text())
    print(
        f"{phase:>6}: active={data.get('N_active')} "
        f"stranding={data.get('S')} balanced_headroom={data.get('H_balanced')}"
    )
PY
echo
echo "--- Final Layout ---"
cat "$OUTPUT_DIR/layout-after.txt"

log "output saved to $OUTPUT_DIR"
