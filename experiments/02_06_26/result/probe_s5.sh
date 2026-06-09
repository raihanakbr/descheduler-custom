#!/usr/bin/env bash
# One-seed probe across contrasting policies to confirm the scenario discriminates
# (and to inspect why) before launching the full multi-seed sweep.
set -uo pipefail
cd "$(dirname "$0")"
POLICIES="${POLICIES:-topsis just-c1 just-c2 just-c3 random largest}"
TMPL="descheduler/ablation/sut-ablation.tmpl.yaml"
GEN_DIR="descheduler/ablation/generated"; mkdir -p "$GEN_DIR"
for policy in $POLICIES; do
  manifest="$GEN_DIR/sut-${policy}-probe.yaml"
  sed -e "s|__POLICY__|${policy}|g" -e "s|__SEED__|1|g" \
      -e "s|__PENALTY__|${PENALTY:-0}|g" "$TMPL" > "$manifest"
  echo "################ PROBE policy=$policy ($(date +%H:%M:%S)) ################"
  bash s5_heterogeneous/scenario_s5_setup.sh >/dev/null 2>&1 || echo "SETUP FAILED $policy"
  RUN_TAG="${policy}-probe" SUT_MANIFEST="$PWD/$manifest" bash s5_heterogeneous/perform_and_capture.sh \
    | grep -E "METRICS \(after\)|S \(strand|N_active|H_balanced|eviction decision|total_evictions"
  bash s5_heterogeneous/cleanup.sh >/dev/null 2>&1
  until [ "$(kubectl -n defrag-exp get pods --no-headers 2>/dev/null | grep -c .)" = "0" ]; do sleep 2; done
done
echo "### PROBE DONE ($(date +%H:%M:%S))"
