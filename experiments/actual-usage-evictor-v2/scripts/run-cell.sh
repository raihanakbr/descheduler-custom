#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RESOURCE="${1:?usage: run-cell.sh <cpu|memory> <N0|R0|R1|H0|H1> <repeat>}"
SYSTEM="${2:?usage: run-cell.sh <cpu|memory> <N0|R0|R1|H0|H1> <repeat>}"
REPEAT="${3:?usage: run-cell.sh <cpu|memory> <N0|R0|R1|H0|H1> <repeat>}"

case "$RESOURCE" in cpu|memory) ;; *) echo "invalid resource: $RESOURCE" >&2; exit 2 ;; esac
case "$SYSTEM" in N0|R0|R1|H0|H1) ;; *) echo "invalid system: $SYSTEM" >&2; exit 2 ;; esac

NS="${NS:-actual-usage-exp}"
LOAD_PATTERN="${LOAD_PATTERN:-sustained}"
PRE_EVENT_SECONDS="${PRE_EVENT_SECONDS:-60}"
POST_EVENT_SECONDS="${POST_EVENT_SECONDS:-120}"
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
OUTPUT_DIR="$ROOT/results/$LOAD_PATTERN/$RESOURCE/$SYSTEM/repeat-$REPEAT/$TIMESTAMP"
mkdir -p "$OUTPUT_DIR"
export OUTPUT_DIR NS

GIT_COMMIT="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || printf 'unknown')"
GIT_DIRTY=false
if ! git -C "$ROOT" diff --quiet 2>/dev/null ||
   ! git -C "$ROOT" diff --cached --quiet 2>/dev/null; then
  GIT_DIRTY=true
fi
LAYOUT_VERSION="s2-6workers-api-240-mem-200-v2"

log() {
  printf '[run-cell] %s\n' "$*"
}

wait_for_exit() {
  local pid="$1" timeout_seconds="$2"
  local deadline=$((SECONDS + timeout_seconds))

  while kill -0 "$pid" >/dev/null 2>&1; do
    (( SECONDS >= deadline )) && return 1
    sleep 1
  done
  wait "$pid" 2>/dev/null || true
}

stop_process() {
  local pid="${1:-}" signal="${2:-TERM}" timeout_seconds="${3:-10}"
  [[ -n "$pid" ]] || return 0

  if kill -0 "$pid" >/dev/null 2>&1; then
    kill "-$signal" "$pid" >/dev/null 2>&1 || true
    if ! wait_for_exit "$pid" "$timeout_seconds"; then
      kill -TERM "$pid" >/dev/null 2>&1 || true
      if ! wait_for_exit "$pid" 5; then
        kill -KILL "$pid" >/dev/null 2>&1 || true
        wait "$pid" 2>/dev/null || true
      fi
    fi
  else
    wait "$pid" 2>/dev/null || true
  fi
}

cleanup() {
  stop_process "${API_PID:-}" INT "${K6_STOP_TIMEOUT_SECONDS:-15}"
  stop_process "${WATCH_PID:-}" TERM "${WATCH_STOP_TIMEOUT_SECONDS:-5}"
}
trap cleanup EXIT

log "running preflight and preparing layout"
"$ROOT/scripts/preflight.sh" | tee "$OUTPUT_DIR/preflight.txt"
"$ROOT/scripts/setup-layout.sh" | tee "$OUTPUT_DIR/setup.log"

source_node="$(cat "$OUTPUT_DIR/source-node.txt")"
api_pod="$(cat "$OUTPUT_DIR/api-pod.txt")"
source_ip="$(kubectl get node "$source_node" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')"
api_port="$(kubectl -n "$NS" get svc workload-api -o jsonpath='{.spec.ports[0].nodePort}')"
api_url="http://${API_HOST:-$source_ip}:$api_port"

cat > "$OUTPUT_DIR/run.env" <<EOF
RESOURCE=$RESOURCE
SYSTEM=$SYSTEM
REPEAT=$REPEAT
LOAD_PATTERN=$LOAD_PATTERN
GIT_COMMIT=$GIT_COMMIT
GIT_DIRTY=$GIT_DIRTY
LAYOUT_VERSION=$LAYOUT_VERSION
SOURCE_NODE=$source_node
API_POD=$api_pod
API_URL=$api_url
PRE_EVENT_SECONDS=$PRE_EVENT_SECONDS
POST_EVENT_SECONDS=$POST_EVENT_SECONDS
EOF

"$ROOT/scripts/snapshot.sh" before "$OUTPUT_DIR"
python3 "$ROOT/scripts/watch-pods.py" --namespace "$NS" --output "$OUTPUT_DIR/pod-lifecycle.tsv" &
WATCH_PID=$!

API_URL="$api_url" \
API_DURATION="${API_DURATION:-12m}" \
k6 run --out "json=$OUTPUT_DIR/api-load.json" \
  --summary-export "$OUTPUT_DIR/api-load-summary.json" \
  "$ROOT/k6/api-load.js" > "$OUTPUT_DIR/api-load.log" 2>&1 &
API_PID=$!

log "stabilizing api load for ${API_STABILIZE_SECONDS:-60}s"
sleep "${API_STABILIZE_SECONDS:-60}"

threshold=0.80

log "confirming two pre-busy ${RESOURCE} samples below ratio ${threshold}"
python3 "$ROOT/scripts/wait-threshold.py" \
  --namespace "$NS" \
  --pod "$api_pod" \
  --resource "$RESOURCE" \
  --threshold "$threshold" \
  --condition below \
  --consecutive 2 \
  --interval "${BASELINE_SAMPLE_INTERVAL_SECONDS:-5}" \
  --timeout "${BASELINE_SAMPLE_TIMEOUT_SECONDS:-90}" \
  --output "$OUTPUT_DIR/baseline-samples.tsv"

log "waiting for two consecutive ${RESOURCE} samples above ratio ${threshold}"
python3 "$ROOT/scripts/wait-threshold.py" \
  --namespace "$NS" \
  --pod "$api_pod" \
  --resource "$RESOURCE" \
  --threshold "$threshold" \
  --consecutive 2 \
  --output "$OUTPUT_DIR/threshold-samples.tsv"

log "recording pre-event window for ${PRE_EVENT_SECONDS}s"
sleep "$PRE_EVENT_SECONDS"
date -Ins > "$OUTPUT_DIR/event-time.txt"
"$ROOT/scripts/snapshot.sh" event "$OUTPUT_DIR"

log "running system event for ${SYSTEM}"
case "$SYSTEM" in
  N0) echo "N0: no descheduler" > "$OUTPUT_DIR/descheduler.log" ;;
  R0) "$ROOT/scripts/run-descheduler.sh" "$ROOT/policies/r0-rdc2.yaml" "$OUTPUT_DIR" ;;
  R1) "$ROOT/scripts/run-descheduler.sh" "$ROOT/policies/r1-rdc2-actual.yaml" "$OUTPUT_DIR" ;;
  H0) "$ROOT/scripts/run-descheduler.sh" "$ROOT/policies/h0-hnu.yaml" "$OUTPUT_DIR" ;;
  H1) "$ROOT/scripts/run-descheduler.sh" "$ROOT/policies/h1-hnu-actual.yaml" "$OUTPUT_DIR" ;;
esac

log "recording post-event window for ${POST_EVENT_SECONDS}s"
sleep "$POST_EVENT_SECONDS"
"$ROOT/scripts/snapshot.sh" after "$OUTPUT_DIR"
kubectl get events -A --sort-by=.lastTimestamp > "$OUTPUT_DIR/events.txt" 2>&1 || true
kubectl -n "$NS" get pdb -o yaml > "$OUTPUT_DIR/pdb.yaml"

log "stopping load generators and lifecycle watcher"
cleanup
trap - EXIT
log "writing summary to $OUTPUT_DIR/summary.txt"
python3 "$ROOT/scripts/summarize-run.py" "$OUTPUT_DIR" | tee "$OUTPUT_DIR/summary.txt"
log "run complete"
