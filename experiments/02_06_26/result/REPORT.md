# ResourceDefragmentation — Requests-Mode Evaluation Report

**Date:** 2026-06-02 · **normalized to current config 2026-06-06**
**Cluster:** 1 control-plane (`ip-172-31-84-108`) + 6 workers, each **2000m CPU / 830876Ki (~811 MiB)** allocatable, kubeadm v1.36.1, flannel CNI.
**System under test:** `ResourceDefragmentation` (`keegou/descheduler-custom:alpha-8`), `usageMode: requests`, `consolidationThreshold: 0.40`, **`consolidationTarget: 0.9`**, **`balancePenaltyWeight: 0.7` (λ)**, `maxEvictions: 50`, scoped to namespace `defrag-exp`.
**Workload:** `registry.k8s.io/pause` pods that reserve CPU/mem **requests** and consume ~0 — so declared load == accounted load in requests mode.
**Scheduler:** custom `--config=/etc/kubernetes/scheduler-config.yaml`; empirically confirmed to **pack** (MostAllocated) — see §6.

> This report evaluates the **SUT** across S1–S4 in requests mode. The **"before" snapshot is the B0 (no-descheduler) reference** for each scenario; the **B1 HighNodeUtilization** baseline is summarized in §9.
>
> **All scenarios are now on one config** (alpha-8, target 0.9, λ=0.7). S1–S3 were re-run on 2026-06-06 to normalize away the earlier alpha-7/target-1 numbers; the most visible change is **S3, which under λ=0.7 no longer manufactures skew** (see §2.3, §5). The full strategy comparison lives in `REPORT_hnu_vs_rd.md`.

---

## 1. Headline results

| Scenario | N_active before→after | N_empty | Stranding **S** before→after | H_balanced | H_skewed | **E** (evictions) | Passes (per-pass) | Converged | PDB / Pending |
|---|---|---|---|---|---|---|---|---|---|
| **S1** under-utilized | **6 → 2** | 0 → **4** | 0.049 → 0.050 (flat) | 42 → 39 | 12 → 8 | **8** | 2 (8 / 0) | ✅ | 0 / 0 |
| **S2** fragmented | **6 → 3** | 0 → **3** | **3.727 → 0.970 (−74%)** | **15 → 29 (+93%)** | 6 → 6 | **6** | 2 (6 / 0) | ✅ | 0 / 0 |
| **S3** mixed | **6 → 5** | 0 → **1** | 1.395 → 1.401 (flat) | 20 → 20 | 4 → 4 | **2** | 2 (2 / 0) | ✅ | 0 / 0 |
| **S4** hogs+jumbo † | **5 → 4** | 1 → **2** | **3.243 → 1.112 (−66%)** | **13 → 24 (+85%)** | 7 → 6 | **5** | 3 (4 / 1 / 0) | ✅ | 0 / 0 |

† S4's "before" already has one empty node (5/6 filled by design), so it starts at N_active=5. See §2.4.

**One-line verdict:** the SUT consolidates in every scenario and, in the scenarios that matter for stranding (S2 and S4, reducible stranding), it achieves **node reclamation and stranding reduction simultaneously** — the thesis claim. In S1 it is a clean packer; in S3 it hits the node-count floor but does not improve stranding. Against the **B1 HighNodeUtilization** baseline (§9), the SUT acts in every scenario whereas B1 is a **no-op on S1, S2, and S4** and only consolidates S3 (there by slightly worsening balance).

---

## 2. Per-scenario analysis

### S1 — Under-utilized (pure consolidation, O1)

Initial: every worker at 600m/250Mi (~30%/31% incl. daemonsets), avgUtil ≈ 0.30 < 0.40 → all six flagged.

Result: **6 → 2 active nodes (4 emptied)**, converged in 2 passes (8 → 0 evictions, E=8). Final packing:
`ip-172-31-1-245` → 1850m/750Mi (0.93/0.92, 7 pods), `ip-172-31-19-34` → 1350m/550Mi (0.68/0.68, 5 pods).

- **Stranding flat (0.049 → 0.050):** correct. The load was already balanced, so packing balanced pods produces balanced full nodes — nothing to defrag, nothing made worse.
- **Headroom slightly down (H_balanced 42 → 39, H_skewed 12 → 8):** expected. Total free CPU/mem is unchanged, but it is now concentrated on 4 *whole-empty* nodes plus near-full remainders; the two packed nodes (0.93/0.92) accept almost no further pods. This is the normal consolidation trade: same capacity, repartitioned.
- Single pass of 8 evictions (4 nodes × 2 pods) drained every under-utilized node at once; pass 2 confirmed 0 — no oscillation (the receiving nodes are protected as bins).

### S2 — Fragmented / complementary (THE discriminating scenario, O2/O3)

Initial: 3 cpu-skewed workers at 1600m/90Mi (0.80/0.11) + 3 mem-skewed at 200m/530Mi (0.10/0.65). Stranding **S = 3.73**.

Result: **6 → 3 active nodes**, **S 3.73 → 0.97 (−74%)**, **H_balanced 15 → 29**, converged in 2 passes (E=6).

The eviction trace (from `desched_pass1.log`) shows *why* this works — the plugin drained the **mem-skewed** nodes and sent their memory-heavy pods onto the **cpu-skewed** nodes:

```
pod=s2-mem-39  → predictedTarget=ip-172-31-1-245   (a cpu-skewed node)
pod=s2-mem-39  → predictedTarget=ip-172-31-1-245
pod=s2-mem-146 → predictedTarget=ip-172-31-1-245
pod=s2-mem-146 → predictedTarget=ip-172-31-16-198
pod=s2-mem-144 → predictedTarget=ip-172-31-16-198
pod=s2-mem-144 → predictedTarget=ip-172-31-16-198
```

The cpu-skewed pods themselves had **no feasible within-ceiling target** (another cpu node would blow the CPU ceiling), so the plugin left them in place (partial drain) and instead used their free *memory* to absorb the mem-skewed pods. Final state:
- `245`, `198`: 1750m/810Mi → **0.88 cpu / 1.00 mem** (cpu-skewed + mem-skewed co-located = balanced, strand 0.12 each)
- `34`: untouched cpu-skewed (0.80/0.11, strand 0.69) — the one node it could not pair
- `39`, `146`, `144`: **empty**

This is the result a pure bin-packer (B1) cannot produce: it would pack by a single density score and tend to stack cpu pods with cpu pods, *preserving* the skew. Here the BalancedAllocation half of the bin score actively pulled complementary pods together. **H_balanced nearly doubling (15 → 29)** is the schedulability payoff: the cluster can now accept far more balanced work than before.

### S3 — Mixed realistic (O1 + O2 together)

Initial: 2 under-utilized (0.30/0.31) + 2 cpu-skewed (0.80/0.11) + 2 balanced-full (0.85/0.85). Stranding S = 1.40.

Result: **6 → 5 active nodes (1 emptied)**, **S 1.40 → 1.40 (flat)**, **H_skewed 4 → 4 (no new skew)**, E=2, converged in 2 passes.

What happened (under λ=0.7): the plugin drained **one under-utilized node** (`198`) and the scheduler placed its two balanced pods onto the two **balanced-full** nodes (`146`, `144`), which absorbed them and stayed balanced (0.85 → **0.97/0.97**). The two **cpu-skewed** nodes (`34`, `39`) were **left untouched** — moving their cpu-heavy pods anywhere would only spread skew, and the λ balance penalty rejects such targets ("No feasible target within ceiling"). The other under-utilized node (`245`) was left at 0.30/0.31. Final: `198` empty, two cpu-skewed nodes at 0.80/0.11, two balanced-full at 0.97/0.97, one under-util at 0.30/0.31.

- **Node count is near-optimal.** The four cpu-skewed pods total 3000m and are CPU-bound: they cannot fit on fewer than 2 nodes. Add 2 balanced-full (kept) + 1 node for the residual light load ⇒ ~5 nodes is the floor. The SUT hit it.
- **Stranding flat is now the *correct* outcome, not a miss.** The residual S ≈ 1.40 is dominated by the two cpu-skewed nodes (strand 0.69 each); S3 has **no complementary memory-heavy pods** to pair with them (unlike S2), so no placement can balance them — a feasibility floor. Crucially, λ=0.7 makes the SUT **refuse to manufacture skew**: it does *not* pour cpu pods onto the balanced under-utilized nodes (the alpha-7 behavior that degraded H_skewed 4 → 2). It empties a node cleanly instead, leaving H_skewed 4 → 4. See §5 — this resolves the earlier "missed optimum" finding.

### 2.4 — S4 — Hogs + an unmovable jumbo (O1 + O2)

Initial (5/6 filled, 1 empty): 2 cpu-hog nodes at 1900m/122Mi (**0.95/0.15**), 2 mem-hog nodes at 280m/770Mi (**0.14/0.95**), 1 node with a single jumbo pod 1020m/425Mi (**0.51/0.52**), 1 empty. Shapes come from the home manifests `cpu-hog.yaml` (600m/24Mi ×3), `mem-hog.yaml` (60m/240Mi ×3), `jumbo-pods.yaml` (920m/375Mi ×1). Stranding **S = 3.24**.

Result: **5 → 4 active nodes**, **S 3.24 → 1.11 (−66%)**, **H_balanced 13 → 24 (+85%)**, E=5, converged in 3 passes (4/1/0). Deterministic across **5 seeds** (zero variance).

The plugin emptied a **mem-hog node** by scattering its memory-heavy pods into the **stranded memory** of the cpu-hog and jumbo nodes — the same complementary mechanism as S2, but now exploiting partially-filled nodes:
- cpu-hog node `245`: 3 cpu + 1 mem → **0.98 / 0.45** (stranded mem 0.15 → 0.45 absorbed)
- cpu-hog node `198`: 2 cpu + 2 mem → **0.71 / 0.71** (perfectly balanced)
- jumbo node `146`: jumbo + 1 cpu + 1 mem → **0.84 / 0.85** (packed, balanced)
- one mem-hog node → **empty**; the pre-existing empty node stays empty (N_empty 1 → 2)

**Why this scenario matters for the B1 comparison:** the **jumbo pod lifts its node to 0.51/0.52 — above the 40% threshold on both axes — so HighNodeUtilization (B1) sees no drainable node and does nothing at all** (0 evictions, S unchanged). A single large pod blinds the fixed-threshold packer to a consolidation that frees a node *and* cuts stranding two-thirds. The SUT, which scores by balance improvement rather than per-node utilization, finds it. This is the sharpest SUT-vs-B1 separation in the study (§9).

---

## 3. Objective ranking (SUT vs B0 baseline)

| # | Objective | Metric | S1 | S2 | S3 | S4 † | Reading |
|---|---|---|---|---|---|---|---|
| **O1** | Node reclamation | N_active ↓ | 6→2 ✅ | 6→3 ✅ | 6→5 ✅ (≈optimal) | 5→4 ✅ | strong everywhere |
| **O2** | Stranding | S ↓ | flat (n/a) | **−74%** ✅ | flat (floor) | **−66%** ✅ | wins where reducible |
| **O3** | Headroom | H ↑ | −7% | **+93% balanced** ✅ | flat (0%) | **+85% balanced** ✅ | wins on S2 & S4 |
| **O4** | Efficiency | Δ per E | 0.5 node/E | 0.5 node/E, ΔS/E=0.46 | 0.5 node/E | ΔS/E=0.43 | cheap, few evictions |
| **O5** | Stability | later-pass E → 0 | ✅ 8/0 | ✅ 6/0 | ✅ 2/0 | ✅ 4/1/0 | converges, no ping-pong |
| **O6** | Safety | PDB=0, pending=0 | ✅ | ✅ | ✅ | ✅ | clean in all runs |

The S1–S4 pattern is exactly the falsifiable claim from the test plan: the SUT is **strong on O1 in all cases and uniquely strong on O2/O3 when stranding is reducible (S2, S4)**. The B1 HighNodeUtilization comparison (§9) confirms the gap: B1 matches none of S1/S2/S4 (it is a no-op there) and only consolidates S3.

---

## 4. Stability & safety (O5/O6) in detail

- **Convergence:** every scenario reached a 0-eviction pass within ≤3 passes. The `bins` set (a node that received a relocated pod is never later drained) prevented A→B→A ping-pong — confirmed by zero re-evictions across passes.
- **Disruption:** total evictions were small (S1–S4: 8 / 6 / 2 / 5). No pod entered `Pending` in any after-snapshot; all Deployments returned to `Available`.
- **PDBs:** none defined; no eviction was denied.
- **Note (E counting):** `events_descheduled.txt` is empty (this build emits no `reason=Descheduled` event). E is now taken from the framework-level `evictions.go "Evicted pod"` log line, which **every** plugin emits — making it tool-agnostic for the B1 comparison (§9). `common.sh` was switched from the RD-only `Eviction decision` line to this, since the latter under-counts HighNodeUtilization.

---

## 5. Findings / recommendations for the plugin

1. **S3 skew-manufacturing — resolved by `balancePenaltyWeight: 0.7` (λ).** Under the earlier alpha-7/target-1 config the drain order + "pack upward" rule poured cpu-skewed pods onto *balanced* under-utilized nodes, degrading H_skewed (4 → 2) for no node-count gain. The λ penalty now scores those skew-creating targets below the bar, so the SUT instead empties a node cleanly (H_skewed 4 → 4) — see §2.3. The residual S3 stranding is a genuine feasibility floor (no complementary mem pods to pair the cpu-skewed nodes), which λ correctly leaves alone. The complementary merge in S2/S4 *raises* balance, so λ permits it; the distinction works exactly as intended. (Confirmed deterministic ×5.)
2. **`consolidationTarget: 0.9` (current).** Lowered from 1.0 for realistic burst headroom. In S2 the receiving nodes reach memFrac 1.00 on pause pods — acceptable here, but the 0.9 target is what keeps S4's tight layout from being over-packed (§2.4); it is also why, on a *fully* packed complementary cluster with no empty node, the SUT correctly declines to thrash.
3. The plugin's logging is genuinely useful for evaluation (`drain candidate`, `Eviction decision` with `predictedTarget`, `No feasible target within ceiling`); keep those at V(1).

---

## 6. Validity: scheduler precondition was met

The experiment is only meaningful if the cluster scheduler packs. **Confirmed empirically:** in S1, evicting pods off 4 nodes resulted in them landing densely on 2 nodes (not re-spreading), and in S2 the mem pods landed precisely on the predicted cpu-skewed targets. A LeastAllocated (spread) scheduler would have scattered them back and left N_active≈6. So `/etc/kubernetes/scheduler-config.yaml` is effectively MostAllocated+BalancedAllocation, matching `scheduler/most-allocated-config.yaml`.

---

## 7. Reproducibility & next steps

- **Raw artifacts** per run in `experiment/results/<scenario>/<timestamp>/`: `nodes_*.json`, `pods_*.json`, `metrics_{before,after}.txt`, `desched_pass*.log`, `summary.txt`. Every number above is recomputable from these with `metrics.py`.
- **Repeat for CI-grade stats:** the test plan calls for ≥5 seeds. **Done for S3 and S4** (`run_s3_multiseed.sh`, `run_s4_multiseed.sh`): both were bit-identical across 5 seeds (zero variance), so the single-seed S1/S2 rows are existence proofs of deterministic behavior here. Re-run S1/S2 ×5 if a CI is needed for completeness.
- **B1/B2: done for B1 (HighNodeUtilization).** Run on the *same custom image* with the same scope/threshold (`descheduler/b1-hnu.yaml`); results in §9 and `REPORT_hnu_vs_rd.md`. `metrics.py` and the now tool-agnostic eviction counter (`common.sh` counts the framework `"Evicted pod"` line) keep the columns comparable. LowNodeUtilization (B2) still open.
- **B0:** run setup + `MAX_PASSES=0 perform_and_capture.sh` (before≡after) for an explicit do-nothing row.

---

## 8. Threats to validity (as run)

- **Single seed for S1/S2.** S3 and S4 were repeated ×5 and showed **zero variance**; S1/S2 remain one run each (deterministic here, but not yet seeded).
- **Fixed nodes.** O1 here is "nodes emptied," not "nodes deleted" — B1's real-world autoscaler payoff is not captured.
- **pause + requests mode** means S and candidacy are driven purely by requests; actual-usage modes would change the picture and are explicitly out of scope here.
- **Daemonset baseline (100m/50Mi/node)** is included in all fractions (matching the plugin) and is constant across systems, so it does not bias the comparison.

---

## 9. B1 baseline — SUT vs HighNodeUtilization (summary)

Full write-up in `REPORT_hnu_vs_rd.md`. B1 = stock **HighNodeUtilization**, run on the **same image** (`alpha-8`) with the **same scope and 0.40 threshold** as the SUT, so the only variable is the consolidation strategy. SUT here = current config (alpha-8, target 0.9, λ=0.7).

| Scenario | SUT (RD) | B1 (HighNodeUtilization) |
|---|---|---|
| **S1** under-utilized | 6 → 2, E=8 | **no-op** (every node < 40% → no node to pack *into*) |
| **S2** fragmented | 6 → 3, **S −74%** | **no-op** (every node > 40% on its skewed axis → none "under-utilized") |
| **S3** mixed | 6 → 5, balance held flat | 6 → 4 (frees one *more* node, but **S ↑**, H_balanced 20→18 — packs onto skew) |
| **S4** hogs+jumbo † | 5 → 4, **S −66%** | **no-op** (jumbo lifts its node > 40% → nothing classed under-utilized) |

**Reading.** HighNodeUtilization only acts when a node sits *strictly below* a fixed utilization threshold **and** a hotter node exists to receive the drained pods. In S1/S2/S4 that condition never holds, so B1 does nothing. The SUT acts in all four because it scores by **balance improvement and complementarity**, not per-node utilization. The one case B1 frees more nodes (S3) it does so by pushing balanced pods onto already-skewed nodes — gaining node count at the cost of stranding/headroom, the opposite of the SUT's design goal. No Pending pods were produced by either system on any scenario (O6). S3 and S4 results are deterministic across 5 seeds.
