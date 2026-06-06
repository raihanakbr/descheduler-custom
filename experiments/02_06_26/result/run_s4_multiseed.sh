#!/usr/bin/env bash
# S4 (hogs+jumbo) multi-seed: confirm the RD -66%/free-a-node vs HNU no-op split is stable.
set -uo pipefail
cd "$(dirname "$0")"
SEEDS="${SEEDS:-5}"
declare -A MANIFEST=( [rd]=descheduler/rd-current.yaml [hnu]=descheduler/b1-hnu.yaml )
for tag in rd hnu; do
  for k in $(seq 1 "$SEEDS"); do
    echo "### S4 / $tag seed $k/$SEEDS ($(date +%H:%M:%S))"
    bash s4_hogs/scenario_s4_setup.sh >/dev/null 2>&1 || echo "SETUP FAILED s4/$tag/$k"
    RUN_TAG="${tag}-k${k}" SUT_MANIFEST="${MANIFEST[$tag]}" bash s4_hogs/perform_and_capture.sh
    bash s4_hogs/cleanup.sh >/dev/null 2>&1
    until [ "$(kubectl -n defrag-exp get pods --no-headers 2>/dev/null | grep -c .)" = "0" ]; do sleep 2; done
  done
done
echo "### S4 MULTISEED DONE ($(date +%H:%M:%S))"
