#!/usr/bin/env bash
set -euo pipefail

kubectl delete ns actual-usage-exp actual-usage-system --ignore-not-found
kubectl -n kube-system delete job actual-usage-descheduler --ignore-not-found
kubectl -n kube-system delete configmap actual-usage-policy --ignore-not-found

mapfile -t workers < <(kubectl get nodes \
  -l '!node-role.kubernetes.io/control-plane,!node-role.kubernetes.io/master' \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
kubectl uncordon "${workers[@]}" >/dev/null 2>&1 || true
