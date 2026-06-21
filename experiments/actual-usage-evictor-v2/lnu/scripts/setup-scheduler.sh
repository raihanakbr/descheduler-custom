#!/usr/bin/env bash
set -euo pipefail

LNU_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PARENT_ROOT="$(cd "$LNU_ROOT/.." && pwd)"
HNU_CONFIG="/etc/kubernetes/scheduler-config.yaml"

scheduler_command="$(kubectl -n kube-system get pods -l component=kube-scheduler \
  -o jsonpath='{.items[0].spec.containers[0].command}' 2>/dev/null || true)"

if [[ -z "$scheduler_command" ]]; then
  echo "ERROR: unable to read the kube-scheduler command" >&2
  exit 1
fi

if grep -q -- "--config=$HNU_CONFIG" <<<"$scheduler_command"; then
  echo "[lnu-scheduler] HNU MostAllocated configuration detected"
  "$PARENT_ROOT/hnu/scripts/restore-scheduler.sh"
  exit 0
fi

if grep -q -- '--config=' <<<"$scheduler_command"; then
  echo "ERROR: kube-scheduler uses an unrecognized custom configuration:" >&2
  printf '%s\n' "$scheduler_command" >&2
  echo "LNU setup will not overwrite a scheduler configuration it did not create." >&2
  exit 1
fi

echo "[lnu-scheduler] default scheduler detected; LeastAllocated is already active"
printf '%s\n' "$scheduler_command"
