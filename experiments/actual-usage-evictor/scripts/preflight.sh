#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

for command in kubectl k6 python3; do
  command -v "$command" >/dev/null || {
    echo "ERROR: required command not found: $command" >&2
    exit 1
  }
done

[[ -n "${DESCHEDULER_IMAGE:-}" ]] || {
  echo "ERROR: DESCHEDULER_IMAGE must identify an image containing ActualUsageEvictor" >&2
  exit 1
}

kubectl cluster-info >/dev/null
kubectl get --raw /apis/metrics.k8s.io/v1beta1/nodes >/dev/null

mapfile -t workers < <(kubectl get nodes \
  -l '!node-role.kubernetes.io/control-plane,!node-role.kubernetes.io/master' \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)

if (( ${#workers[@]} < 6 )); then
  echo "ERROR: need at least 6 workers, found ${#workers[@]}" >&2
  exit 1
fi

echo "Context: $(kubectl config current-context)"
echo "Workers (${#workers[@]}): ${workers[*]}"
kubectl get nodes "${workers[@]:0:6}" \
  -o custom-columns='NAME:.metadata.name,CPU:.status.allocatable.cpu,MEMORY:.status.allocatable.memory,UNSCHEDULABLE:.spec.unschedulable'
echo "Metrics API: available"
echo "Descheduler image: $DESCHEDULER_IMAGE"
echo "Experiment root: $ROOT"
