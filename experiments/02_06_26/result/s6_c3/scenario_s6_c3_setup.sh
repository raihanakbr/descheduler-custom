#!/usr/bin/env bash
# S6-C3 — C3-decisive corner (scarce-axis relief, beyond raw size).
#
# Drain node W1 is sparse and mem-leaning. C3 = ΔFSI = c·p_m + m·p_c + p_c·p_m
# with c (free cpu frac) large, so the c·p_m term dominates and C3 favors the
# MEM-heavy pod that relieves the scarce mem axis. That pod is deliberately NOT
# the largest by max(cpu,mem)/alloc (a bigger cpu pod is present), so just-c3
# should beat the `largest` heuristic here, and C1 (~0 on this less-skewed node)
# gives little signal. Tests C3's defrag-awareness vs pure footprint.
#
# Receivers (shared S5 landscape): W2 cpu-skewed, W3 mem-skewed, W4 balanced.
SCENARIO=s6-c3
source "$(dirname "$0")/../common.sh"

exp_setup_begin
i=0
for node in "${WORKERS[@]}"; do
  if   (( i == 0 )); then
    place_on_node "$node" "s6c3-cpubig-${node##*-}" 1 350m 20Mi    # biggest by max-share, cpu
    place_on_node "$node" "s6c3-membig-${node##*-}" 1 40m  240Mi   # mem-heavy (best ΔFSI, NOT largest)
    place_on_node "$node" "s6c3-bal-${node##*-}"    1 120m 80Mi    # small balanced
  elif (( i == 1 )); then
    place_on_node "$node" "s6c3-cpuskew-${node##*-}" 1 1300m 40Mi  # spare mem
  elif (( i == 2 )); then
    place_on_node "$node" "s6c3-memskew-${node##*-}" 1 60m  560Mi  # spare cpu
  elif (( i == 3 )); then
    place_on_node "$node" "s6c3-full-${node##*-}"    2 650m 250Mi  # balanced
  fi
  i=$((i + 1))
done
exp_setup_end
