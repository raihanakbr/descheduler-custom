#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
POLICY="$1"
OUTPUT_DIR="$2"
DESCHEDULER_IMAGE="${DESCHEDULER_IMAGE:?DESCHEDULER_IMAGE is required}"
JOB="actual-usage-descheduler"

kubectl apply -f "$ROOT/k8s/rbac.yaml" >/dev/null
kubectl -n kube-system create configmap actual-usage-policy \
  --from-file=policy.yaml="$POLICY" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n kube-system delete job "$JOB" --ignore-not-found --wait=true --timeout=180s >/dev/null

kubectl apply -f - <<EOF >/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: ${JOB}
  namespace: kube-system
spec:
  backoffLimit: 0
  template:
    spec:
      serviceAccountName: actual-usage-descheduler
      restartPolicy: Never
      containers:
      - name: descheduler
        image: ${DESCHEDULER_IMAGE}
        imagePullPolicy: Always
        command: ["/bin/descheduler"]
        args:
        - "--policy-config-file=/policy/policy.yaml"
        - "--v=4"
        volumeMounts:
        - name: policy
          mountPath: /policy
      volumes:
      - name: policy
        configMap:
          name: actual-usage-policy
EOF

kubectl -n kube-system wait --for=condition=complete "job/$JOB" --timeout=180s
kubectl -n kube-system logs "job/$JOB" > "$OUTPUT_DIR/descheduler.log" 2>&1
