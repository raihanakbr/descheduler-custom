#!/usr/bin/env bash
# balancePenaltyWeight (λ) sub-sweep — secondary, not part of the N1-N4 gate.
# Holds selectionPolicy=topsis on one scenario and sweeps λ, which tightens the
# target balance-gate inside predictSchedulerTarget (and thus C2 feasibility).
# Results -> results/<scenario>/<ts>-topsis-lambdaXX-k<seed>/.
#
#   SCEN=s6-mix LAMBDAS="0.0 0.5 0.7 1.0" SEEDS=3 ./run_s6_penalty.sh
set -uo pipefail
cd "$(dirname "$0")"

SCEN="${SCEN:-s6-mix}"
LAMBDAS="${LAMBDAS:-0.0 0.5 0.7 1.0}"
SEEDS="${SEEDS:-3}"

case "$SCEN" in
  s5)     SETUP="s5_heterogeneous/scenario_s5_setup.sh" ;;
  s6-c1)  SETUP="s6_c1/scenario_s6_c1_setup.sh" ;;
  s6-c3)  SETUP="s6_c3/scenario_s6_c3_setup.sh" ;;
  s6-c4)  SETUP="s6_c4/scenario_s6_c4_setup.sh" ;;
  s6-mix) SETUP="s6_mix/scenario_s6_mix_setup.sh" ;;
  *) echo "unknown SCEN=$SCEN"; exit 1 ;;
esac
DIR="$(dirname "$SETUP")"
TMPL="descheduler/ablation/sut-ablation.tmpl.yaml"
GEN_DIR="descheduler/ablation/generated"; mkdir -p "$GEN_DIR"

echo "### S6 PENALTY SWEEP START scen=$SCEN lambdas=[$LAMBDAS] seeds=$SEEDS ($(date +%H:%M:%S))"
for lam in $LAMBDAS; do
  tag="lambda$(echo "$lam" | tr -d '.')"
  for k in $(seq 1 "$SEEDS"); do
    manifest="$GEN_DIR/sut-${SCEN}-topsis-${tag}-k${k}.yaml"
    sed -e "s|__POLICY__|topsis|g" -e "s|__SEED__|${k}|g" \
        -e "s|__PENALTY__|${lam}|g" "$TMPL" > "$manifest"
    echo "### $SCEN / topsis λ=$lam seed=$k/$SEEDS ($(date +%H:%M:%S))"
    bash "$SETUP" >/dev/null 2>&1 || echo "SETUP FAILED $SCEN/$lam/$k"
    RUN_TAG="topsis-${tag}-k${k}" SUT_MANIFEST="$PWD/$manifest" \
      bash "$DIR/perform_and_capture.sh" | grep -E "S \(strand|eviction decision|total_evict"
    bash "$DIR/cleanup.sh" >/dev/null 2>&1
    until [ "$(kubectl -n defrag-exp get pods --no-headers 2>/dev/null | grep -c .)" = "0" ]; do sleep 2; done
  done
done
echo "### S6 PENALTY SWEEP DONE ($(date +%H:%M:%S))"
