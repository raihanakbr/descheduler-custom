#!/usr/bin/env bash
# S5 selector ablation. One image (alpha-9), one scenario (heterogeneous drain
# node), one knob changed per run: selectionPolicy. Everything else fixed, so a
# metric (S/H) difference isolates the selector.
#
#   POLICIES="topsis just-c1 ..."  SEEDS=5  ./run_s5_ablation.sh
#
# Defaults run the full sweep (core MCDM-justification + optional per-criterion
# necessity). Each (policy, seed) = fresh setup -> descheduler-to-convergence ->
# teardown. Results land in results/s5/<ts>-<policy>-k<seed>/.
set -uo pipefail
cd "$(dirname "$0")"

SEEDS="${SEEDS:-5}"
# core = MCDM justification; optional = per-criterion necessity
POLICIES="${POLICIES:-topsis just-c1 just-c2 just-c3 random largest no-c1 no-c2 no-c3}"

TMPL="descheduler/ablation/sut-ablation.tmpl.yaml"
GEN_DIR="descheduler/ablation/generated"
mkdir -p "$GEN_DIR"

echo "### S5 ABLATION START ($(date +%F\ %H:%M:%S))  seeds=$SEEDS"
echo "### policies: $POLICIES"

for policy in $POLICIES; do
  for k in $(seq 1 "$SEEDS"); do
    manifest="$GEN_DIR/sut-${policy}-k${k}.yaml"
    sed -e "s|__POLICY__|${policy}|g" -e "s|__SEED__|${k}|g" \
        -e "s|__PENALTY__|${PENALTY:-0}|g" "$TMPL" > "$manifest"

    echo "============================================================"
    echo "### S5 / policy=$policy seed=$k/$SEEDS ($(date +%H:%M:%S))"
    echo "============================================================"
    bash s5_heterogeneous/scenario_s5_setup.sh >/dev/null 2>&1 \
      || echo "SETUP FAILED s5/$policy/$k"
    RUN_TAG="${policy}-k${k}" SUT_MANIFEST="$PWD/$manifest" \
      bash s5_heterogeneous/perform_and_capture.sh
    bash s5_heterogeneous/cleanup.sh >/dev/null 2>&1
    # wait for the namespace to fully drain before the next run (fairness)
    until [ "$(kubectl -n defrag-exp get pods --no-headers 2>/dev/null | grep -c .)" = "0" ]; do
      sleep 2
    done
  done
done
echo "### S5 ABLATION DONE ($(date +%F\ %H:%M:%S))"
