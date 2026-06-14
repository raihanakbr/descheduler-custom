#!/usr/bin/env bash
set -euo pipefail

LNU_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NS="actual-usage-exp"
WORKLOAD_IMAGE="${WORKLOAD_IMAGE:-docker.io/matthewhjt/workload-http:actual-usage-v1}"
OUTPUT_DIR="${OUTPUT_DIR:?OUTPUT_DIR is required}"

mapfile -t all_workers < <(kubectl get nodes \
  -l '!node-role.kubernetes.io/control-plane,!node-role.kubernetes.io/master' \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)

if [[ -n "${ACTIVE_WORKERS:-}" ]]; then
  read -r -a workers <<<"$ACTIVE_WORKERS"
else
  workers=("${all_workers[@]:0:6}")
fi
if (( ${#workers[@]} != 6 )); then
  echo "ERROR: ACTIVE_WORKERS must contain exactly 6 node names" >&2
  exit 1
fi

printf '%s\n' "${workers[@]}" > "$OUTPUT_DIR/active-workers.txt"
printf '%s\n' "${workers[@]:0:3}" > "$OUTPUT_DIR/destination-nodes.txt"
printf '%s\n' "${workers[@]:3:3}" > "$OUTPUT_DIR/source-nodes.txt"
printf '%s\n' "${workers[5]}" > "$OUTPUT_DIR/source-node.txt"

kubectl delete ns "$NS" actual-usage-system --ignore-not-found --wait=true --timeout=180s
kubectl create ns "$NS"
kubectl cordon "${all_workers[@]}" >/dev/null

deploy_workload() {
  local name="$1" node="$2" replicas="$3" cpu_request="$4" memory_request="$5"
  local cpu_limit="$6" memory_limit="$7"

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
            memory: ${memory_limit}
EOF
  kubectl -n "$NS" rollout status "deployment/$name" --timeout=180s >/dev/null
  kubectl cordon "$node" >/dev/null
}

# Three lightly loaded destinations, below both 20% thresholds.
for i in 0 1 2; do
  node="${workers[$i]}"
  deploy_workload "workload-destination-$((i + 1))" "$node" 1 50m 50Mi 1000m 700Mi
done

# Two natural LNU sources. Removing either 200Mi pod drops the node below the
# 50% target threshold, so LNU stops after one successful eviction per source.
deploy_workload workload-idle-source-1 "${workers[3]}" 2 50m 200Mi 1000m 700Mi
deploy_workload workload-idle-source-2 "${workers[4]}" 2 50m 200Mi 1000m 700Mi

# The API is Burstable and is considered before the Guaranteed idle fallback.
# L0 evicts the API; L1 blocks it and then evicts the idle fallback.
deploy_workload workload-api "${workers[5]}" 1 50m 200Mi 1000m 700Mi
deploy_workload workload-api-fallback "${workers[5]}" 1 50m 200Mi 50m 200Mi

for node in "${workers[@]}"; do
  kubectl uncordon "$node" >/dev/null
done

kubectl apply -f "$LNU_ROOT/k8s/services.yaml"
kubectl -n "$NS" wait --for=condition=available deployment --all --timeout=180s

api_pod="$(kubectl -n "$NS" get pod -l app=workload-api -o jsonpath='{.items[0].metadata.name}')"
api_node="$(kubectl -n "$NS" get pod "$api_pod" -o jsonpath='{.spec.nodeName}')"
if [[ "$api_node" != "${workers[5]}" ]]; then
  echo "ERROR: API pod landed on $api_node, expected ${workers[5]}" >&2
  exit 1
fi

echo "$api_pod" > "$OUTPUT_DIR/api-pod.txt"
kubectl get nodes -o json > "$OUTPUT_DIR/nodes-layout.json"
kubectl get pods -A -o json > "$OUTPUT_DIR/pods-layout.json"
python3 "$LNU_ROOT/scripts/validate-layout.py" \
  --nodes "$OUTPUT_DIR/nodes-layout.json" \
  --pods "$OUTPUT_DIR/pods-layout.json" \
  --workers "$OUTPUT_DIR/active-workers.txt" \
  --destinations "$OUTPUT_DIR/destination-nodes.txt" \
  --sources "$OUTPUT_DIR/source-nodes.txt" \
  --namespace "$NS" \
  --api-pod "$api_pod" \
  | tee "$OUTPUT_DIR/layout-validation.json"

kubectl -n "$NS" get pods -o wide --sort-by=.spec.nodeName | tee "$OUTPUT_DIR/layout.txt"
