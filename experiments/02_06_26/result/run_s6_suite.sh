#!/usr/bin/env bash
# S6 necessity suite: corner scenarios x 12 selection policies x N seeds.
# One image (alpha-9), only selectionPolicy changes per run within a scenario.
# Results land in results/<scenario>/<ts>-<policy>-k<seed>/ (scenario tags:
# s5, s6-c1, s6-c3, s6-c4, s6-mix). Aggregate with aggregate_suite.py.
#
#   SEEDS=3 ./run_s6_suite.sh
#   SCENARIOS="s6-mix" POLICIES="topsis just-c2" SEEDS=1 ./run_s6_suite.sh   # subset
set -uo pipefail
cd "$(dirname "$0")"

SEEDS="${SEEDS:-3}"
SCENARIOS="${SCENARIOS:-s5 s6-c1 s6-c3 s6-c4 s6-mix}"
POLICIES="${POLICIES:-topsis just-c1 just-c2 just-c3 just-c4 no-c1 no-c2 no-c3 no-c4 random largest lowest-priority}"
PENALTY="${PENALTY:-0}"

TMPL="descheduler/ablation/sut-ablation.tmpl.yaml"
GEN_DIR="descheduler/ablation/generated"; mkdir -p "$GEN_DIR"

# scenario tag -> directory holding {scenario_*_setup,perform_and_capture,cleanup}.sh
setup_script() {
  case "$1" in
    s5)     echo "s5_heterogeneous/scenario_s5_setup.sh" ;;
    s6-c1)  echo "s6_c1/scenario_s6_c1_setup.sh" ;;
    s6-c3)  echo "s6_c3/scenario_s6_c3_setup.sh" ;;
    s6-c4)  echo "s6_c4/scenario_s6_c4_setup.sh" ;;
    s6-mix) echo "s6_mix/scenario_s6_mix_setup.sh" ;;
    *) echo ""; ;;
  esac
}
scen_dir() { dirname "$(setup_script "$1")"; }

echo "### S6 SUITE START ($(date +%F\ %H:%M:%S))  seeds=$SEEDS penalty=$PENALTY"
echo "### scenarios: $SCENARIOS"
echo "### policies:  $POLICIES"

for scenario in $SCENARIOS; do
  setup="$(setup_script "$scenario")"; dir="$(scen_dir "$scenario")"
  if [[ -z "$setup" ]]; then echo "UNKNOWN scenario '$scenario', skipping"; continue; fi
  for policy in $POLICIES; do
    for k in $(seq 1 "$SEEDS"); do
      manifest="$GEN_DIR/sut-${scenario}-${policy}-k${k}.yaml"
      sed -e "s|__POLICY__|${policy}|g" -e "s|__SEED__|${k}|g" \
          -e "s|__PENALTY__|${PENALTY}|g" "$TMPL" > "$manifest"
      echo "============================================================"
      echo "### $scenario / policy=$policy seed=$k/$SEEDS ($(date +%H:%M:%S))"
      echo "============================================================"
      bash "$setup" >/dev/null 2>&1 || echo "SETUP FAILED $scenario/$policy/$k"
      RUN_TAG="${policy}-k${k}" SUT_MANIFEST="$PWD/$manifest" \
        bash "$dir/perform_and_capture.sh"
      bash "$dir/cleanup.sh" >/dev/null 2>&1
      until [ "$(kubectl -n defrag-exp get pods --no-headers 2>/dev/null | grep -c .)" = "0" ]; do
        sleep 2
      done
    done
  done
done
echo "### S6 SUITE DONE ($(date +%F\ %H:%M:%S))"
