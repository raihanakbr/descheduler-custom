# Synthetic Simulation: ResourceDefragmentation

Deterministic Python walk-through of the ResourceDefragmentation descheduler.
Mirrors `pkg/framework/plugins/resourcedefragmentation/resourcedefragmentation.go`
function-for-function so the calculations in the thesis can be cross-validated
against the production implementation without running a real cluster.

## What it does

Builds a synthetic 3-worker cluster, runs the same `Balance()` loop as the Go
plugin (RII -> FSI -> priority -> TOPSIS over candidate Pods -> feasibility
guard -> eviction -> cache update), and prints every intermediate value: the
node state cache, fragmented-node priority order, the raw / normalized /
weighted TOPSIS decision matrix, the ideal best/worst per criterion, the d+/d-
separations, the closeness coefficient, the per-target feasibility decision,
the post-eviction node state, and the final probe schedulability outcome.

A small kube-scheduler-style stage is then run on the post-eviction state
(`NodeResourcesFit` filter, `LeastAllocated` mean(CPU, mem) score, lexicographic
tiebreak) under both probe-first and replacement-first scheduling orders so the
script also shows where the evicted Pods' replacements land.

## Function map

| Go (`resourcedefragmentation.go`) | Python (`simulate.py`)        |
|-----------------------------------|-------------------------------|
| `computeRII`                      | `compute_rii`                 |
| `computeFSI`                      | `compute_fsi`                 |
| `computePriorityIndex`            | `compute_priority`            |
| `computeC1`                       | `compute_c1`                  |
| `evaluateFeasibleTargets`         | `evaluate_feasible_targets`   |
| `computeC2`                       | `compute_c2`                  |
| `computeC3`                       | `compute_c3`                  |
| `computeC4`                       | `compute_c4`                  |
| `topsis`                          | `topsis`                      |
| `Balance`                         | `balance`                     |

The simulation runs in `UsageModeRequests`, so pod usage equals pod requests.
This matches the request-based variant evaluated in the thesis.

## Default scenario

Three workers, each `2000m / 2000Mi` allocatable. Pod templates `C = 400m/100Mi`
(CPU-heavy) and `M = 100m/400Mi` (memory-heavy). Initial layout `A=4C`,
`B=2C+4M`, `C=3C+2M`. Probe Pod requests `900m / 300Mi`. Threshold `0.2`,
`MaxEvictions=2`. The probe is initially Pending because aggregate free CPU is
sufficient (1800m) but no single Node fits the 900m request.

## Usage

```bash
python3 simulate.py                              # baseline (threshold=0.2, me=2)
python3 simulate.py --max-evictions 1            # MaxEvictions sweep
python3 simulate.py --threshold 0.1              # threshold sweep
python3 simulate.py --probe-cpu 1000             # tighter probe
python3 simulate.py --quiet                      # suppress TOPSIS internals
python3 simulate.py --no-scheduler               # skip post-eviction scheduler
python3 simulate.py --help
```

No external dependencies; standard library only (`argparse`, `copy`, `math`,
`dataclasses`).

## Thesis cross-reference

This script produces the numbers used in the synthetic walk-through tables of
the thesis report (Bab 3 illustrative TOPSIS scenario and Bab 4 synthetic
walk-through validation). Re-running it should yield identical output.
