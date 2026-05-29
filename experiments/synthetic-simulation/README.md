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

## Scenarios

The script ships with two scenarios, selected via `--scenario`:

- `fragmentation` (default). Three workers (`2000m / 2000Mi` each) with Pod
  templates `C = 400m / 100Mi` (CPU-heavy) and `M = 100m / 400Mi`
  (memory-heavy). Initial layout `A = 4C`, `B = 2C + 4M`, `C = 3C + 2M`.
  Probe Pod `900m / 300Mi`. Aggregate free CPU is sufficient (`1800m`) but no
  single Node fits the probe, so the probe is initially Pending.
- `hidden-imbalance`. Three workers (`2000m / 2000Mi` each), every Pod
  declares an identical `250m / 250Mi` request. Runtime-usage profiles
  differ: `C-*` reports `400m / 100Mi`, `M-*` reports `100m / 400Mi`,
  `B-*` reports `250m / 250Mi`. Layout `A = 3C + 3B`, `B = 3M + 3B`,
  `C = 6B`. Per-Node request sums are identical, so request-based RII is
  zero everywhere; only the actual-usage signal reveals fragmentation.

## Usage modes

`--usage-mode` selects the signal consumed by RII and TOPSIS:

- `requests` (default). RII and TOPSIS read declared Pod requests, mirroring
  the production `UsageModeRequests` path.
- `actual-raw`. RII and TOPSIS read the per-Pod runtime usage carried in
  `Pod.actual_cpu` and `Pod.actual_mem` (falling back to requests when no
  actual values are set, matching the Go fallback). The feasibility guard
  still uses Pod requests against target free-by-requests space, since the
  default kube-scheduler admits Pods by their declared requests.

## Usage

```bash
python3 simulate.py                                                # fragmentation, requests, me=2
python3 simulate.py --max-evictions 1                              # MaxEvictions sweep
python3 simulate.py --threshold 0.1                                # threshold sweep
python3 simulate.py --probe-cpu 1000                               # tighter probe
python3 simulate.py --scenario hidden-imbalance --usage-mode requests --max-evictions 5
python3 simulate.py --scenario hidden-imbalance --usage-mode actual-raw --max-evictions 4
python3 simulate.py --quiet                                        # suppress TOPSIS internals
python3 simulate.py --no-scheduler                                 # skip post-eviction scheduler
python3 simulate.py --help
```

No external dependencies; standard library only (`argparse`, `copy`, `math`,
`dataclasses`).

## Thesis cross-reference

This script produces the numbers used in the synthetic walk-through tables
of the thesis report (Bab 3 illustrative and actual-usage TOPSIS scenarios,
Bab 4 synthetic walk-through validation, and Bab 4 actual-usage walk-through).
Re-running it should yield identical output.
