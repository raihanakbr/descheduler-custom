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

cleanup() {
  for pid in "${FOREGROUND_PID:-}" "${HOTSPOT_PID:-}" "${WATCH_PID:-}"; do
    [[ -n "$pid" ]] && kill -INT "$pid" >/dev/null 2>&1 || true
  done
  wait "${FOREGROUND_PID:-}" "${HOTSPOT_PID:-}" "${WATCH_PID:-}" 2>/dev/null || true
}
trap cleanup EXIT

"$ROOT/scripts/preflight.sh" | tee "$OUTPUT_DIR/preflight.txt"
"$ROOT/scripts/setup-layout.sh" | tee "$OUTPUT_DIR/setup.log"

source_node="$(cat "$OUTPUT_DIR/source-node.txt")"
hotspot_pod="$(cat "$OUTPUT_DIR/hotspot-pod.txt")"
source_ip="$(kubectl get node "$source_node" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')"
foreground_port="$(kubectl -n "$NS" get svc workload-foreground -o jsonpath='{.spec.ports[0].nodePort}')"
hotspot_port="$(kubectl -n "$NS" get svc workload-hotspot -o jsonpath='{.spec.ports[0].nodePort}')"
foreground_url="http://${FOREGROUND_HOST:-$source_ip}:$foreground_port"
hotspot_url="http://${HOTSPOT_HOST:-$source_ip}:$hotspot_port"

cat > "$OUTPUT_DIR/run.env" <<EOF
RESOURCE=$RESOURCE
SYSTEM=$SYSTEM
REPEAT=$REPEAT
LOAD_PATTERN=$LOAD_PATTERN
SOURCE_NODE=$source_node
HOTSPOT_POD=$hotspot_pod
FOREGROUND_URL=$foreground_url
HOTSPOT_URL=$hotspot_url
PRE_EVENT_SECONDS=$PRE_EVENT_SECONDS
POST_EVENT_SECONDS=$POST_EVENT_SECONDS
EOF

"$ROOT/scripts/snapshot.sh" before "$OUTPUT_DIR"
python3 "$ROOT/scripts/watch-pods.py" --namespace "$NS" --output "$OUTPUT_DIR/pod-lifecycle.tsv" &
WATCH_PID=$!

FOREGROUND_URL="$foreground_url" \
FOREGROUND_DURATION="${FOREGROUND_DURATION:-12m}" \
k6 run --out "json=$OUTPUT_DIR/foreground.json" \
  --summary-export "$OUTPUT_DIR/foreground-summary.json" \
  "$ROOT/k6/foreground.js" > "$OUTPUT_DIR/foreground.log" 2>&1 &
FOREGROUND_PID=$!

sleep "${FOREGROUND_STABILIZE_SECONDS:-60}"

if [[ "$LOAD_PATTERN" != "idle" ]]; then
  HOTSPOT_URL="$hotspot_url" RESOURCE="$RESOURCE" \
  HOTSPOT_DURATION="${HOTSPOT_DURATION:-10m}" \
  k6 run --out "json=$OUTPUT_DIR/hotspot.json" \
    --summary-export "$OUTPUT_DIR/hotspot-summary.json" \
    "$ROOT/k6/hotspot.js" > "$OUTPUT_DIR/hotspot.log" 2>&1 &
  HOTSPOT_PID=$!
fi

threshold=0.80
[[ "$RESOURCE" == "memory" ]] && threshold=0.90

if [[ "$LOAD_PATTERN" == "idle" ]]; then
  sleep "${IDLE_OBSERVE_SECONDS:-45}"
else
  python3 "$ROOT/scripts/wait-threshold.py" \
    --namespace "$NS" \
    --pod "$hotspot_pod" \
    --resource "$RESOURCE" \
    --threshold "$threshold" \
    --consecutive 2 \
    --output "$OUTPUT_DIR/threshold-samples.tsv"
fi

if [[ "$LOAD_PATTERN" == "transient" && -n "${HOTSPOT_PID:-}" ]]; then
  kill -INT "$HOTSPOT_PID" >/dev/null 2>&1 || true
  wait "$HOTSPOT_PID" 2>/dev/null || true
  HOTSPOT_PID=""
  sleep "${TRANSIENT_GAP_SECONDS:-5}"
fi

sleep "$PRE_EVENT_SECONDS"
date -Ins > "$OUTPUT_DIR/event-time.txt"
"$ROOT/scripts/snapshot.sh" event "$OUTPUT_DIR"

case "$SYSTEM" in
  N0) echo "N0: no descheduler" > "$OUTPUT_DIR/descheduler.log" ;;
  R0) "$ROOT/scripts/run-descheduler.sh" "$ROOT/policies/r0-rdc2.yaml" "$OUTPUT_DIR" ;;
  R1) "$ROOT/scripts/run-descheduler.sh" "$ROOT/policies/r1-rdc2-actual.yaml" "$OUTPUT_DIR" ;;
  H0) "$ROOT/scripts/run-descheduler.sh" "$ROOT/policies/h0-hnu.yaml" "$OUTPUT_DIR" ;;
  H1) "$ROOT/scripts/run-descheduler.sh" "$ROOT/policies/h1-hnu-actual.yaml" "$OUTPUT_DIR" ;;
esac

sleep "$POST_EVENT_SECONDS"
"$ROOT/scripts/snapshot.sh" after "$OUTPUT_DIR"
kubectl get events -A --sort-by=.lastTimestamp > "$OUTPUT_DIR/events.txt" 2>&1 || true
kubectl -n "$NS" get pdb -o yaml > "$OUTPUT_DIR/pdb.yaml"

cleanup
trap - EXIT
python3 "$ROOT/scripts/summarize-run.py" "$OUTPUT_DIR" | tee "$OUTPUT_DIR/summary.txt"
