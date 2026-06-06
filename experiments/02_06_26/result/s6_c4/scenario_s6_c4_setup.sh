#!/usr/bin/env bash
# S6-C4 — C4-decisive corner (priority is the deciding signal).
#
# Drain node W1 holds three differently-shaped pods with DISTINCT priorities. The
# pod whose relocation minimizes global stranding (the mem-heavy pod -> mem-skewed
# receiver's complementary cpu... here -> cpu-skewed receiver's spare mem) is given
# the LOW priority, while the resource criteria (C1/C2/C3) marginally prefer a
# higher-priority pod. C4 is a cost criterion (lower priority preferred), so
# just-c4 / lowest-priority pick the right pod; dropping C4 (no-c4) should flip
# TOPSIS to the worse pick -> demonstrates C4 contributes.
#
# Requires PriorityClasses (applied below). Receivers: shared S5 landscape.
SCENARIO=s6-c4
source "$(dirname "$0")/../common.sh"

kubectl apply -f "$(dirname "$0")/../s6_priorityclasses.yaml" >/dev/null

exp_setup_begin
i=0
for node in "${WORKERS[@]}"; do
  if   (( i == 0 )); then
    place_on_node "$node" "s6c4-cpu-${node##*-}" 1 400m 20Mi  defrag-high  # cpu-heavy, HIGH prio
    place_on_node "$node" "s6c4-mem-${node##*-}" 1 40m  200Mi defrag-low   # mem-heavy, LOW prio (best to move)
    place_on_node "$node" "s6c4-bal-${node##*-}" 1 150m 80Mi  defrag-med   # balanced, MED prio
  elif (( i == 1 )); then
    place_on_node "$node" "s6c4-cpuskew-${node##*-}" 1 1300m 40Mi  # spare mem
  elif (( i == 2 )); then
    place_on_node "$node" "s6c4-memskew-${node##*-}" 1 60m  560Mi  # spare cpu
  elif (( i == 3 )); then
    place_on_node "$node" "s6c4-full-${node##*-}"    2 650m 250Mi  # balanced
  fi
  i=$((i + 1))
done
exp_setup_end
