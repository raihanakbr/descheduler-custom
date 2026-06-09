#!/usr/bin/env bash
# S6-C1 — C1-decisive corner (origin de-skew).
#
# Drain node W1 is a SPARSE, cpu-SKEWED node (candidate via low avgUtilization).
# nodeRII = cpuFrac - memFrac > 0, so C1 = nodeRII*podRII*maxShare is maximized by
# evicting the large cpu-heavy pod (the one whose skew matches the node's). Moving
# that pod de-skews W1 and lands it on the mem-skewed receiver's spare cpu, which
# minimizes global stranding. The C2 (target bin-score) and priority criteria are
# meant to point at a different pod, so just-c1 ~ topsis should beat just-c2/c4.
#
# Receivers (shared S5 landscape): W2 cpu-skewed (spare mem), W3 mem-skewed
# (spare cpu), W4 balanced. W5/W6 empty.
SCENARIO=s6-c1
source "$(dirname "$0")/../common.sh"

exp_setup_begin
i=0
for node in "${WORKERS[@]}"; do
  if   (( i == 0 )); then
    place_on_node "$node" "s6c1-cpubig-${node##*-}" 1 550m 15Mi    # cpu-heavy, large (skew-match)
    place_on_node "$node" "s6c1-cpumed-${node##*-}" 1 350m 15Mi    # cpu-heavy, medium
    place_on_node "$node" "s6c1-mem-${node##*-}"    1 50m  130Mi   # mem-heavy, small
  elif (( i == 1 )); then
    place_on_node "$node" "s6c1-cpuskew-${node##*-}" 1 1300m 40Mi  # spare mem
  elif (( i == 2 )); then
    place_on_node "$node" "s6c1-memskew-${node##*-}" 1 60m  560Mi  # spare cpu
  elif (( i == 3 )); then
    place_on_node "$node" "s6c1-full-${node##*-}"    2 650m 250Mi  # balanced
  fi
  i=$((i + 1))
done
exp_setup_end
