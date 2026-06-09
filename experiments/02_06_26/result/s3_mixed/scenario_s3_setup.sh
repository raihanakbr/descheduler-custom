#!/usr/bin/env bash
# S3 - Mixed realistic (stresses O1 + O2 together).
#   workers 1-2: under-utilized   2 x (250m / 100Mi) = 500m  / 200Mi  (~25%/25%, should drain)
#   workers 3-4: cpu-skewed       2 x (750m / 20Mi)  = 1500m / 40Mi   (bad bin, should defrag)
#   workers 5-6: balanced-full    2 x (800m / 320Mi) = 1600m / 640Mi  (~80%/79%, should be KEPT)
SCENARIO=s3
source "$(dirname "$0")/../common.sh"

exp_setup_begin
i=0
for node in "${WORKERS[@]}"; do
  if   (( i < 2 )); then
    place_on_node "$node" "s3-under-${node##*-}" 2 250m 100Mi   # under-utilized
  elif (( i < 4 )); then
    place_on_node "$node" "s3-cpu-${node##*-}"   2 750m 20Mi    # cpu-skewed
  else
    place_on_node "$node" "s3-full-${node##*-}"  2 800m 320Mi   # balanced-full (keep)
  fi
  i=$((i + 1))
done
exp_setup_end
