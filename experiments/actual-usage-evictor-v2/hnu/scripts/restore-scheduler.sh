#!/usr/bin/env bash
set -euo pipefail

MANIFEST="/etc/kubernetes/manifests/kube-scheduler.yaml"
BACKUP="/etc/kubernetes/kube-scheduler.yaml.pre-hnu"
TARGET_CONFIG="/etc/kubernetes/scheduler-config.yaml"

[[ -f "$MANIFEST" ]] || {
  echo "ERROR: $MANIFEST not found; run this command on the control plane" >&2
  exit 1
}
command -v sudo >/dev/null || {
  echo "ERROR: sudo is required to restore the control-plane scheduler" >&2
  exit 1
}
sudo -n true >/dev/null 2>&1 || {
  echo "ERROR: passwordless sudo is required on the control plane" >&2
  exit 1
}
sudo -n test -f "$BACKUP" || {
  echo "ERROR: HNU scheduler backup not found: $BACKUP" >&2
  exit 1
}

echo "[hnu-scheduler] restoring original static Pod manifest from $BACKUP"
sudo -n cp --preserve=mode,ownership,timestamps "$BACKUP" "$MANIFEST"

echo "[hnu-scheduler] waiting for kube-scheduler to reload"
kubectl -n kube-system wait \
  --for=condition=Ready pod \
  -l component=kube-scheduler \
  --timeout=180s >/dev/null

deadline=$((SECONDS + 180))
while (( SECONDS < deadline )); do
  command_line="$(kubectl -n kube-system get pods -l component=kube-scheduler \
    -o jsonpath='{.items[0].spec.containers[0].command}' 2>/dev/null || true)"
  if ! grep -q -- "--config=$TARGET_CONFIG" <<<"$command_line"; then
    echo "[hnu-scheduler] original scheduler manifest restored"
    printf '%s\n' "$command_line"
    exit 0
  fi
  sleep 3
done

echo "ERROR: kube-scheduler is Ready but still uses $TARGET_CONFIG" >&2
exit 1
