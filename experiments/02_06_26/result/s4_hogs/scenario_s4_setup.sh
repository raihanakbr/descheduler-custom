#!/usr/bin/env bash
# S4 - Hogs + one jumbo, 5/6 nodes filled (1 node left empty). Uses the home-manifest
# shapes ~/cpu-hog.yaml (600m/24Mi), ~/mem-hog.yaml (60m/240Mi), ~/jumbo-pods.yaml (920m/375Mi):
#   workers 1-2: cpu-hog  3 x (600m / 24Mi)  = 1800m / 72Mi   (~0.95 cpu / 0.15 mem w/ daemonset)
#   workers 3-4: mem-hog  3 x (60m  / 240Mi) = 180m  / 720Mi  (~0.14 cpu / 0.95 mem)
#   worker  5  : jumbo    1 x (920m / 375Mi) = 920m  / 375Mi  (~0.51 cpu / 0.52 mem, big & balanced)
#   worker  6  : (left empty)
# The jumbo pod is large and balanced, so it does NOT fit on either hog type
# (cpu nodes lack cpu, mem nodes lack mem); the only complementary headroom is on
# the hog nodes' stranded axis, which RD can exploit by merging the hog nodes.
SCENARIO=s4
source "$(dirname "$0")/../common.sh"

exp_setup_begin
i=0
for node in "${WORKERS[@]}"; do
  if   (( i < 2 )); then
    place_on_node "$node" "s4-cpu-${node##*-}"   3 600m 24Mi    # cpu-hog
  elif (( i < 4 )); then
    place_on_node "$node" "s4-mem-${node##*-}"   3 60m  240Mi   # mem-hog
  elif (( i < 5 )); then
    place_on_node "$node" "s4-jumbo-${node##*-}" 1 920m 375Mi   # jumbo (big, balanced)
  fi
  # worker 6 (i==5): intentionally left empty
  i=$((i + 1))
done
exp_setup_end
