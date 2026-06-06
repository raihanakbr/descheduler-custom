#!/usr/bin/env bash
# S1 - Under-utilized: every worker ~25% balanced load (stresses O1, pure consolidation).
# Per node: 2 x (250m / 100Mi)  ->  500m / 200Mi  (~25% cpu, ~25% mem on a 2000m/811Mi node).
SCENARIO=s1
source "$(dirname "$0")/../common.sh"

exp_setup_begin
for node in "${WORKERS[@]}"; do
  place_on_node "$node" "s1-fill-${node##*-}" 2 250m 100Mi
done
exp_setup_end
