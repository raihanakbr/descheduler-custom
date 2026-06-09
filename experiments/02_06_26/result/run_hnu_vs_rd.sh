#!/usr/bin/env bash
# Driver: compare current ResourceDefragmentation (rd-current.yaml) vs
# HighNodeUtilization baseline (b1-hnu.yaml), same image/scope/threshold.
# For each scenario and each strategy: fresh setup -> capture -> cleanup.
set -uo pipefail
cd "$(dirname "$0")"

declare -A SETUP=(
  [s1]=s1_underutilized/scenario_s1_setup.sh
  [s2]=s2_fragmented/scenario_s2_setup.sh
  [s3]=s3_mixed/scenario_s3_setup.sh
)
declare -A PERF=(
  [s1]=s1_underutilized/perform_and_capture.sh
  [s2]=s2_fragmented/perform_and_capture.sh
  [s3]=s3_mixed/perform_and_capture.sh
)
declare -A CLEAN=(
  [s1]=s1_underutilized/cleanup.sh
  [s2]=s2_fragmented/cleanup.sh
  [s3]=s3_mixed/cleanup.sh
)
declare -A MANIFEST=(
  [rd]=descheduler/rd-current.yaml
  [hnu]=descheduler/b1-hnu.yaml
)

for sc in s1 s2 s3; do
  for tag in rd hnu; do
    echo "============================================================"
    echo "### $sc / $tag  ($(date +%H:%M:%S))"
    echo "============================================================"
    bash "${SETUP[$sc]}"                                          || { echo "SETUP FAILED $sc/$tag"; }
    RUN_TAG="$tag" SUT_MANIFEST="${MANIFEST[$tag]}" bash "${PERF[$sc]}"
    bash "${CLEAN[$sc]}"
  done
done
echo "### ALL RUNS DONE ($(date +%H:%M:%S))"
