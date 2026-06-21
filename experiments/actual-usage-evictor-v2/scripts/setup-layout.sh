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

if (( ${#all_workers[@]} < 6 )); then
  echo "ERROR: need at least 6 workers, found ${#all_workers[@]}" >&2
  exit 1
fi

if [[ -n "${ACTIVE_WORKERS:-}" ]]; then
  read -r -a workers <<<"$ACTIVE_WORKERS"
else
  workers=("${all_workers[@]:0:6}")
fi

if (( ${#workers[@]} != 6 )); then
  echo "ERROR: ACTIVE_WORKERS must contain exactly 6 node names" >&2
  exit 1
fi

source_node="${SOURCE_NODE:-${workers[5]}}"

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

# S2 fragmented/complementary layout (6 workers, 2 pods per worker):
#   workers 1-3: cpu-skewed  2 x (750m/25Mi)  = 1500m/50Mi   (~0.75 cpu, ~0.06 mem)
#   workers 4-5: mem-skewed  2 x (50m/240Mi)  =  100m/480Mi  (~0.05 cpu, ~0.59 mem)
#   worker 6:    source      1 api (50m/350Mi) + 1 mem (50m/250Mi) = 100m/600Mi
#
# The api pod (350Mi) has larger memory than the idle mem pod (250Mi), so C2
# deterministically selects the api pod first (higher binScore at cpu-skewed target).
# Worker-6 memory (600Mi) exceeds worker-4/5 (480Mi), giving it higher RDC2 priority.
i=0
for node in "${workers[@]}"; do
  if (( i < 3 )); then
    deploy_workload "workload-cpu-${node##*-}" "$node" 2 750m 25Mi false 800m
  elif (( i < 5 )); then
    deploy_workload "workload-mem-${node##*-}" "$node" 2 50m 240Mi false 1000m
  else
    deploy_workload workload-api "$node" 1 50m 350Mi true 1000m
    deploy_workload "workload-mem-${node##*-}" "$node" 1 50m 250Mi false 1000m
  fi
  i=$((i + 1))
done

for node in "${workers[@]}"; do
  kubectl uncordon "$node" >/dev/null
done

kubectl apply -f "$ROOT/k8s/services.yaml"
kubectl -n "$NS" wait --for=condition=available deployment --all --timeout=180s

api_pod="$(kubectl -n "$NS" get pod -l app=workload-api -o jsonpath='{.items[0].metadata.name}')"
api_node="$(kubectl -n "$NS" get pod "$api_pod" -o jsonpath='{.spec.nodeName}')"
if [[ "$api_node" != "$source_node" ]]; then
  echo "ERROR: api pod landed on $api_node, expected $source_node" >&2
  exit 1
fi

expected_pods=12
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
  --api-pod "$api_pod" \
  | tee "$OUTPUT_DIR/layout-validation.json"

kubectl -n "$NS" get pods -o wide --sort-by=.spec.nodeName | tee "$OUTPUT_DIR/layout.txt"
echo "$api_pod" > "$OUTPUT_DIR/api-pod.txt"
