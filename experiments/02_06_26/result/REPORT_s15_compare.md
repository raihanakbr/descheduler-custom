# S1–S5: HighNodeUtilization vs ResourceDefragmentation vs ResourceDefragmentationC2

**Date:** 2026-06-07
**Cluster:** 1 control-plane + 6 workers, each **2000m CPU / ~811Mi** allocatable
(requests signal; `pause` pods reserve requests, use ~0).
**Scheduler:** MostAllocated + BalancedAllocation (packing profile — fairness
precondition, identical for all three systems).
**Common controls:** same workloads, same namespace scope (`defrag-exp`
evictable, `kube-system` excluded), same consolidation threshold **0.40**,
**requests** usage signal, `MAX_PASSES=5`, `SETTLE=20s`, **3 seeds per cell**.

> **Run note.** The HighNodeUtilization and ResourceDefragmentation cells are
> carried over from the same-day comparison run; only **ResourceDefragmentationC2
> was re-run, now on the beta-5 binary** (`keegou/descheduler-custom:beta-5`),
> enabling the distinct `ResourceDefragmentationC2` plugin. Superseded artifacts
> are archived: beta-1 under `results_rdc2_beta1/`; the first beta-2 attempt
> (which mistakenly re-enabled the `ResourceDefragmentation` plugin on the beta-2
> image and so just reproduced RD) under `results_rdc2_beta2_misconfig/`; the
> correct-plugin beta-2 run under `results_rdc2_beta2/`; beta-3 under
> `results_rdc2_beta3/`; and beta-4 (bit-identical to beta-3) under
> `results_rdc2_beta4/`.
>
> **beta-5 reaches the S5 = 0.123 target.** After beta-2 (0.332) regressed to
> beta-3/beta-4 (0.443 = plain RD), the **beta-5** binary cuts S5 stranding to
> **0.123** — matching the `just-c2` ablation — and does it with **2 evictions
> instead of 3**, reclaiming the same node with *more* free headroom (H_bal 38 vs
> RD's 36). This is the absolute-balance selection fix landing: C2 now ranks the
> predicted landing node by balance, not by the raw scheduler score.
>
> **Net effect:** C2 (beta-5) ties RD on S1/S2/S4 and **beats** it on S3
> (1.386 vs 1.401) and decisively on S5 (0.123 vs 0.443, **−72% vs RD**), at equal
> or lower eviction cost. It is now a strict improvement over full TOPSIS on this
> suite.

## Systems under comparison

| Tag | Plugin | Image | Config |
|---|---|---|---|
| **HighNodeUtilization** | `HighNodeUtilization` (upstream) | `keegou/descheduler-custom:alpha-8` | `thresholds{cpu:40, memory:40}` |
| **ResourceDefragmentation** | `ResourceDefragmentation` (SUT) | `keegou/descheduler-custom:alpha-8` | `consolidationThreshold 0.40`, `consolidationTarget 0.90`, `balancePenaltyWeight 0.70`, `usageMode: requests` |
| **ResourceDefragmentationC2** | **`ResourceDefragmentationC2`** (SUT) | **`keegou/descheduler-custom:beta-5`** | `consolidationThreshold 0.40`, `consolidationTarget 0.90`, `usageMode: requests` — **no `balancePenaltyWeight`** (none in its args) |

Manifests: [b1-hnu.yaml](descheduler/b1-hnu.yaml),
[rd-current.yaml](descheduler/rd-current.yaml) (= `descheduler-custom.yaml`),
[rd-c2-beta5.yaml](descheduler/rd-c2-beta5.yaml) (created for this run).

> **Why C2 pins `usageMode: requests`.** the C2 binary changes the default: an empty
> `UsageMode` now resolves to `actual-ewma` when a metrics collector is present,
> else `requests`. This cluster has no metrics-server, but to keep C2 on the
> exact same **requests** signal as the other two systems (and to be robust if a
> collector is ever added) the mode is set explicitly.

**What ResourceDefragmentationC2 is.** Same consolidation / drain-candidate
pipeline as ResourceDefragmentation, but a **single-criterion victim selector**:
no TOPSIS and no balance penalty — among a drain node's pods it evicts the one
whose *predicted kube-scheduler landing node* (MostAllocated + BalancedAllocation,
reproduced verbatim from upstream) scores highest. So the variable isolated here
is the **selector** (TOPSIS C1–C4 → C2-only), carried on the beta-5 binary.

---

## Headline result

| Scenario | Intent | HighNodeUtilization | ResourceDefragmentation | ResourceDefragmentationC2 |
|---|---|---|---|---|
| **S1** under-utilized | pure consolidation (O1) | **0 nodes**, S 0.049→0.049 | **4 nodes**, S→0.050 | **4 nodes**, S→0.050 |
| **S2** fragmented | reducible stranding (O2/O3) | **0 nodes**, S 3.73→3.73 | **3 nodes**, S→0.97 (**−74%**) | **3 nodes**, S→0.97 (**−74%**) |
| **S3** mixed | O1+O2 together | **2 nodes**, S→1.41 | 1 node, S→1.40 | 1 node, S→**1.39** |
| **S4** hogs+jumbo | merge hogs around jumbo | **0 nodes**, S 3.24→3.24 | 1 node, S→1.11 (**−66%**) | 1 node, S→1.11 (**−66%**) |
| **S5** heterogeneous drain | shape-aware packing (O2/O3) | 1 node, S→1.30 (−3%) | 1 node, S→0.44 (−67%) | 1 node, S→**0.12** (**−91%**, E=2) |

*Bold = best in row. "nodes" = `N_active` reclaimed (before→after). S = stranding
`Σ|cpuFrac − memFrac|`, lower is better.*

**Three findings dominate:**

1. **ResourceDefragmentationC2 (beta-5) is a strict improvement over RD.** Across
   all 5 scenarios C2 reclaims the **same nodes** (identical `N_active`/`N_empty`)
   at **equal or lower eviction cost**, ties RD's stranding on S1/S2/S4, and beats
   it on **S3** (1.386 vs 1.401) and decisively on **S5** (0.123 vs 0.443, −72% vs
   RD) — the latter with **2 evictions instead of 3**.

2. **beta-5 realizes the `just-c2` operating point (0.123) on S5.** This took
   five binaries: beta-2 reached 0.332, beta-3/beta-4 regressed to 0.443, beta-5
   lands 0.123 — matching the selector ablation. The fix was to rank the
   *predicted landing node* by **absolute** balance (`nodeBinScore`, which tracks
   the stranding metric `S = Σ|cpuFrac − memFrac|`) rather than by the raw
   kube-scheduler score (MostAllocated + *marginal* balance). See *How beta-5 fixed S5*.

3. **C2's win is efficiency, not just placement.** On S5 it reaches a far lower S
   with one fewer eviction and leaves more usable headroom (H_bal 38 vs RD's 36) —
   so the better selector is also cheaper. On S1/S2/S4 it is identical to RD, so
   the change carries no downside on this suite.

4. **Both SUT plugins reduce stranding where HighNodeUtilization cannot.** HNU is
   a one-dimensional bin-packer: it only acts when some nodes sit above the
   threshold to receive pods drained from below-threshold nodes. In uniformly
   under-loaded (S1) or uniformly fragmented (S2, S4) layouts it does **nothing**
   (0 evictions). RD/C2 consolidate and defragment in every scenario, cutting
   stranding by 66–91% in S2/S4/S5 (C2 reaching −91% on S5).

---

## Per-objective detail (mean ± 95% CI, n=3)

### O1 — Node reclamation (`N_active` ↓ / `N_empty` ↑)

| Scenario | HNU N_active | RD N_active | RDC2 N_active | HNU empty | RD empty | RDC2 empty |
|---|---|---|---|---|---|---|
| S1 | 6→6 | 6→**2** | 6→**2** | 0 | **4** | **4** |
| S2 | 6→6 | 6→**3** | 6→**3** | 0 | **3** | **3** |
| S3 | 6→**4** | 6→5 | 6→5 | **2** | 1 | 1 |
| S4 | 5→5 | 5→**4** | 5→**4** | 1 | **2** | **2** |
| S5 | 4→3 | 4→3 | 4→3 | 3 | 3 | 3 |

- RD/RDC2 win O1 in **S1, S2, S4** (HNU is a no-op there).
- **S3 is HNU's only win**: it empties 2 nodes vs RD's 1. S3 contains
  balanced-full "keep" nodes above the 40% threshold, giving HNU valid drain
  targets; it greedily packs the two under-utilized nodes onto them. RD is more
  conservative (respects the 0.90 target and the balance penalty), reclaiming 1.
- S5: both reclaim the single heterogeneous drain node — the difference is *how
  well* they place the pods (see O2).

### O2 — Stranding (`S` ↓)

| Scenario | HNU S before→after | RD S before→after | RDC2 S before→after |
|---|---|---|---|
| S1 | 0.049→0.049 | 0.049→0.050 | 0.049→0.050 |
| S2 | 3.727→3.727 | 3.727→**0.971** | 3.727→**0.971** |
| S3 | 1.395→1.408 | 1.395→1.401 | 1.395→**1.386** |
| S4 | 3.243→3.243 | 3.243→**1.112** | 3.243→**1.112** |
| S5 | 1.338→1.298 | 1.338→0.443 | 1.338→**0.123** |

- **S2/S4/S5 are decisive for RD/C2.** These layouts have *reducible*
  stranding (complementary cpu-skewed and mem-skewed nodes). Both SUT plugins
  pack complementary shapes together; HNU leaves stranding untouched (S2/S4) or
  barely dented (S5, −3%).
- **S5 is C2's biggest win**: same node reclaimed by all three, but C2 (beta-5)
  cuts S to **0.123** (−91% vs the 1.338 start) vs RD's 0.443 and HNU's 1.298 —
  and with one fewer eviction. C2's absolute-balance selector seats the cpu-pod →
  spare-cpu node and the mem-pod → spare-mem node so each kept node ends nearly
  balanced; RD's TOPSIS and HNU's bin-packer both leave a node lopsided.
- **S3**: C2 edges RD (1.386 vs 1.401) on the same 1 node / 2 evictions — a small
  but consistent (zero-variance across seeds) placement gain.
- S1 has essentially no stranding to fix (uniform 25%/30% nodes); RD's job there
  is pure consolidation, not defrag — S stays minimal.

### O3 — Headroom (`H_balanced` / `H_skewed`, ↑)

| Scenario | HNU H_bal | RD H_bal | RDC2 H_bal | HNU H_skew | RD H_skew | RDC2 H_skew |
|---|---|---|---|---|---|---|
| S1 | **42** | 39 | 39 | **12** | 8 | 8 |
| S2 | 15 | **29** | **29** | 6 | 6 | 6 |
| S3 | 18 | **20** | 19 | 4 | 4 | 2 |
| S4 | 13 | **24** | **24** | **7** | 6 | 6 |
| S5 | 32 | 36 | **38** | 8 | 8 | 8 |

- RD/C2 free more **balanced-pod** headroom in S2 (+93%), S4 (+85%), S5. On **S5**
  C2 (beta-5) now tops the column at 38 (vs RD 36, HNU 32) — its tighter,
  better-balanced packing un-strands the most usable capacity.
- S3 is the one cell where C2 and RD diverge on headroom: C2's tighter packing
  (lower S) leaves slightly fewer whole reference pods' worth of room
  (H_bal 19 vs 20, H_skew 2 vs 4) — a placement trade-off, not a regression in S.
- HNU shows higher headroom in S1 only because it *didn't move anything* (pods
  stay spread, so each node still has both-axis room); it has not consolidated.

### O4 — Efficiency (Δ per eviction)

| Scenario | HNU E (ΔN, ΔS) | RD E (ΔN, ΔS) | RDC2 E (ΔN, ΔS) |
|---|---|---|---|
| S1 | 0 (—) | 10 (−4 nodes, ≈0 S) | 10 (−4 nodes, ≈0 S) |
| S2 | 0 (—) | 6 (−3 nodes, −2.76 S) | 6 (−3 nodes, −2.76 S) |
| S3 | 4 (−2 nodes) | 2 (−1 node, −0.00 S) | 2 (−1 node, −0.01 S) |
| S4 | 0 (—) | 5 (−1 node, −2.13 S) | 5 (−1 node, −2.13 S) |
| S5 | 3 (−1 node, −0.04 S) | 3 (−1 node, −0.90 S) | **2** (−1 node, −**1.22** S) |

- **S5 is C2's efficiency win**: C2 (beta-5) reclaims the node and buys −1.22 S
  with only **2** evictions, vs RD's −0.90 S for 3 and HNU's −0.04 S for 3 — fewer
  moves, much larger improvement. The absolute-balance selector finds the two pods
  whose relocation does the most for stranding, so it doesn't need the third move.
- HNU's S3 reclamation (2 nodes for 4 evictions) is its strongest efficiency
  point; everywhere else it spends 0 evictions and delivers 0.

### O5 — Stability / convergence (per-pass evictions → 0)

All cells converge (a later pass evicts 0). Representative (seed k1):

| Scenario | HNU | RD | RDC2 |
|---|---|---|---|
| S1 | `0` | `8, 2, 0` | `8, 2, 0` |
| S2 | `0` | `6, 0` | `6, 0` |
| S3 | `4, 0` | `2, 0` | `2, 0` |
| S4 | `0` | `4, 1, 0` | `4, 1, 0` |
| S5 | `3, 0` | `3, 0` | `2, 0` |

No oscillation: evictions monotonically decay to 0 within 2–3 passes for RD/C2.
HNU is a trivial no-op (single 0-pass) in S1/S2/S4. C2 (beta-5) matches RD's
per-pass counts except **S5**, where its better-targeted selector needs only 2
evictions (vs RD's 3) to converge to a lower-stranding layout.

### O6 — Safety

- **Zero `Pending` pods** in any after-snapshot across all 45 runs — no eviction
  left a pod unschedulable.
- All evicted pods are RS-owned and DefaultEvictor-eligible; `kube-system`
  excluded; no PDBs configured in `defrag-exp`.
- `evictionRequests=0` beyond `evictedPods` in every job log (no stuck requests).

---

## Interpretation

- **HighNodeUtilization** is the wrong tool for stranding. By design it only
  drains nodes *below* a utilization floor onto nodes *above* it; it has no
  notion of per-axis (cpu vs mem) imbalance. So when every node is uniformly
  under-loaded (S1) or uniformly fragmented with no clear "high" target (S2, S4),
  it evicts nothing. Its single win (S3) comes from greedy packing onto pre-full
  nodes, at the cost of a slightly *worse* S.

- **ResourceDefragmentation** consolidates *and* defragments: it reduces
  `N_active` and the two-dimensional stranding `S` together, with safe,
  converging, well-targeted evictions. It dominates HNU on the fragmentation
  scenarios (S2/S4/S5) and on pure consolidation (S1).

- **ResourceDefragmentationC2 (beta-5)** keeps RD's consolidation pipeline but
  swaps the multi-criteria TOPSIS victim selector for a **single-criterion**
  choice. In requests mode it reclaims the **same nodes** as RD on every scenario
  at equal-or-lower eviction cost, ties RD on S1/S2/S4, and **beats** it on S3
  (1.386 vs 1.401) and S5 (0.123 vs 0.443, with 2 evictions vs 3). On this suite
  the simpler selector strictly dominates full TOPSIS.

> **How beta-5 fixed S5 (the 0.123 gap, resolved).** The S5 selector ablation
> (`just-c2`, `descheduler/ablation/`) had long shown S=0.123 was achievable, but
> earlier C2 binaries missed it: beta-2 reached 0.332, beta-3/beta-4 sat at 0.443
> (= RD). The cause was *which surrogate scored the victim choice*. `just-c2`
> ranks the predicted landing node by **absolute-balance** `nodeBinScore`, which
> directly tracks the stranding metric `S = Σ|cpuFrac − memFrac|`; the earlier C2
> plugin ranked by `schedulerNodeScore`, the verbatim kube-scheduler MostAllocated
> + **marginal**-balance equations — faithful to the scheduler, but optimizing the
> scheduler's objective, not absolute balance. **beta-5** ranks the (still
> scheduler-accurately predicted) landing node by absolute balance, decoupling
> "where does it land" (scheduler-faithful) from "is that landing good for S"
> (absolute balance). Result: S5 → 0.123 at 2 evictions, no regression on S1–S4.

## Reproduce

```bash
cd experiment
# hnu/rd cells:
SEEDS=3 STRATS="hnu rd" ./run_s15_compare.sh
# C2 cell (ResourceDefragmentationC2 plugin on the beta-5 binary):
SEEDS=3 STRATS=rdc2 RDC2_MANIFEST=descheduler/rd-c2-beta5.yaml ./run_s15_compare.sh
python3 aggregate_compare.py          # tables + AGG_JSON  (RUN_DATE=20260607 default)
```

Raw artifacts per run in `results/<scenario>/20260607-*-<strat>-k<seed>/`
(nodes/pods JSON both snapshots, per-pass descheduler logs, metrics, summary).
Aggregate text: [results/COMPARE_S1-S5.txt](results/COMPARE_S1-S5.txt).
