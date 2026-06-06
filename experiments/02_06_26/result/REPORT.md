# ResourceDefragmentation — Requests-Mode Evaluation Report

**Date:** 2026-06-02
**Cluster:** 1 control-plane (`ip-172-31-84-108`) + 6 workers, each **2000m CPU / 830876Ki (~811 MiB)** allocatable, kubeadm v1.36.1, flannel CNI.
**System under test:** `ResourceDefragmentation`, `usageMode: requests`, `consolidationThreshold: 0.40`, `consolidationTarget: 1`, `maxEvictions: 50`, scoped to namespace `defrag-exp`.
**Workload:** `registry.k8s.io/pause` pods that reserve CPU/mem **requests** and consume ~0 — so declared load == accounted load in requests mode.
**Scheduler:** custom `--config=/etc/kubernetes/scheduler-config.yaml`; empirically confirmed to **pack** (MostAllocated) — see §6.

> This run evaluates the **SUT only** across S1–S3 (the requests-mode test the user asked for). The **"before" snapshot is the B0 (no-descheduler) reference** for each scenario. B1/B2 were not run; the harness is ready for them via `SUT_MANIFEST=<other>.yaml` (§7).

---

## 1. Headline results

| Scenario | N_active before→after | N_empty | Stranding **S** before→after | H_balanced | H_skewed | **E** (evictions) | Passes (per-pass) | Converged | PDB / Pending |
|---|---|---|---|---|---|---|---|---|---|
| **S1** under-utilized | **6 → 2** | 0 → **4** | 0.049 → 0.050 (flat) | 42 → 39 | 12 → 8 | **10** | 3 (8 / 2 / 0) | ✅ | 0 / 0 |
| **S2** fragmented | **6 → 3** | 0 → **3** | **3.727 → 0.970 (−74%)** | **15 → 29 (+93%)** | 6 → 6 | **6** | 2 (6 / 0) | ✅ | 0 / 0 |
| **S3** mixed | **6 → 5** | 0 → **1** | 1.395 → 1.386 (flat) | 20 → 19 | 4 → 2 | **2** | 2 (2 / 0) | ✅ | 0 / 0 |

**One-line verdict:** the SUT consolidates in every scenario and, in the scenario that matters (S2, reducible stranding), it achieves **node reclamation and stranding reduction simultaneously** — the thesis claim. In S1 it is a clean packer; in S3 it hits the node-count floor but does not improve stranding.

---

## 2. Per-scenario analysis

### S1 — Under-utilized (pure consolidation, O1)

Initial: every worker at 600m/250Mi (~30%/31% incl. daemonsets), avgUtil ≈ 0.30 < 0.40 → all six flagged.

Result: **6 → 2 active nodes (4 emptied)**, converged in 3 passes (8 → 2 → 0 evictions, E=10). Final packing:
`ip-172-31-3-39` → 1850m/750Mi (0.93/0.92, 7 pods), `ip-172-31-31-146` → 1350m/550Mi (0.68/0.68, 5 pods).

- **Stranding flat (0.049 → 0.050):** correct. The load was already balanced, so packing balanced pods produces balanced full nodes — nothing to defrag, nothing made worse.
- **Headroom slightly down (H_balanced 42 → 39, H_skewed 12 → 8):** expected. Total free CPU/mem is unchanged, but it is now concentrated on 4 *whole-empty* nodes plus near-full remainders; the two packed nodes (0.93/0.92) accept almost no further pods. This is the normal consolidation trade: same capacity, repartitioned.
- Pass 2's two extra evictions are mop-up of the last under-utilized node, **not** oscillation (the receiving nodes are protected as bins).

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

Result: **6 → 5 active nodes (1 emptied)**, **S 1.40 → 1.39 (flat)**, E=2, converged in 2 passes.

What happened: the plugin emptied one cpu-skewed node (`34`) by relocating its two cpu pods — one each onto the two under-utilized nodes (`245`, `198`), which went 0.30/0.31 → 0.68/0.33. The two balanced-full nodes were correctly **kept** (avgUtil 0.85 ≥ 0.40, high bin score). The other cpu-skewed node (`39`) could not be drained — no feasible target had room for a 750m pod under the ceiling.

- **Node count is near-optimal.** The four cpu-skewed pods total 3000m and are CPU-bound: they cannot fit on fewer than 2 nodes. Add 2 balanced-full (kept) + 1 node for the consolidated light load ⇒ ~5 nodes is the floor. The SUT hit it.
- **Stranding did not improve (honest negative).** It chose to pour cpu-skewed pods onto the *balanced* under-utilized nodes (creating skew there, strand 0.35 each) instead of merging the two under-utilized nodes together (which would have emptied a node with **no** added skew). Both paths give N_active=5, but the chosen one leaves stranding unchanged and H_skewed slightly worse (4 → 2). This is a greedy-ordering artifact of TOPSIS + "pack upward," not a feasibility limit — see §5.

---

## 3. Objective ranking (SUT vs B0 baseline)

| # | Objective | Metric | S1 | S2 | S3 | Reading |
|---|---|---|---|---|---|---|
| **O1** | Node reclamation | N_active ↓ | 6→2 ✅ | 6→3 ✅ | 6→5 ✅ (≈optimal) | strong everywhere |
| **O2** | Stranding | S ↓ | flat (n/a) | **−74%** ✅ | flat ✗ | wins where reducible |
| **O3** | Headroom | H ↑ | −7% | **+93% balanced** ✅ | −5% | wins on S2 |
| **O4** | Efficiency | Δ per E | 0.4 node/E | 0.5 node/E, ΔS/E=0.46 | 0.5 node/E | cheap, few evictions |
| **O5** | Stability | later-pass E → 0 | ✅ 8/2/0 | ✅ 6/0 | ✅ 2/0 | converges, no ping-pong |
| **O6** | Safety | PDB=0, pending=0 | ✅ | ✅ | ✅ | clean in all runs |

The S1/S2/S3 pattern is exactly the falsifiable claim from the test plan: the SUT is **strong on O1 in all cases and uniquely strong on O2/O3 when stranding is reducible (S2)**. A pure consolidator (B1) would be expected to match O1 on S1/S3 but lose on S2's O2/O3; running B1/B2 next would confirm the gap.

---

## 4. Stability & safety (O5/O6) in detail

- **Convergence:** every scenario reached a 0-eviction pass within ≤3 passes. The `bins` set (a node that received a relocated pod is never later drained) prevented A→B→A ping-pong — confirmed by zero re-evictions across passes.
- **Disruption:** total evictions were small (10 / 6 / 2). No pod entered `Pending` in any after-snapshot; all Deployments returned to `Available`.
- **PDBs:** none defined; no eviction was denied.
- **Note:** the `events_descheduled.txt` cross-check came back empty — this descheduler build does not emit a `reason=Descheduled` event, so **E is taken from the plugin's own `Eviction decision` log lines** (which match the observed before/after pod movements exactly). If you want a fully tool-agnostic E for the B1/B2 comparison, count pod-deletion events or scrape each tool's eviction log uniformly.

---

## 5. Findings / recommendations for the plugin

1. **S3 stranding is a missed optimum, not a hard limit.** The drain-priority order (`pr = 0.5·|RII| + 0.5·(1/FSI)`) and "pack upward" rule led it to fill *balanced* nodes with skewed pods. Consider: when two equally-good targets exist by bin score, break ties toward the node whose post-placement balance is highest (it already scores this in `predictSchedulerTarget`, but the *source* selection / priority does not consider that emptying a balanced under-utilized node is cheaper than skewing it). Worth a targeted unit test with the S3 layout.
2. **`consolidationTarget: 1` was reached** (node `245` hit memFrac 1.00 in S2). That is fine for requests-mode pause pods, but with real pods a 100% request ceiling leaves no burst room — lower to ~0.9 for realistic workloads.
3. The plugin's logging is genuinely useful for evaluation (`drain candidate`, `Eviction decision` with `predictedTarget`); keep those at V(1).

---

## 6. Validity: scheduler precondition was met

The experiment is only meaningful if the cluster scheduler packs. **Confirmed empirically:** in S1, evicting pods off 4 nodes resulted in them landing densely on 2 nodes (not re-spreading), and in S2 the mem pods landed precisely on the predicted cpu-skewed targets. A LeastAllocated (spread) scheduler would have scattered them back and left N_active≈6. So `/etc/kubernetes/scheduler-config.yaml` is effectively MostAllocated+BalancedAllocation, matching `scheduler/most-allocated-config.yaml`.

---

## 7. Reproducibility & next steps

- **Raw artifacts** per run in `experiment/results/<scenario>/<timestamp>/`: `nodes_*.json`, `pods_*.json`, `metrics_{before,after}.txt`, `desched_pass*.log`, `summary.txt`. Every number above is recomputable from these with `metrics.py`.
- **Repeat for CI-grade stats:** the test plan calls for ≥5 seeds; re-run `perform_and_capture.sh` 5× per scenario and report mean ± 95% CI (placement order varies even though the workload is deterministic).
- **Add B1/B2:** point the runner at an upstream-descheduler manifest (HighNodeUtilization / LowNodeUtilization, namespace-scoped to `defrag-exp`, threshold 0.40) — `SUT_MANIFEST=/path/b1.yaml ./s2_fragmented/perform_and_capture.sh`. `metrics.py` is tool-agnostic, so the S/N/H columns stay directly comparable. The decisive plot is **ΔS vs E on S2**.
- **B0:** run setup + `MAX_PASSES=0 perform_and_capture.sh` (before≡after) for an explicit do-nothing row.

---

## 8. Threats to validity (as run)

- **Single seed.** These are one run each; treat as existence proofs of behavior, not statistics. S3's stranding result in particular may vary with placement order.
- **Fixed nodes.** O1 here is "nodes emptied," not "nodes deleted" — B1's real-world autoscaler payoff is not captured.
- **pause + requests mode** means S and candidacy are driven purely by requests; actual-usage modes would change the picture and are explicitly out of scope here.
- **Daemonset baseline (100m/50Mi/node)** is included in all fractions (matching the plugin) and is constant across systems, so it does not bias the comparison.
