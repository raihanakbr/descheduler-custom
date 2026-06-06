#!/usr/bin/env bash
# S2 - Fragmented / complementary (the discriminating scenario; stresses O2, O3).
#   workers 1-3: cpu-skewed  ~75% cpu / ~5% mem   -> 2 x (750m / 20Mi)  = 1500m / 40Mi
#   workers 4-6: mem-skewed  ~5% cpu / ~60% mem   -> 2 x (50m  / 240Mi) = 100m  / 480Mi
# Cluster cpu ~= mem, so the stranding is REDUCIBLE: pairing a cpu-skewed pod with a
# mem-skewed node balances both. A pure packer can't exploit that; a defragmenter can.
SCENARIO=s2
source "$(dirname "$0")/../common.sh"

exp_setup_begin
i=0
for node in "${WORKERS[@]}"; do
  if (( i < 3 )); then
    place_on_node "$node" "s2-cpu-${node##*-}" 2 750m 20Mi    # cpu-skewed
  else
    place_on_node "$node" "s2-mem-${node##*-}" 2 50m 240Mi    # mem-skewed
  fi
  i=$((i + 1))
done
exp_setup_end
