#!/usr/bin/env bash
# S3 multi-seed pass: repeat S3 N times for each strategy to see whether the
# single-seed result (RD 6->5, HNU 6->4) is typical or placement-order luck.
# Each repetition: fresh setup -> capture -> cleanup. Tagged rd-kN / hnu-kN.
set -uo pipefail
cd "$(dirname "$0")"

SEEDS="${SEEDS:-5}"
SETUP=s3_mixed/scenario_s3_setup.sh
PERF=s3_mixed/perform_and_capture.sh
CLEAN=s3_mixed/cleanup.sh
declare -A MANIFEST=( [rd]=descheduler/rd-current.yaml [hnu]=descheduler/b1-hnu.yaml )

for tag in rd hnu; do
  for k in $(seq 1 "$SEEDS"); do
    echo "============================================================"
    echo "### S3 / $tag  seed $k/$SEEDS  ($(date +%H:%M:%S))"
    echo "============================================================"
    bash "$SETUP" >/dev/null 2>&1 || echo "SETUP FAILED s3/$tag/$k"
    RUN_TAG="${tag}-k${k}" SUT_MANIFEST="${MANIFEST[$tag]}" bash "$PERF"
    bash "$CLEAN" >/dev/null 2>&1
  done
done
echo "### S3 MULTISEED DONE ($(date +%H:%M:%S))"
