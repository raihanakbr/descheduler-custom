#!/usr/bin/env bash
# S6-MIX — the necessity "money shot": no single criterion suffices.
#
# Drain node W1 carries FOUR heterogeneous pods (cpu-heavy, mem-heavy, balanced,
# and a small low-priority misfit) with a priority spread, against tight
# complementary receivers. The intent is that every single-criterion / heuristic
# selector mis-picks (strands a pod or an axis) while the full TOPSIS blend picks
# the ordering that fully drains W1 and minimizes global stranding. This is the
# strongest single piece of "TOPSIS is robust / each criterion contributes"
# evidence. Numbers are a starting point; tuned via probe until topsis is
# best-or-tied and the single-criterion policies show large regret.
#
# Requires PriorityClasses. Receivers: shared S5 landscape (tightened a touch).
SCENARIO=s6-mix
source "$(dirname "$0")/../common.sh"

kubectl apply -f "$(dirname "$0")/../s6_priorityclasses.yaml" >/dev/null

exp_setup_begin
i=0
for node in "${WORKERS[@]}"; do
  if   (( i == 0 )); then
    place_on_node "$node" "s6mix-cpu-${node##*-}" 1 450m 20Mi  defrag-high  # cpu-heavy
    place_on_node "$node" "s6mix-mem-${node##*-}" 1 40m  200Mi defrag-med   # mem-heavy
    place_on_node "$node" "s6mix-bal-${node##*-}" 1 160m 80Mi  defrag-high  # balanced
    place_on_node "$node" "s6mix-sml-${node##*-}" 1 60m  40Mi  defrag-low   # small low-prio misfit
  elif (( i == 1 )); then
    place_on_node "$node" "s6mix-cpuskew-${node##*-}" 1 1400m 40Mi  # spare mem (tight cpu)
  elif (( i == 2 )); then
    place_on_node "$node" "s6mix-memskew-${node##*-}" 1 60m  600Mi  # spare cpu (tight mem)
  elif (( i == 3 )); then
    place_on_node "$node" "s6mix-full-${node##*-}"    2 680m 270Mi  # balanced, fuller
  fi
  i=$((i + 1))
done
exp_setup_end
