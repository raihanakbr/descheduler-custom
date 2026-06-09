#!/usr/bin/env bash
# Re-run S1/S2/S3 on the CURRENT SUT config (rd-current.yaml: alpha-8, target 0.9, lambda 0.7)
# so the main report's headline table is normalized to one config.
set -uo pipefail
cd "$(dirname "$0")"
declare -A SETUP=( [s1]=s1_underutilized/scenario_s1_setup.sh [s2]=s2_fragmented/scenario_s2_setup.sh [s3]=s3_mixed/scenario_s3_setup.sh )
declare -A PERF=( [s1]=s1_underutilized/perform_and_capture.sh [s2]=s2_fragmented/perform_and_capture.sh [s3]=s3_mixed/perform_and_capture.sh )
declare -A CLEAN=( [s1]=s1_underutilized/cleanup.sh [s2]=s2_fragmented/cleanup.sh [s3]=s3_mixed/cleanup.sh )
for sc in s1 s2 s3; do
  echo "### $sc / current ($(date +%H:%M:%S))"
  bash "${SETUP[$sc]}" >/dev/null 2>&1 || echo "SETUP FAILED $sc"
  RUN_TAG="current" SUT_MANIFEST="descheduler/rd-current.yaml" bash "${PERF[$sc]}"
  bash "${CLEAN[$sc]}" >/dev/null 2>&1
  until [ "$(kubectl -n defrag-exp get pods --no-headers 2>/dev/null | grep -c .)" = "0" ]; do sleep 2; done
done
echo "### S123 CURRENT DONE ($(date +%H:%M:%S))"
