#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NS="${NS:-actual-usage-exp}"
WORKLOAD_IMAGE="${WORKLOAD_IMAGE:-docker.io/matthewhjt/workload-http:actual-usage-v1}"
OUTPUT_DIR="${OUTPUT_DIR:-/tmp/actual-usage-layout}"

mkdir -p "$OUTPUT_DIR"

if [[ "$NS" != "actual-usage-exp" ]]; then
  echo "ERROR: manifests currently require NS=actual-usage-exp" >&2
  exit 1
fi

mapfile -t all_workers < <(kubectl get nodes \
  -l '!node-role.kubernetes.io/control-plane,!node-role.kubernetes.io/master' \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)

workers_cordoned=false
restore_workers_on_error() {
  local status=$?
  trap - EXIT
  if (( status != 0 )) && [[ "$workers_cordoned" == true ]]; then
    kubectl uncordon "${all_workers[@]}" >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap restore_workers_on_error EXIT

if (( ${#all_workers[@]} < 5 )); then
  echo "ERROR: need at least 5 workers, found ${#all_workers[@]}" >&2
  exit 1
fi

if [[ -n "${ACTIVE_WORKERS:-}" ]]; then
  read -r -a workers <<<"$ACTIVE_WORKERS"
else
  workers=("${all_workers[@]:0:5}")
fi

if (( ${#workers[@]} != 5 )); then
  echo "ERROR: ACTIVE_WORKERS must contain exactly 5 node names" >&2
  exit 1
fi

source_node="${SOURCE_NODE:-${workers[0]}}"

if [[ ! " ${workers[*]} " =~ " ${source_node} " ]]; then
  echo "ERROR: SOURCE_NODE must be one of ACTIVE_WORKERS" >&2
  exit 1
fi

printf '%s\n' "${workers[@]}" > "$OUTPUT_DIR/active-workers.txt"
printf '%s\n' "$source_node" > "$OUTPUT_DIR/source-node.txt"

kubectl delete ns "$NS" actual-usage-system --ignore-not-found --wait=true --timeout=180s
kubectl create ns "$NS"

kubectl cordon "${all_workers[@]}" >/dev/null
workers_cordoned=true

deploy_workload() {
  local name="$1" node="$2" replicas="$3" cpu_request="$4" memory_request="$5"
  local hotspot="$6" cpu_limit="$7"

  kubectl uncordon "$node" >/dev/null
  kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${name}
  namespace: ${NS}
  labels:
    experiment: actual-usage-evictor
spec:
  replicas: ${replicas}
  selector:
    matchLabels:
      app: ${name}
  template:
    metadata:
      labels:
        app: ${name}
        experiment: actual-usage-evictor
        hotspot: "${hotspot}"
    spec:
      terminationGracePeriodSeconds: 35
      containers:
      - name: workload
        image: ${WORKLOAD_IMAGE}
        imagePullPolicy: Always
        ports:
        - containerPort: 8080
        env:
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: DRAIN_DELAY_SECONDS
          value: "5"
        - name: SHUTDOWN_TIMEOUT_SECONDS
          value: "25"
        - name: MAX_CPU_UNITS
          value: "5000"
        - name: ITERATIONS_PER_CPU_UNIT
          value: "200000"
        - name: MAX_MEM_MB
          value: "160"
        - name: MAX_HOLD_MS
          value: "10000"
        - name: MAX_INFLIGHT
          value: "128"
        - name: MAX_TOTAL_ALLOC_MB
          value: "640"
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8080
          initialDelaySeconds: 2
          periodSeconds: 1
          timeoutSeconds: 1
          failureThreshold: 2
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 5
        resources:
          requests:
            cpu: ${cpu_request}
            memory: ${memory_request}
          limits:
            cpu: ${cpu_limit}
            memory: 700Mi
EOF
  kubectl -n "$NS" rollout status "deployment/$name" --timeout=180s >/dev/null
  kubectl cordon "$node" >/dev/null
}

# Five-worker complementary fragmentation:
#   source: one memory-heavy HTTP Pod that receives the dynamic hotspot
#   worker 2: two smaller memory-heavy HTTP Pods
#   workers 3-5: one CPU-heavy HTTP Pod each
#
# On the reference 2000m/~811Mi workers, including the ~100m/50Mi daemonset
# baseline, the nodes are approximately:
#   source      0.10 CPU / 0.65 memory
#   memory peer 0.10 CPU / 0.60 memory
#   CPU targets 0.83 CPU / 0.11 memory
#
# CPU-heavy Pods have no valid target: memory nodes have a lower min-utilization
# than their source and other CPU nodes lack room. The source ranks ahead of the
# memory peer, so with maxEvictions=1 RDC2 deterministically selects the hotspot.
deploy_workload workload-hotspot "$source_node" 1 100m 480Mi true 1000m

memory_node=""
for node in "${workers[@]}"; do
  [[ "$node" == "$source_node" ]] && continue
  memory_node="$node"
  break
done
deploy_workload workload-memory "$memory_node" 2 50m 220Mi false 1000m

cpu_index=1
for node in "${workers[@]}"; do
  [[ "$node" == "$source_node" || "$node" == "$memory_node" ]] && continue
  deploy_workload "workload-cpu-${cpu_index}" "$node" 1 1550m 40Mi false 1700m
  cpu_index=$((cpu_index + 1))
done

for node in "${workers[@]}"; do
  kubectl uncordon "$node" >/dev/null
done

kubectl apply -f "$ROOT/k8s/services.yaml"
kubectl -n "$NS" wait --for=condition=available deployment --all --timeout=180s

hotspot_pod="$(kubectl -n "$NS" get pod -l hotspot=true -o jsonpath='{.items[0].metadata.name}')"
hotspot_node="$(kubectl -n "$NS" get pod "$hotspot_pod" -o jsonpath='{.spec.nodeName}')"
if [[ "$hotspot_node" != "$source_node" ]]; then
  echo "ERROR: hotspot landed on $hotspot_node, expected $source_node" >&2
  exit 1
fi

expected_pods=6
actual_pods="$(kubectl -n "$NS" get pods -l experiment=actual-usage-evictor --no-headers | wc -l)"
if (( actual_pods != expected_pods )); then
  echo "ERROR: expected $expected_pods workload Pods, found $actual_pods" >&2
  exit 1
fi

kubectl get nodes -o json > "$OUTPUT_DIR/nodes-layout.json"
kubectl get pods -A -o json > "$OUTPUT_DIR/pods-layout.json"
python3 "$ROOT/scripts/validate-layout.py" \
  --nodes "$OUTPUT_DIR/nodes-layout.json" \
  --pods "$OUTPUT_DIR/pods-layout.json" \
  --workers "$OUTPUT_DIR/active-workers.txt" \
  --namespace "$NS" \
  --hotspot-pod "$hotspot_pod" \
  | tee "$OUTPUT_DIR/layout-validation.json"

kubectl -n "$NS" get pods -o wide --sort-by=.spec.nodeName | tee "$OUTPUT_DIR/layout.txt"
echo "$hotspot_pod" > "$OUTPUT_DIR/hotspot-pod.txt"
echo "$memory_node" > "$OUTPUT_DIR/memory-node.txt"
