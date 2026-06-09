#!/usr/bin/env bash
set -uo pipefail
cd "$(dirname "$0")"
declare -A MANIFEST=( [rd]=descheduler/rd-current.yaml [hnu]=descheduler/b1-hnu.yaml )
for tag in rd hnu; do
  echo "============================================================"
  echo "### S4 / $tag  ($(date +%H:%M:%S))"
  echo "============================================================"
  bash s4_hogs/scenario_s4_setup.sh >/dev/null 2>&1 || echo "SETUP FAILED s4/$tag"
  RUN_TAG="$tag" SUT_MANIFEST="${MANIFEST[$tag]}" bash s4_hogs/perform_and_capture.sh
  bash s4_hogs/cleanup.sh >/dev/null 2>&1
done
echo "### S4 DONE ($(date +%H:%M:%S))"
