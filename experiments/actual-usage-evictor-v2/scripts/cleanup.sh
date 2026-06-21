#!/usr/bin/env bash
set -euo pipefail

status=0

kubectl delete ns actual-usage-exp actual-usage-system \
  --ignore-not-found --wait=true --timeout=180s || status=$?
kubectl -n kube-system delete job actual-usage-descheduler \
  --ignore-not-found --wait=true --timeout=180s || status=$?
kubectl -n kube-system delete configmap actual-usage-policy \
  --ignore-not-found --wait=true --timeout=180s || status=$?

mapfile -t workers < <(kubectl get nodes \
  -l '!node-role.kubernetes.io/control-plane,!node-role.kubernetes.io/master' \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
if (( ${#workers[@]} > 0 )); then
  kubectl uncordon "${workers[@]}" >/dev/null 2>&1 || status=$?
fi

exit "$status"
