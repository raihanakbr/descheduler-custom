#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RESOURCE="${1:?usage: run-suite.sh <cpu|memory>}"
REPEATS="${REPEATS:-5}"

for repeat in $(seq 1 "$REPEATS"); do
  for system in N0 R0 R1 H0 H1; do
    "$ROOT/scripts/run-cell.sh" "$RESOURCE" "$system" "$repeat"
  done
done

python3 "$ROOT/scripts/aggregate-results.py" "$RESOURCE" --pattern "${LOAD_PATTERN:-sustained}"
