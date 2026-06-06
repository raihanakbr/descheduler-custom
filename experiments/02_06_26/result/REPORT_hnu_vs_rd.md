# ResourceDefragmentation vs HighNodeUtilization — strategy comparison

**Date:** 2026-06-06
**Question:** how does the *current* custom descheduler config compare against the
stock **HighNodeUtilization (HNU)** consolidation baseline, holding everything
else fixed?

**Fairness controls (only the strategy changes):**

| Held constant | Value |
|---|---|
| Image | `keegou/descheduler-custom:alpha-8` (same binary for both) |
| Cluster | 1 control-plane + 6 workers, 2000m / ~811Mi each |
| Scheduler | MostAllocated + BalancedAllocation (packing) |
| Usage mode | requests (pause pods; HNU also computes usage from requests) |
| Threshold | RD `consolidationThreshold: 0.40` == HNU `thresholds{cpu:40,memory:40}` |
| Eviction scope | only `defrag-exp` pods evictable (system/flannel are DaemonSet-owned, non-evictable) |
| Workloads | identical S1/S2/S3 seeds |

- **RD** = `descheduler/rd-current.yaml` (= `descheduler-custom.yaml`):
  ResourceDefragmentation, target 0.9, **balancePenaltyWeight λ=0.7**.
- **HNU** = `descheduler/b1-hnu.yaml`: stock HighNodeUtilization, same image/scope/threshold.

Raw artifacts: `results/s{1,2,3}/20260606-04*-{rd,hnu}/` (now also includes
`pods_before.txt`). One seed per cell.

## Results

`N_active`↓ better, `N_empty`↑ better, `S` (stranding)↓ better,
`H_balanced` (balanced pods that still fit)↑ better, `E` = pods evicted.

| Scenario | Strategy | N_active | N_empty | S (strand) | H_balanced | H_skewed | **E** |
|---|---|---|---|---|---|---|---|
| **S1** under-util | RD  | 6 → **2** | 0 → **4** | 0.049 → 0.050 | 42 → 39 | 12 → 8 | 10 |
|               | HNU | 6 → 6 | 0 → 0 | 0.049 → 0.049 | 42 → 42 | 12 → 12 | **0** |
| **S2** fragmented | RD  | 6 → **3** | 0 → **3** | **3.73 → 0.97 (−74%)** | **15 → 29** | 6 → 6 | 6 |
|               | HNU | 6 → 6 | 0 → 0 | 3.73 → 3.73 | 15 → 15 | 6 → 6 | **0** |
| **S3** mixed | RD  | 6 → 5 | 0 → 1 | 1.395 → 1.401 | 20 → 20 | 4 → 4 | 2 |
|               | HNU | 6 → **4** | 0 → **2** | 1.395 → 1.408 | 20 → **18** | 4 → 4 | 4 |
| **S4** hogs+jumbo | RD  | 5 → **4** | 1 → 2 | **3.24 → 1.11 (−66%)** | **13 → 24** | 7 → 6 | 5 |
|               | HNU | 5 → 5 | 1 → 1 | 3.24 → 3.24 (**no-op**) | 13 → 13 | 7 → 7 | 0 |

> **E-measurement note:** the per-run `summary.txt` shows `E=0` for every HNU run
> because it counted RD's custom `Eviction decision` log line, which HNU does not
> emit. The table above uses the framework-level `totalEvicted` / `"Evicted pod"`
> count (S3/HNU truly evicted **4**). `common.sh` has been fixed to count the
> tool-agnostic `"Evicted pod"` line going forward, so future baseline runs report
> E correctly.

## Read-out

### S1 (uniformly under-utilized) — RD wins decisively
Every worker sits at 30%/31% (below 40%), so HNU classifies **all 6 nodes as
under-utilized and finds zero "high" nodes to pack into → 0 evictions, no change.**
This is the structural limit of HighNodeUtilization: it only drains an
under-threshold node *toward* an over-threshold one, so a uniformly light cluster
gives it no destination. RD consolidates the same layout **6 → 2** (4 nodes freed).

### S2 (fragmented / complementary) — RD wins decisively
W1–3 are cpu-skewed (75% cpu / 5% mem), W4–6 mem-skewed (5% cpu / 60% mem). Each
node already exceeds 40% on *one* resource, so HNU sees **no under-utilized node →
0 evictions, S unchanged at 3.73.** RD pairs complementary pods (a cpu-skewed pod
onto a mem-skewed node and vice-versa), cutting stranding **−74% (3.73 → 0.97)**,
nearly doubling schedulable balanced headroom (**H_balanced 15 → 29**), and freeing
**3 nodes**. This is the scenario the SUT is built for, and the gap is total.

### S3 (mixed) — a genuine trade-off
Two under-util + two cpu-skewed + two balanced-full nodes.
- **HNU frees more nodes (6 → 4 vs RD's 6 → 5):** it drains *both* under-util
  nodes. But it does so blindly — it dumps two balanced (250m/100Mi) pods onto the
  already **cpu-skewed** nodes (34, 39), pushing them to ~87% cpu while their memory
  stays stranded. Net: **S edges up (1.395 → 1.408)** and **H_balanced drops 20 → 18**
  — it bought node count by spending balance/future schedulability.
- **RD frees one node and holds balance flat** (S ~1.40, H_balanced 20). λ=0.7 makes
  it *refuse* to move the cpu-skewed pods ("No feasible target within ceiling"),
  because any such move only spreads skew. It drains the one under-util node whose
  pods have a balance-preserving home and stops. The remaining stranding (~1.38) is
  the two cpu-skewed nodes with **no complementary mem pods to pair** — a feasibility
  floor, not a tuning miss.

So S3 is "more nodes (HNU) vs better balance/headroom (RD)," not a clean win for
either — exactly the O1-vs-O2/O3 tension.

## Safety (O6)
**No Pending pods in any after-state** for either strategy (checked across all six
runs). On these scenarios HNU's lack of a node-fit guard did not strand anything,
because the MostAllocated scheduler always found a feasible home; on tighter
clusters that is not guaranteed.

## Bottom line

| Objective | Winner |
|---|---|
| **O1** node reclamation, light clusters (S1) | **RD** (6→2 vs no-op) |
| **O1** node reclamation, mixed (S3) | HNU (6→4 vs 6→5) |
| **O2/O3** stranding + headroom (S2) | **RD** (−74% S, H 15→29 vs no change) |
| **Robustness** (acts at all) | **RD** — HNU is a no-op whenever no node is already > 40% |

HNU only consolidates when the cluster *already* has a hot node to pack into and a
strictly-under-threshold node to drain; in two of three scenarios that condition
never holds and it does nothing. ResourceDefragmentation acts in all three, and its
λ penalty keeps it from HNU's S3 failure mode (filling skew). The one case HNU wins
(S3 node count) it wins by degrading balance — a cost the SUT's penalty term
deliberately avoids.

### S4 (hogs + an unmovable jumbo) — RD frees a node *and* defragments; HNU no-ops
S4 uses the home-manifest shapes: **2 nodes** of cpu-hog `~/cpu-hog.yaml`
(3×600m/24Mi ≈ 0.95 cpu / 0.15 mem), **2 nodes** of mem-hog `~/mem-hog.yaml`
(3×60m/240Mi ≈ 0.14 / 0.95), **1 node** with a single jumbo `~/jumbo-pods.yaml`
(920m/375Mi ≈ 0.51 / 0.52, big & balanced), and **1 node left empty** — 5/6 filled.

- **HNU does nothing at all** (0 evictions). Its log: *"Number of underutilized
  nodes = 1"*, and that one node is the **already-empty** one (no pods to evict).
  The **jumbo node at 0.51/0.52 is above the 40% threshold on both axes**, so HNU
  treats it as adequately utilized and never considers freeing it — even though the
  cluster is badly fragmented (S=3.24). HNU is blind to any consolidation that
  isn't "drain a node that's under the fixed threshold."
- **RD frees a node and defragments.** It empties a mem-hog node (39) by scattering
  its mem pods into the **stranded memory** of the cpu-hog and jumbo nodes:
  cpu node → 0.98/**0.45**, the other cpu node → **0.71/0.71** (perfectly balanced),
  jumbo node → 0.84/0.85 (packed). Result: **N_active 5 → 4**, **S 3.24 → 1.11
  (−66%)**, **H_balanced 13 → 24 (≈+85% more balanced pods fit)**. No Pending pods.

This is the cleanest separation in the whole study: a single jumbo pod lifts its
node over HNU's threshold and HNU stalls completely, while RD — which scores by
*balance improvement*, not per-node utilization — turns a 3.24-stranding layout
into a 1.11 one and reclaims a node. (Earlier variants of S4 used a `small` node
or 3 hogs everywhere; this jumbo variant is the one kept here.)

## Multi-seed passes (n=5 per strategy)

Both discriminating scenarios were repeated 5× per strategy to check the
single-seed numbers aren't placement luck. **Every metric had zero variance across
all seeds** — the cluster + scheduler + descheduler pipeline is fully deterministic
here.

### S4 jumbo (n=5) — driver `run_s4_multiseed.sh`

| S4 jumbo | N_active | S_after | H_balanced | E | Pending |
|---|---|---|---|---|---|
| **RD**  | 5 → 4 (5/5) | 1.1118 ± 0 (from 3.2426) | 24 ± 0 | 5 ± 0 | 0 |
| **HNU** | 5 → 5 (5/5) | 3.2426 ± 0 (unchanged)   | 13 ± 0 | 0 ± 0 | 0 |

The −66% stranding / free-a-node win (RD) vs total no-op (HNU) reproduces on every
seed.

## S3 multi-seed pass (n=5 per strategy)

S3 was the only cell with multiple equally-valid moves, so it was repeated 5×
for each strategy (`results/s3/20260606-*-{rd,hnu}-k{1..5}`, driver
`run_s3_multiseed.sh`). **Both strategies were perfectly deterministic — zero
variance on every metric:**

| S3 (n=5) | N_active | N_empty | S_after | H_balanced | E |
|---|---|---|---|---|---|
| **RD**  | 6 → 5 (5/5) | 1 | 1.4006 ± 0 | 20 ± 0 | 2 ± 0 |
| **HNU** | 6 → 4 (5/5) | 2 | 1.4077 ± 0 | 18 ± 0 | 4 ± 0 |

No seed ever flipped the outcome: the "HNU frees one more node but at higher
stranding (1.4077 vs 1.4006) and lower balanced headroom (18 vs 20)" trade-off is
stable, not a placement artifact. (S1/S2 are structurally deterministic — HNU has
nothing to do; RD's merges are forced.)

### Caveats
- Single environment, requests mode, fixed nodes (O1 = "nodes emptied," not deleted
  — the autoscaler payoff is not measured here).
- HNU used DefaultEvictor defaults (no nodeFit); enabling nodeFit would only make it
  *more* conservative, not change the S1/S2 no-op result.
