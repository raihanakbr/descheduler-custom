# Shared helpers for the ResourceDefragmentation consolidation experiments.
#
# Sourced by every scenario's setup / perform_and_capture / cleanup script.
# It only uses plain kubectl + python3. Override any knob below via the
# environment, e.g.  MAX_PASSES=6 ./perform_and_capture.sh
#
# Worker facts for THIS cluster (measured, requests mode):
#   6 workers, each 2000m CPU / 830876Ki (~811Mi) allocatable,
#   ~100m / 50Mi already requested by daemonsets (flannel + kube-proxy).

set -euo pipefail

# ---- knobs (override via env) ----------------------------------------------
NS="${NS:-defrag-exp}"                                   # experiment namespace
WORKER_COUNT="${WORKER_COUNT:-6}"                        # workers to use (plan = 6)
PAUSE_IMAGE="${PAUSE_IMAGE:-registry.k8s.io/pause:3.9}"  # reserves requests, uses ~0
MAX_PASSES="${MAX_PASSES:-5}"                            # re-run descheduler until 0 evictions
SETTLE_SECONDS="${SETTLE_SECONDS:-20}"                   # pause after a pass for re-scheduling

EXP_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUT_MANIFEST="${SUT_MANIFEST:-$EXP_ROOT/descheduler/sut-requests.yaml}"
DESCHED_NS="kube-system"
DESCHED_JOB="descheduler-job"

# ---- node discovery --------------------------------------------------------
# Fills the WORKERS array with the first $WORKER_COUNT non-control-plane nodes.
discover_workers() {
  mapfile -t ALL_WORKERS < <(kubectl get nodes \
    -l '!node-role.kubernetes.io/control-plane,!node-role.kubernetes.io/master' \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
  if (( ${#ALL_WORKERS[@]} < WORKER_COUNT )); then
    echo "ERROR: need $WORKER_COUNT workers, found ${#ALL_WORKERS[@]}: ${ALL_WORKERS[*]}" >&2
    exit 1
  fi
  WORKERS=("${ALL_WORKERS[@]:0:$WORKER_COUNT}")
  echo "Using workers: ${WORKERS[*]}" >&2
}

cordon_all_workers()   { kubectl cordon   "${WORKERS[@]}" >/dev/null; }
uncordon_all_workers() { kubectl uncordon "${WORKERS[@]}" >/dev/null; }

ensure_namespace() {
  kubectl get ns "$NS" >/dev/null 2>&1 || kubectl create ns "$NS" >/dev/null
}

# place_on_node NODE NAME REPLICAS CPU MEM [PRIORITY_CLASS]
#
# Seeds a Deployment onto exactly one worker. Assumes every worker is currently
# cordoned (see exp_setup_begin): it uncordons just NODE so the scheduler has no
# other choice, applies the Deployment, waits for it to be Ready, then re-cordons
# NODE so the next group lands elsewhere. The pods carry NO nodeName/affinity, so
# the descheduler can later move them freely.
#
# The optional 6th arg sets spec.priorityClassName so the pods carry a non-zero
# pod priority (activates the selector's C4 criterion). Omitted -> no priority
# (every pre-existing scenario is unchanged).
place_on_node() {
  local node="$1" name="$2" replicas="$3" cpu="$4" mem="$5" prio="${6:-}"
  local prio_line=""
  if [[ -n "$prio" ]]; then prio_line="priorityClassName: \"$prio\""; fi
  echo "  -> $name : ${replicas} x (cpu=$cpu mem=$mem${prio:+ prio=$prio}) on $node" >&2
  kubectl uncordon "$node" >/dev/null
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $name
  namespace: $NS
  labels:
    experiment: defrag
    scenario: "$SCENARIO"
spec:
  replicas: $replicas
  selector:
    matchLabels:
      app: $name
  template:
    metadata:
      labels:
        app: $name
        experiment: defrag
        scenario: "$SCENARIO"
    spec:
      terminationGracePeriodSeconds: 0
      $prio_line
      containers:
      - name: pause
        image: $PAUSE_IMAGE
        resources:
          requests:
            cpu: "$cpu"
            memory: "$mem"
          limits:
            cpu: "$cpu"
            memory: "$mem"
EOF
  kubectl -n "$NS" rollout status deploy/"$name" --timeout=120s >/dev/null
  kubectl cordon "$node" >/dev/null
}

# ---- setup begin / end -----------------------------------------------------
exp_setup_begin() {
  discover_workers
  ensure_namespace
  echo "Cordoning all workers so initial placement is controlled..." >&2
  cordon_all_workers
}

exp_setup_end() {
  echo "Uncordoning all workers (cluster ready for descheduler)..." >&2
  uncordon_all_workers
  echo
  echo "Initial layout for $SCENARIO:"
  kubectl -n "$NS" get pods -o wide --sort-by=.spec.nodeName
}

# ---- snapshot + metrics ----------------------------------------------------
results_dir() { echo "$EXP_ROOT/results/$SCENARIO/$1"; }

# snapshot LABEL DIR : dump raw json and compute tool-agnostic metrics from it.
snapshot() {
  local label="$1" dir="$2"
  kubectl get nodes -o json > "$dir/nodes_${label}.json"
  kubectl get pods -A -o json > "$dir/pods_${label}.json"
  python3 "$EXP_ROOT/metrics.py" \
    --namespace "$NS" \
    --nodes-json "$dir/nodes_${label}.json" \
    --pods-json "$dir/pods_${label}.json" \
    --label "$label" \
    | tee "$dir/metrics_${label}.txt"
}

# run_descheduler_pass PASS DIR : one descheduler run. Echoes eviction count.
run_descheduler_pass() {
  local pass="$1" dir="$2"
  kubectl -n "$DESCHED_NS" delete job "$DESCHED_JOB" --ignore-not-found --wait=true >/dev/null 2>&1
  kubectl apply -f "$SUT_MANIFEST" >/dev/null
  echo "  pass $pass: running descheduler job..." >&2
  kubectl -n "$DESCHED_NS" wait --for=condition=complete job/"$DESCHED_JOB" --timeout=180s >/dev/null 2>&1 \
    || echo "  WARNING: descheduler job did not report complete in 180s" >&2
  kubectl -n "$DESCHED_NS" logs job/"$DESCHED_JOB" > "$dir/desched_pass${pass}.log" 2>&1 || true
  # Tool-agnostic E: count the framework-level evictions.go "Evicted pod" line,
  # which EVERY plugin emits (RD's custom "Eviction decision" line is emitted
  # only by ResourceDefragmentation, so it under-counts HNU/B1/B2 baselines).
  local count
  count="$(grep -c '"Evicted pod"' "$dir/desched_pass${pass}.log" 2>/dev/null)" || count=0
  echo "$count"
}

# exp_run_and_capture : before snapshot -> N descheduler passes -> after snapshot.
exp_run_and_capture() {
  discover_workers
  local ts dir; ts="$(date +%Y%m%d-%H%M%S)${RUN_TAG:+-$RUN_TAG}"; dir="$(results_dir "$ts")"; mkdir -p "$dir"
  echo "Results -> $dir"

  echo "== BEFORE =="
  snapshot before "$dir"
  kubectl -n "$NS" get pods -o wide --sort-by=.spec.nodeName > "$dir/pods_before.txt" 2>/dev/null || true

  local -a passes=(); local total=0 converged=0 p e
  for (( p=1; p<=MAX_PASSES; p++ )); do
    e="$(run_descheduler_pass "$p" "$dir")"
    echo "  pass $p: $e eviction decision(s)"
    passes+=("$e"); total=$((total + e))
    if (( SETTLE_SECONDS > 0 )); then sleep "$SETTLE_SECONDS"; fi
    kubectl -n "$NS" wait --for=condition=available deploy --all --timeout=120s >/dev/null 2>&1 || true
    if [[ "$e" == "0" ]]; then converged=1; break; fi
  done

  echo "== AFTER =="
  snapshot after "$dir"

  # disruption / safety evidence
  kubectl get events -A --field-selector reason=Descheduled \
    -o custom-columns='TIME:.lastTimestamp,NS:.involvedObject.namespace,POD:.involvedObject.name,MSG:.message' \
    > "$dir/events_descheduled.txt" 2>/dev/null || true
  kubectl -n "$NS" get pods -o wide --sort-by=.spec.nodeName > "$dir/pods_after.txt" 2>/dev/null || true
  kubectl -n "$NS" get pdb -o wide > "$dir/pdb.txt" 2>/dev/null || true

  {
    echo "scenario:            $SCENARIO"
    echo "timestamp:           $ts"
    echo "per_pass_evictions:  ${passes[*]}"
    echo "total_evictions(E):  $total"
    echo "converged:           $converged   (1 = a pass evicted 0)"
    echo "passes_run:          ${#passes[@]} (max $MAX_PASSES)"
  } | tee "$dir/summary.txt"
  echo "Done. Raw json, per-pass logs, metrics and summary in: $dir"
}

# ---- cleanup ---------------------------------------------------------------
exp_cleanup() {
  discover_workers || true
  echo "Deleting $SCENARIO workloads in ns/$NS ..."
  kubectl -n "$NS" delete deploy -l "scenario=$SCENARIO" --ignore-not-found
  echo "Uncordoning all workers (in case setup was interrupted) ..."
  kubectl uncordon "${WORKERS[@]:-}" >/dev/null 2>&1 || true
  echo "Removing descheduler job ..."
  kubectl -n "$DESCHED_NS" delete job "$DESCHED_JOB" --ignore-not-found >/dev/null 2>&1 || true
  echo "Cleanup for $SCENARIO done. (namespace '$NS' kept; 'kubectl delete ns $NS' to remove entirely.)"
}
