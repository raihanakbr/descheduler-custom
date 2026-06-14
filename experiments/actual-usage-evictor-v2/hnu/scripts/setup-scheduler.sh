#!/usr/bin/env bash
set -euo pipefail

HNU_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SOURCE_CONFIG="$HNU_ROOT/scheduler/most-allocated-config.yaml"
TARGET_CONFIG="/etc/kubernetes/scheduler-config.yaml"
MANIFEST="/etc/kubernetes/manifests/kube-scheduler.yaml"
BACKUP="/etc/kubernetes/kube-scheduler.yaml.pre-hnu"

[[ -f "$SOURCE_CONFIG" ]] || {
  echo "ERROR: scheduler config not found: $SOURCE_CONFIG" >&2
  exit 1
}
[[ -f "$MANIFEST" ]] || {
  echo "ERROR: $MANIFEST not found; run this experiment on the control plane" >&2
  exit 1
}
command -v sudo >/dev/null || {
  echo "ERROR: sudo is required to configure the control-plane scheduler" >&2
  exit 1
}

echo "[hnu-scheduler] verifying passwordless sudo access"
sudo -n true >/dev/null 2>&1 || {
  echo "ERROR: passwordless sudo is required on the control plane" >&2
  exit 1
}
sudo -n python3 -c 'import yaml' >/dev/null 2>&1 || {
  echo "ERROR: PyYAML is required for root's python3 (install python3-yaml)" >&2
  exit 1
}
python3 - "$SOURCE_CONFIG" <<'PY'
import sys
import yaml

config = yaml.safe_load(open(sys.argv[1]))
kubeconfig = config.get("clientConnection", {}).get("kubeconfig")
if kubeconfig != "/etc/kubernetes/scheduler.conf":
    raise SystemExit(
        "ERROR: scheduler config must set "
        "clientConnection.kubeconfig=/etc/kubernetes/scheduler.conf"
    )
PY

if ! sudo -n test -f "$BACKUP"; then
  echo "[hnu-scheduler] backing up static Pod manifest to $BACKUP"
  sudo -n cp --preserve=mode,ownership,timestamps "$MANIFEST" "$BACKUP"
fi

echo "[hnu-scheduler] installing NodeResourcesFit/MostAllocated config"
sudo -n install -o root -g root -m 0644 "$SOURCE_CONFIG" "$TARGET_CONFIG"

echo "[hnu-scheduler] configuring kube-scheduler static Pod"
sudo -n python3 "$HNU_ROOT/scripts/configure-scheduler-manifest.py" \
  --manifest "$MANIFEST"

echo "[hnu-scheduler] waiting for kube-scheduler to reload"
deadline=$((SECONDS + 180))
while (( SECONDS < deadline )); do
  if kubectl -n kube-system wait \
    --for=condition=Ready pod \
    -l component=kube-scheduler \
    --timeout=5s >/dev/null 2>&1; then
    command_line="$(kubectl -n kube-system get pods -l component=kube-scheduler \
      -o jsonpath='{.items[0].spec.containers[0].command}' 2>/dev/null || true)"
    if grep -q -- '--config=/etc/kubernetes/scheduler-config.yaml' <<<"$command_line"; then
      echo "[hnu-scheduler] scheduler is Ready with $TARGET_CONFIG"
      printf '%s\n' "$command_line"
      exit 0
    fi
  fi
  sleep 3
done

echo "ERROR: kube-scheduler did not become Ready with the HNU config" >&2
kubectl -n kube-system get pods -l component=kube-scheduler -o wide >&2 || true
kubectl -n kube-system logs -l component=kube-scheduler --tail=80 >&2 || true
echo "[hnu-scheduler] restoring $BACKUP" >&2
sudo -n cp --preserve=mode,ownership,timestamps "$BACKUP" "$MANIFEST"
kubectl -n kube-system wait \
  --for=condition=Ready pod \
  -l component=kube-scheduler \
  --timeout=180s >/dev/null 2>&1 || true
exit 1
