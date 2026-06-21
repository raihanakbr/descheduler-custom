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

if [[ -n "${ACTIVE_WORKERS:-}" ]]; then
  read -r -a selected_workers <<<"$ACTIVE_WORKERS"
else
  selected_workers=("${workers[@]:0:6}")
fi
if (( ${#selected_workers[@]} != 6 )); then
  echo "ERROR: ACTIVE_WORKERS must contain exactly 6 node names" >&2
  exit 1
fi

echo "Context: $(kubectl config current-context)"
echo "Workers (${#workers[@]}): ${workers[*]}"
kubectl get nodes "${selected_workers[@]}" \
  -o custom-columns='NAME:.metadata.name,CPU:.status.allocatable.cpu,MEMORY:.status.allocatable.memory,UNSCHEDULABLE:.spec.unschedulable'
for node in "${selected_workers[@]}"; do
  cpu="$(kubectl get node "$node" -o jsonpath='{.status.allocatable.cpu}')"
  memory="$(kubectl get node "$node" -o jsonpath='{.status.allocatable.memory}')"
  python3 - "$node" "$cpu" "$memory" <<'PY'
import sys

name, cpu_value, memory_value = sys.argv[1:]

def cpu_m(value):
    return int(value[:-1]) if value.endswith("m") else int(float(value) * 1000)

def mem_mi(value):
    units = {"Ki": 1 / 1024, "Mi": 1, "Gi": 1024}
    for suffix, multiplier in units.items():
        if value.endswith(suffix):
            return float(value[:-len(suffix)]) * multiplier
    return float(value) / (1024 * 1024)

cpu = cpu_m(cpu_value)
memory = mem_mi(memory_value)
if not 1900 <= cpu <= 2100 or not 780 <= memory <= 850:
    raise SystemExit(
        f"ERROR: worker {name} has {cpu}m/{memory:.1f}Mi allocatable; "
        "the fixed S2 layout requires reference-sized "
        "workers near 2000m CPU and 811Mi memory"
    )
PY
done
echo "Metrics API: available"
echo "Descheduler image: $DESCHEDULER_IMAGE"
echo "Experiment root: $ROOT"
