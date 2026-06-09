#!/usr/bin/env bash
# S5 - Heterogeneous drain node (the selector-ablation scenario).
#
# Exactly ONE under-utilized node carries three differently-shaped pods
# (cpu-hog + mem-hog + balanced) on the SAME node, so when the descheduler
# drains it the *selector* must decide which pod to move where. The receiving
# nodes expose deliberately complementary, shape-specific headroom so that a
# good (TOPSIS) selector can pack each pod into its complementary stranded
# axis while a naive selector mis-packs or fails to fully drain. Everything
# else in the pipeline is fixed, so a metric (S/H) difference isolates the
# selectionPolicy.
#
# Worker facts: 2000m CPU / ~811Mi alloc, ~100m/50Mi already used by daemonsets.
#
#   W1 (drain) : cpu  1x(400m/20Mi)  ~0.35 cpu               <- heterogeneous,
#                mem  1x(40m /160Mi)  /0.38 mem (both <0.40)     under threshold
#                bal  1x(160m/80Mi)                          -> must be drained
#   W2 (keep)  : cpu-skewed 1x(1300m/40Mi)  ~0.70 cpu /0.11 mem  spare mem (complements mem-pod)
#   W3 (keep)  : mem-skewed 1x(60m /560Mi)  ~0.08 cpu /0.75 mem  spare cpu (complements cpu-pod)
#   W4 (keep)  : balanced   2x(650m/250Mi)  ~0.65 cpu /0.65 mem  balanced spare
#   W5, W6     : (left empty)
#
# All three receivers have headroom WITHIN the consolidationTarget=0.90 ceiling,
# but their *stranded* axes differ, so where each drain pod lands changes global
# stranding S. An optimal drain (cpu->W3, mem->W2, bal->W4) empties W1 and fills
# complementary axes; a naive selector mis-targets and leaves residual stranding.
SCENARIO=s5
source "$(dirname "$0")/../common.sh"

exp_setup_begin
i=0
for node in "${WORKERS[@]}"; do
  if   (( i == 0 )); then
    # heterogeneous drain node: three shapes on the SAME worker
    place_on_node "$node" "s5-cpu-${node##*-}" 1 400m 20Mi    # cpu-hog
    place_on_node "$node" "s5-mem-${node##*-}" 1 40m  160Mi   # mem-hog
    place_on_node "$node" "s5-bal-${node##*-}" 1 160m 80Mi    # balanced
  elif (( i == 1 )); then
    place_on_node "$node" "s5-cpuskew-${node##*-}" 1 1300m 40Mi   # cpu-skewed (spare mem)
  elif (( i == 2 )); then
    place_on_node "$node" "s5-memskew-${node##*-}" 1 60m  560Mi   # mem-skewed (spare cpu)
  elif (( i == 3 )); then
    place_on_node "$node" "s5-full-${node##*-}"    2 650m 250Mi   # balanced (keep)
  fi
  # workers 5,6 (i==4,5): intentionally left empty
  i=$((i + 1))
done
exp_setup_end
