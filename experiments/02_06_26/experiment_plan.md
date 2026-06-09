# Comparative Evaluation: Consolidation Deschedulers

**Cluster:** 1 control-plane + 6 workers (fixed node set; no autoscaling).
**Goal:** measure ResourceDefragmentation against established consolidation/rebalancing deschedulers
on identical workloads, using tool-agnostic, cluster-observable metrics.

---

## 1. Systems under comparison

| ID | System | Role | Notes |
|---|---|---|---|
| **B0** | Scheduler only (no descheduler) | Control / lower bound | "do nothing" reference |
| **B1** | descheduler **HighNodeUtilization** | Consolidation baseline | the direct comparator — drains under-utilized nodes to pack |
| **B2** | descheduler **LowNodeUtilization** | Spreading reference | opposite objective; shows the consolidation–balance axis |
| **SUT** | **ResourceDefragmentation** (yours) | Consolidation **+** defrag | claims both node reclamation and stranding reduction |

> Node-lifecycle consolidators (Karpenter, cluster-autoscaler scale-down) are **out of scope**: they add/remove
> nodes, which this fixed 6-worker cluster doesn't exercise. If you later test on a cloud node group, add them
> as B3/B4 with "nodes removed" as the headline metric.

## 2. Fairness controls (must hold across ALL systems)

1. **Same cluster** (6 workers, same allocatable) and **same scheduler profile** (pin it; verify restart).
2. **Same initial workload** per scenario (identical Deployments/replicas/requests), deployed the same way
   (no nodeName/affinity, RS-owned so DefaultEvictor accepts them).
3. **Same usage signal**: all tools read the same source (`requests`, or actual usage if metrics-server is
   installed — then enable it for every tool).
4. **Equivalent thresholds**: "under-utilized" must mean the same %. e.g. HighNodeUtilization
   `thresholds {cpu:40, memory:40}` ↔ ResourceDefragmentation `consolidationThreshold: 0.40`.
5. **Same eviction budget**: cap total evictions equally (HighNodeUtilization
   `maxNoOfPodsToEvict*` ↔ `maxEvictions`) so "cost" is measured under the same ceiling.
6. **Same measurement protocol**: run to steady state (multiple passes until convergence), then snapshot.
7. **No PDBs differing between runs** (or identical PDBs for all).

## 3. Objectives (the comparison axes)

Each objective is one metric on which the systems are ranked. The last column is the *a priori* expectation —
useful to state up front so results are falsifiable.

| # | Objective | Metric (lower/higher better) | Why it matters | Expected leader |
|---|---|---|---|---|
| **O1** | **Node reclamation** | `N_active` (↓), empty workers (↑) | fewer nodes = cost/scale-down | B1 & SUT |
| **O2** | **Stranding reduction** | `S = Σ alloc·\|cpuFrac−memFrac\|` (↓) | reclaim wasted complementary capacity | **SUT** |
| **O3** | **Schedulability headroom** | `H(shape)` = extra reference pods that still fit (↑), for a *balanced* and a *skewed* shape | can the cluster accept new work after | **SUT** (esp. skewed shape) |
| **O4** | **Migration efficiency** | objective gain per eviction = ΔO1/`E`, ΔO2/`E` (↑) | achieve more with less disruption | tie / SUT |
| **O5** | **Stability / convergence** | pass-2 residual evictions (↓), passes-to-converge (↓), re-eviction rate (↓) | avoid churn / ping-pong | depends |
| **O6** | **Safety & disruption** | PDB violations (=0), pending pod-seconds (↓) | production-safety | all should be 0 |

**The discriminating claim:** B1 (HighNodeUtilization) should win/tie on **O1** but be weak on **O2/O3**
(it packs onto whatever node, often *creating* stranding); B2 (LowNodeUtilization) is the inverse (good
spread, bad O1). SUT should be the only one strong on **O1 and O2/O3 simultaneously**. That joint result is
the thesis evidence.

## 4. Workload scenarios (neutral, identical for every system)

> Reference worker = 4 vCPU / 8 GiB. Express loads as per-node fractions; scale to your nodes.

| ID | Scenario | Initial layout across W1–W6 | Stresses |
|---|---|---|---|
| **S1** | **Under-utilized** | every worker ~20–30% **balanced** load | O1 (pure consolidation) |
| **S2** | **Fragmented / complementary** | 3 workers cpu-skewed (~75% cpu / 5% mem), 3 workers mem-skewed (~5% cpu / 60% mem), all "looking" busy | O2, O3 (stranding) |
| **S3** | **Mixed realistic** | 2 under-utilized + 2 cpu-skewed + 2 balanced-full | O1+O2 together |
| **S4** | **Dynamic churn** (optional) | S3 with pods created/deleted every 60 s for 20 min | O5 (steady-state stability) |

S2 is the key scenario: total cpu ≈ total mem cluster-wide, so the stranding is **reducible** (pairing a
cpu-skewed pod with a mem-skewed node balances both). A pure packer can't exploit that; a defragmenter can.

## 5. Metrics & instrumentation (tool-agnostic)

All computed from `kubectl get nodes/pods -o json` (a small script), **not** from any tool's logs:

- `N_active` — workers carrying ≥1 experiment pod; `N_empty` — schedulable workers with 0.
- `S` — cluster stranding (formula above), using the same usage signal as the run.
- `H(balanced)` and `H(skewed)` — count additional reference pods (e.g. 450m/900Mi and 1500m/100Mi) that
  still fit by requests.
- `E` — evictions counted from `Event`/`pods/eviction` API (works for any descheduler).
- `pending pod-seconds`, `PDB denials` — from pod phase transitions and eviction responses.
- Per-pass `E` to measure convergence/oscillation (O5).

## 6. Procedure

For each **system × scenario**:
1. Deploy scheduler profile + workload; wait for initial scheduling to settle; **snapshot "before"**.
2. Run the descheduler (B0 = skip). Re-run on its normal interval until **two consecutive passes evict 0**
   (converged) or a max of K passes.
3. Let placement settle; **snapshot "after"**; record per-pass `E`.
4. Tear down; repeat **≥5 seeds** (workload is deterministic but placement order isn't); report mean ± 95% CI.
5. Compare systems with a non-parametric test (Mann–Whitney U) per objective.

## 7. Results template

| System | Scenario | N before→after | S before→after | H_skew before→after | E (p1/p2) | PDB viol | Verdict |
|---|---|---|---|---|---|---|---|
| B0 | S2 | … | … | … | 0/0 | 0 | baseline |
| B1 | S2 | … | … | … | … | … | … |
| B2 | S2 | … | … | … | … | … | … |
| SUT | S2 | … | … | … | … | … | … |

Plus per-objective ranking tables (O1…O6) and the efficiency plot ΔS vs `E` (O4).

## 8. Threats to validity / fairness notes
- **Threshold alignment is the biggest fairness risk** — document the exact knobs per tool and that they
  encode the same "under-utilized" definition; ideally sweep thresholds and compare at matched aggressiveness.
- **Scheduler profile dominates outcomes** — all systems must run under the *same* profile; state which one.
  (Your plugin assumes MostAllocated+BalancedAllocation; B1/B2 are profile-agnostic but their re-placement
  isn't, so this affects everyone equally — disclose it.)
- **Fixed nodes** mean O1 is "nodes emptied," not "nodes deleted"; note that B1's real-world payoff
  (autoscaler removing the emptied node) isn't captured here.
- Report `usageMode`; `requests` vs actual can change S and candidacy for all tools.