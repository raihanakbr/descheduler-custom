#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NS="${NS:-actual-usage-exp}"
SYSTEM_NS="${SYSTEM_NS:-actual-usage-system}"
WORKLOAD_IMAGE="${WORKLOAD_IMAGE:-docker.io/matthewhjt/workload-http:actual-usage-v1}"
OUTPUT_DIR="${OUTPUT_DIR:-/tmp/actual-usage-layout}"

mkdir -p "$OUTPUT_DIR"

if [[ "$NS" != "actual-usage-exp" || "$SYSTEM_NS" != "actual-usage-system" ]]; then
  echo "ERROR: manifests currently require NS=actual-usage-exp and SYSTEM_NS=actual-usage-system" >&2
  exit 1
fi

mapfile -t all_workers < <(kubectl get nodes \
  -l '!node-role.kubernetes.io/control-plane,!node-role.kubernetes.io/master' \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)

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

kubectl delete ns "$NS" "$SYSTEM_NS" --ignore-not-found --wait=true
kubectl create ns "$NS"
kubectl create ns "$SYSTEM_NS"

kubectl cordon "${all_workers[@]}" >/dev/null

deploy_workload() {
  local index="$1" node="$2" hotspot="$3"
  local name="workload-${index}"

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
  replicas: 1
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
            cpu: 250m
            memory: 128Mi
          limits:
            cpu: 1000m
            memory: 700Mi
EOF
  kubectl -n "$NS" rollout status "deployment/$name" --timeout=180s >/dev/null
  kubectl cordon "$node" >/dev/null
}

index=1
for node in "${workers[@]}"; do
  if [[ "$node" == "$source_node" ]]; then
    deploy_workload "$index" "$node" true
  else
    deploy_workload "$index" "$node" false
  fi
  index=$((index + 1))
done

# The four destination nodes must be above the 40% requests threshold while the
# source remains below it. Ballast is excluded from eviction by namespace.
for node in "${workers[@]}"; do
  [[ "$node" == "$source_node" ]] && continue
  kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ballast-${node}
  namespace: ${SYSTEM_NS}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ballast-${node}
  template:
    metadata:
      labels:
        app: ballast-${node}
    spec:
      nodeSelector:
        kubernetes.io/hostname: ${node}
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
        resources:
          requests:
            cpu: 650m
            memory: 250Mi
          limits:
            cpu: 650m
            memory: 250Mi
EOF
done

for node in "${workers[@]}"; do
  kubectl uncordon "$node" >/dev/null
done

kubectl apply -f "$ROOT/k8s/services.yaml"
kubectl -n "$NS" wait --for=condition=available deployment --all --timeout=180s
kubectl -n "$SYSTEM_NS" wait --for=condition=available deployment --all --timeout=180s

hotspot_pod="$(kubectl -n "$NS" get pod -l hotspot=true -o jsonpath='{.items[0].metadata.name}')"
hotspot_node="$(kubectl -n "$NS" get pod "$hotspot_pod" -o jsonpath='{.spec.nodeName}')"
if [[ "$hotspot_node" != "$source_node" ]]; then
  echo "ERROR: hotspot landed on $hotspot_node, expected $source_node" >&2
  exit 1
fi

kubectl -n "$NS" get pods -o wide --sort-by=.spec.nodeName | tee "$OUTPUT_DIR/layout.txt"
kubectl -n "$SYSTEM_NS" get pods -o wide --sort-by=.spec.nodeName | tee "$OUTPUT_DIR/ballast.txt"
echo "$hotspot_pod" > "$OUTPUT_DIR/hotspot-pod.txt"
