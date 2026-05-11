#!/usr/bin/env bash
set -euo pipefail

GROUP="${GROUP:-E0}"
NAMESPACE="${NAMESPACE:-e0-e5-${GROUP,,}}"
RESULT_DIR="${RESULT_DIR:-$(pwd)/e0-e5-results/${GROUP}-$(date -u +%Y%m%dT%H%M%SZ)}"
DESCHEDULER_IMAGE="${DESCHEDULER_IMAGE:-busybox:1.36}"
AGENT_IMAGE="${AGENT_IMAGE:-busybox:1.36}"
DESCHEDULER_BIN_HOST_PATH="${DESCHEDULER_BIN_HOST_PATH:-/root/descheduler}"
AGENT_BIN_HOST_PATH="${AGENT_BIN_HOST_PATH:-/root/actual-usage-agent}"
DESCHEDULER_NODE_NAME="${DESCHEDULER_NODE_NAME:-}"
KUBECTL="${KUBECTL:-kubectl}"

mkdir -p "$RESULT_DIR"
log(){ printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" | tee -a "$RESULT_DIR/run.log"; }

stage_replicas(){
  case "$1" in
    low) echo "1 1 1 1" ;;
    medium) echo "2 2 2 2" ;;
    high-safe) echo "3 3 2 2" ;;
    *) echo "unknown stage $1" >&2; exit 2 ;;
  esac
}

capture(){
  local label="$1"
  $KUBECTL get pods -n "$NAMESPACE" -o json > "$RESULT_DIR/pods-${label}.json" || true
  $KUBECTL get nodes -o json > "$RESULT_DIR/nodes-${label}.json" || true
  $KUBECTL top nodes > "$RESULT_DIR/top-nodes-${label}.txt" 2>&1 || true
  $KUBECTL top pods -n "$NAMESPACE" > "$RESULT_DIR/top-pods-${label}.txt" 2>&1 || true
  $KUBECTL get events -n "$NAMESPACE" --sort-by=.lastTimestamp > "$RESULT_DIR/events-${label}.txt" 2>&1 || true
}

write_workload(){
  local stage="$1" cpu_rep="$2" mem_rep="$3" mixed_rep="$4" bursty_rep="$5"
  python3 - "$NAMESPACE" "$stage" "$cpu_rep" "$mem_rep" "$mixed_rep" "$bursty_rep" > "$RESULT_DIR/workload-${stage}.yaml" <<'PY'
import sys
ns, stage, cpu_rep, mem_rep, mixed_rep, bursty_rep = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), int(sys.argv[5]), int(sys.argv[6])
def deploy(name, replicas, cmd, req_cpu, req_mem, lim_cpu, lim_mem):
    return f'''---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {name}-{stage}
  namespace: {ns}
  labels: {{app: {name}, stage: {stage}}}
spec:
  replicas: {replicas}
  selector: {{matchLabels: {{app: {name}, stage: {stage}}}}}
  template:
    metadata: {{labels: {{app: {name}, stage: {stage}}}}}
    spec:
      containers:
      - name: work
        image: python:3.12-alpine
        command: ["/bin/sh","-c"]
        args: [{cmd!r}]
        resources:
          requests: {{cpu: {req_cpu}, memory: {req_mem}}}
          limits: {{cpu: {lim_cpu}, memory: {lim_mem}}}
'''
parts=[]
parts.append(deploy('cpu-burner', cpu_rep, 'while true; do :; done', '25m','32Mi','500m','96Mi'))
parts.append(deploy('memory-burner', mem_rep, 'python -c "import time; x=bytearray(180*1024*1024); x[0]=1; time.sleep(7200)"', '25m','32Mi','250m','256Mi'))
parts.append(deploy('mixed-burner', mixed_rep, 'python -c "import time; x=bytearray(120*1024*1024); end=time.time()+7200\nwhile time.time()<end: pass"', '25m','32Mi','500m','192Mi'))
parts.append(deploy('bursty-burner', bursty_rep, 'while true; do end=$((SECONDS+20)); while [ $SECONDS -lt $end ]; do :; done; sleep 20; done', '10m','24Mi','400m','96Mi'))
for i in range(3):
    parts.append(f'''---
apiVersion: v1
kind: Pod
metadata:
  name: probe-{stage}-{i}
  namespace: {ns}
  labels: {{app: probe, stage: {stage}}}
spec:
  restartPolicy: Never
  containers:
  - name: probe
    image: busybox:1.36
    command: ["/bin/sh","-c","date -u +%s > /tmp/probe-start; sleep 60"]
    resources:
      requests: {{cpu: 50m, memory: 64Mi}}
      limits: {{cpu: 100m, memory: 96Mi}}
''')
print('\n'.join(parts))
PY
}

run_descheduler_once(){
  local policy="$1"
  if [[ -z "$policy" ]]; then return 0; fi
  cp "$policy" "$RESULT_DIR/policy.yaml"
  log "Running descheduler through CronJob with policy $(basename "$policy")"
  $KUBECTL -n kube-system create configmap e0-e5-descheduler-policy --from-file=policy.yaml="$RESULT_DIR/policy.yaml" --dry-run=client -o yaml | $KUBECTL apply -f -
  $KUBECTL -n kube-system delete cronjob e0-e5-descheduler --ignore-not-found
  local node_name_yaml=""
  if [[ -n "$DESCHEDULER_NODE_NAME" ]]; then
    node_name_yaml="          nodeName: ${DESCHEDULER_NODE_NAME}"
  fi
  cat <<YAML | $KUBECTL apply -f -
apiVersion: batch/v1
kind: CronJob
metadata:
  name: e0-e5-descheduler
  namespace: kube-system
spec:
  schedule: "* * * * *"
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 1
  failedJobsHistoryLimit: 1
  jobTemplate:
    spec:
      template:
        spec:
${node_name_yaml}
          restartPolicy: Never
          serviceAccountName: descheduler
          tolerations:
          - operator: Exists
          containers:
          - name: descheduler
            image: ${DESCHEDULER_IMAGE}
            command: ["/host/descheduler"]
            args: ["--policy-config-file=/policy-dir/policy.yaml", "--v=4"]
            volumeMounts:
            - name: policy
              mountPath: /policy-dir
            - name: descheduler-bin
              mountPath: /host/descheduler
              readOnly: true
          volumes:
          - name: policy
            configMap:
              name: e0-e5-descheduler-policy
          - name: descheduler-bin
            hostPath:
              path: ${DESCHEDULER_BIN_HOST_PATH}
              type: File
YAML
  local job=""
  for _ in $(seq 1 90); do
    job="$($KUBECTL -n kube-system get jobs -l batch.kubernetes.io/cronjob-name=e0-e5-descheduler --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}' 2>/dev/null || true)"
    [[ -n "$job" ]] && break
    sleep 2
  done
  if [[ -z "$job" ]]; then
    log "ERROR: CronJob did not create a Job within timeout"
    return 1
  fi
  log "Waiting for CronJob-created Job $job"
  $KUBECTL -n kube-system wait --for=condition=complete "job/${job}" --timeout=240s || true
  $KUBECTL -n kube-system logs "job/${job}" > "$RESULT_DIR/descheduler-${GROUP}.log" 2>&1 || true
  $KUBECTL -n kube-system patch cronjob e0-e5-descheduler -p '{"spec":{"suspend":true}}' >/dev/null 2>&1 || true
}

start_agent_for_e5(){
  [[ "$GROUP" == "E5" ]] || return 0
  log "Starting E5 actual-usage-agent publisher"
  $KUBECTL -n kube-system delete deploy e0-e5-actual-usage-agent --ignore-not-found
  cat <<YAML | $KUBECTL apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: e0-e5-actual-usage-agent
  namespace: kube-system
spec:
  replicas: 1
  selector: {matchLabels: {app: e0-e5-actual-usage-agent}}
  template:
    metadata: {labels: {app: e0-e5-actual-usage-agent}}
    spec:
      serviceAccountName: actual-usage-agent
      containers:
      - name: agent
        image: ${AGENT_IMAGE}
        command: ["/host/actual-usage-agent"]
        args: ["--interval=30s", "--namespace=", "--publish-target=node-annotations", "--output-dir=/tmp/actual-usage-agent"]
        volumeMounts:
        - name: agent-bin
          mountPath: /host/actual-usage-agent
          readOnly: true
      volumes:
      - name: agent-bin
        hostPath:
          path: ${AGENT_BIN_HOST_PATH}
          type: File
YAML
  sleep 75
}

case "$GROUP" in
  E0) POLICY="" ;;
  E1) POLICY="$(dirname "$0")/../policies/e1-low-node-utilization.yaml" ;;
  E2) POLICY="$(dirname "$0")/../policies/e2-request-rii-topsis.yaml" ;;
  E3) POLICY="$(dirname "$0")/../policies/e3-actual-raw-rii-topsis.yaml" ;;
  E4) POLICY="$(dirname "$0")/../policies/e4-actual-ewma-tight-persisted.yaml" ;;
  E5) POLICY="$(dirname "$0")/../policies/e5-published-ewma-loose.yaml" ;;
  *) echo "GROUP must be E0..E5" >&2; exit 2 ;;
esac

log "Starting group=$GROUP namespace=$NAMESPACE result_dir=$RESULT_DIR"
$KUBECTL create namespace "$NAMESPACE" --dry-run=client -o yaml | $KUBECTL apply -f -
start_agent_for_e5
capture initial
for stage in low medium high-safe; do
  read -r c m x b < <(stage_replicas "$stage")
  log "Applying stage=$stage cpu=$c memory=$m mixed=$x bursty=$b"
  write_workload "$stage" "$c" "$m" "$x" "$b"
  $KUBECTL apply -f "$RESULT_DIR/workload-${stage}.yaml"
  sleep 90
  capture "${stage}-before-descheduler"
  run_descheduler_once "$POLICY"
  sleep 90
  capture "${stage}-after-descheduler"
done
python3 "$(dirname "$0")/summarize-e0-e5.py" "$RESULT_DIR" "$GROUP" "$NAMESPACE" > "$RESULT_DIR/summary.md"
log "Done. Summary: $RESULT_DIR/summary.md"
