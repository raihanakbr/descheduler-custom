# Consolidation experiment — requests mode

Comparative evaluation harness for the **ResourceDefragmentation** descheduler
(SUT) on this fixed cluster: **1 control-plane + 6 workers**, each worker
**2000m CPU / 830876Ki (~811Mi)** allocatable, requests mode only.

Everything is plain `kubectl` (`cordon` / `uncordon` / `apply` / `rollout` /
`delete`) plus one `python3` metrics script. No metrics-server needed —
`pause` pods reserve their CPU/mem **requests** and use ~nothing, so the load is
exactly what we declare.

```
experiment/
  common.sh                 # shared helpers (sourced by every script)
  metrics.py                # tool-agnostic metrics from kubectl json (N, S, H)
  descheduler/
    sut-requests.yaml       # ResourceDefragmentation, requests, threshold 0.40, scoped to defrag-exp
  scheduler/
    most-allocated-config.yaml   # REQUIRED scheduler profile (reference)
  s1_underutilized/         # scenario_s1_setup.sh  perform_and_capture.sh  cleanup.sh
  s2_fragmented/            # "  (the discriminating scenario)
  s3_mixed/                 # "
  results/<scenario>/<ts>/  # created per run: raw json, per-pass logs, metrics, summary
```

## Before you run anything (fairness preconditions)

1. **Scheduler must pack.** The plugin assumes MostAllocated + BalancedAllocation.
   Confirm `/etc/kubernetes/scheduler-config.yaml` on the control-plane matches
   `scheduler/most-allocated-config.yaml`. With the default (LeastAllocated)
   scheduler the cluster spreads pods back and consolidation cannot happen.
2. Same scheduler profile, same workloads, same threshold for **every** system
   you compare (B1 HighNodeUtilization `thresholds{cpu:40,memory:40}` ==
   SUT `consolidationThreshold: 0.40`).

## Why setup cordons nodes

Under a MostAllocated scheduler, simply applying Deployments would pack them at
deploy time — there would be no under-utilized / fragmented "before" state to
measure. So setup **cordons every worker, uncordons one at a time** to seed each
node, then **uncordons all** before the descheduler runs. The pods carry no
`nodeName`/affinity, so the descheduler can move them freely (fairness control:
RS-owned, DefaultEvictor-eligible).

## Run one scenario

```bash
cd experiment
chmod +x */*.sh                 # first time only

./s2_fragmented/scenario_s2_setup.sh      # seed the fragmented layout
./s2_fragmented/perform_and_capture.sh    # before -> descheduler passes -> after
./s2_fragmented/cleanup.sh                # reset for the next run
```

Re-run `perform_and_capture.sh` across **>=5 seeds** (the workload is
deterministic but placement order is not) and report mean ± 95% CI per the plan.

### Useful overrides (env vars)

```bash
MAX_PASSES=6 ./s2_fragmented/perform_and_capture.sh    # cap descheduler re-runs
SETTLE_SECONDS=30 ./s2_fragmented/perform_and_capture.sh
```

## What gets captured (`results/<scenario>/<timestamp>/`)

| File | Purpose |
|---|---|
| `metrics_before.txt` / `metrics_after.txt` | N_active, N_empty, S, H_balanced, H_skewed (+ `METRICS_JSON` line) |
| `nodes_*.json` / `pods_*.json` | raw cluster state both snapshots (recompute anything later) |
| `desched_passN.log` | descheduler log per pass |
| `events_descheduled.txt` | eviction events (tool-agnostic E cross-check; O6 safety) |
| `pdb.txt` | PDB state (O6) |
| `summary.txt` | per-pass evictions, total E, convergence |

**E** (evictions) is reported as the count of `Eviction decision` log lines per
pass; cross-check against `events_descheduled.txt`.

## Mapping to the objectives

- **O1** node reclamation: `N_active` (↓), `N_empty` (↑)
- **O2** stranding: `S` before→after (↓)
- **O3** headroom: `H_balanced`, `H_skewed` before→after (↑)
- **O4** efficiency: ΔS / E, ΔN_active / E
- **O5** stability: `per_pass_evictions` (later passes → 0)
- **O6** safety: `events_descheduled.txt`, `pdb.txt`, any Pending pods in `pods_after.txt`

## Comparing against B0 / B1 / B2

- **B0** (no descheduler): run setup + the two snapshots, skip the descheduler
  passes (`MAX_PASSES=0`).
- **B1 / B2** (HighNodeUtilization / LowNodeUtilization): point the run at a
  different manifest with the same namespace scope and 0.40 threshold —
  `SUT_MANIFEST=/path/to/b1.yaml ./s2_fragmented/perform_and_capture.sh`.
  metrics.py is tool-agnostic, so the numbers stay comparable.

## Scenarios (sizes scaled to 2000m / 811Mi workers)

| Scenario | Per-node seed | Intent |
|---|---|---|
| **S1** under-utilized | every worker 2×(250m/100Mi) ≈ 25%/25% | pure consolidation (O1) |
| **S2** fragmented | W1–3 cpu-skewed 2×(750m/20Mi); W4–6 mem-skewed 2×(50m/240Mi) | reducible stranding (O2/O3) |
| **S3** mixed | W1–2 under-util; W3–4 cpu-skewed; W5–6 balanced-full 2×(800m/320Mi, kept) | O1+O2 together |
