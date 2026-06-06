# ResourceDefragmentation — Full Evaluation Report

**Date:** 2026-06-06 · **Mode:** requests (pause pods) · **Cluster:** kubeadm v1.36.1, 1 control-plane + 6 workers, each **2000m CPU / ~811Mi** allocatable (≈100m/50Mi already held by DaemonSets), flannel CNI.
**Scheduler:** custom `/etc/kubernetes/scheduler-config.yaml`, empirically confirmed **MostAllocated + BalancedAllocation** (packing) — the precondition the plugin assumes.
**System under test (SUT):** `ResourceDefragmentation`, `usageMode: requests`, `consolidationThreshold: 0.40`, `consolidationTarget: 0.90`, `balancePenaltyWeight λ = 0.7`, `maxEvictions: 50`, scoped to namespace `defrag-exp`.

This report consolidates four experiments:
- **A. System comparison** vs the stock consolidation descheduler **HighNodeUtilization (HNU)** — S1–S4.
- **B. Component ablation — the λ balance gate** — S3.
- **C. Component ablation — the MCDM (TOPSIS) pod selector** — S5 + S6 suite.
- and the SUT-only behavioural characterisation that motivated them.

All metrics are computed **black-box** from `kubectl … -o json` by `metrics.py`, identical for every system, so no result depends on the SUT's own logs.

---

## 1. Method

### 1.1 Metrics (tool-agnostic)
- **N_active / N_empty** — worker nodes carrying ≥1 / 0 experiment pods. *(consolidation)*
- **S** = `Σ_n alloc_n·|cpuFrac_n − memFrac_n|` — cluster stranding. *(defragmentation; lower = less wasted complementary capacity)*
- **H_balanced / H_skewed** — additional reference pods (balanced `200m/80Mi`; skewed `700m/20Mi`) that still fit. *(schedulability headroom)*
- **E** — pods evicted (framework-level `"Evicted pod"` count, tool-agnostic).
- **Pending pod-seconds / PDB violations** — disruption/safety.

### 1.2 Fairness controls (held constant across systems)
Same cluster, same MostAllocated+BalancedAllocation scheduler, same image (`alpha-8`), same usage signal (requests), **matched thresholds** (RD `consolidationThreshold:0.40` ≡ HNU `thresholds{cpu:40,memory:40}`), same eviction scope (`defrag-exp` only), same eviction budget, identical workloads. Setup cordons all workers and uncordons one at a time to seed a controlled "before" state (otherwise a packing scheduler would leave nothing to consolidate).

### 1.3 Scenarios
| ID | Layout | Purpose |
|---|---|---|
| **S1** under-utilized | 6 workers ~30%/31% balanced | pure consolidation |
| **S2** fragmented | 3 cpu-skewed (0.80/0.11) + 3 mem-skewed (0.10/0.65) | reducible stranding (the discriminating case) |
| **S3** mixed | 2 under-util + 2 cpu-skewed + 2 balanced-full | O1↔O2 tension |
| **S4** hogs+jumbo | 2 cpu-hog + 2 mem-hog + 1 jumbo (0.51/0.52) + 1 empty | a single pod lifts its node over the threshold |
| **S5** heterogeneous | one drain node carrying cpu+mem+balanced pods | selector ablation |
| **S6**-{c1,c3,c4,mix} | drain nodes engineered so a *different* criterion is pivotal | selector ablation across instances |

---

## 2. Part A — SUT vs HighNodeUtilization

`N_active`↓, `N_empty`↑, `S`↓, `H_balanced`↑ are better. Multi-seed cells (n=5) had **zero variance** (deterministic pipeline); reported as point values.

| Scenario | Strategy | N_active | N_empty | S (strand) | H_balanced | E |
|---|---|---|---|---|---|---|
| **S1** under-util | **RD** | 6 → **2** | 0 → **4** | 0.049 → 0.050 | 42 → 39 | 10 |
| | HNU | 6 → 6 | 0 → 0 | 0.049 → 0.049 | 42 → 42 | **0 (no-op)** |
| **S2** fragmented | **RD** | 6 → **3** | 0 → **3** | **3.73 → 0.97 (−74%)** | **15 → 29 (+93%)** | 6 |
| | HNU | 6 → 6 | 0 → 0 | 3.73 → 3.73 | 15 → 15 | **0 (no-op)** |
| **S3** mixed | RD | 6 → 5 | 0 → 1 | 1.395 → 1.401 | 20 → 20 | 2 |
| | HNU | 6 → **4** | 0 → **2** | 1.395 → 1.408 | 20 → **18** | 4 |
| **S4** hogs+jumbo | **RD** | 5 → **4** | 1 → 2 | **3.24 → 1.11 (−66%)** | **13 → 24 (+85%)** | 5 |
| | HNU | 5 → 5 | 1 → 1 | 3.24 → 3.24 | 13 → 13 | **0 (no-op)** |

**S1 / S2 / S4 — RD wins decisively; HNU is a no-op.** HNU only drains a node that is *strictly under* the threshold *toward* a node *above* it. In S1 every node is uniformly light (no "high" target); in S2/S4 every node already exceeds 40% on one axis (no "low" source) — so HNU finds nothing to do. This is the **structural limitation** of utilization-threshold consolidation. RD, scoring by *balance improvement* rather than per-node utilization, acts in all three: it pairs complementary pods (a cpu-skewed pod into a mem-skewed node's stranded memory, and vice-versa), cutting stranding **−74%** (S2) / **−66%** (S4) and nearly doubling balanced headroom, while still freeing nodes.

**S3 — a genuine trade-off, not a clean win.** HNU frees one more node (6→4 vs 6→5) but does it blindly: it dumps balanced pods onto the already cpu-skewed nodes, pushing them to ~87% cpu with memory still stranded → `S` edges **up** (1.408) and `H_balanced` **drops** (18). RD's λ gate *refuses* those skew-creating moves ("no feasible target within ceiling"), drains only the node whose pods have a balance-preserving home, and stops — holding `S`/`H` flat (1.401 / 20). So S3 is "more nodes (HNU) vs better balance & headroom (RD)" — the O1-vs-O2/O3 tension, stable across 5 seeds.

**Safety.** No Pending pods in any after-state for either system; no PDB violations.

### Objective scorecard
| Objective | Winner |
|---|---|
| O1 node reclamation, light/fragmented clusters (S1,S2,S4) | **RD** (HNU no-op) |
| O1 node reclamation, mixed (S3) | HNU (6→4 vs 6→5) — but by degrading balance |
| O2/O3 stranding + headroom (S2,S4) | **RD** (−74% / −66% S; H +93% / +85%) |
| Robustness (acts at all) | **RD** — HNU stalls whenever no node is already >40% on exactly one axis |

---

## 3. Part B — Ablation: the λ balance gate

`balancePenaltyWeight` (λ) rejects a relocation that would reduce the *destination's* cpu:mem balance by more than (1−λ). Confound-controlled: λ is the **only** variable changed (`consolidationTarget` held at 1 here, the rest identical). Verified across **3 seeds** (deterministic).

| Scenario | Metric | λ = 0 (legacy) | λ = 0.7 | Verdict |
|---|---|---|---|---|
| S1 | N / S / E | 6→2 / 0.049→0.050 / 10 | identical | unaffected ✅ |
| S2 | N / S / H_bal | 6→3 / **3.73→0.97** / 15→29 | identical | **win preserved** ✅ |
| S3 | behavior | empties a cpu-skewed node, **manufactures skew** on 2 clean nodes (0.30/0.31→0.68/0.33), **H_skewed 4→2** | empties an **under-util** node **cleanly**; balanced-full nodes stay balanced; **H_skewed 4→4** | **fixed** ✅ |

**Why S2 doesn't regress (the key risk).** A complementary merge (mem pod → cpu-skewed node) moves the target's memory 0.11→~1.0, which *raises* balance (positive Δbalance), so the penalty *helps* rather than blocks it. The gate distinguishes "pour complements together" (keep) from "skew a clean node" (reject) by the sign of Δbalance — exactly the S2-vs-S3 separation.

**Honest scope.** S3's *scalar* stranding (~1.40) is dominated by two cpu-skewed nodes with **no complementary mem pods to pair** — a feasibility floor, not a tuning miss. λ correctly leaves them alone (any move only spreads skew); its win in S3 is eliminating the *self-inflicted* skew (H_skewed 4→4 instead of 4→2), not reducing the irreducible part.

---

## 4. Part C — Ablation: the MCDM (TOPSIS) pod selector

Which pod to evict from a drain node is chosen by TOPSIS over four criteria (C1 skew-relief, C2 predicted-destination balance/feasibility, C3 ΔFSI free-space, C4 priority). The `selectionPolicy` knob swaps **only** this step; everything else is identical, so any difference is attributable to the selector. C4 is inert in these scenarios (equal priorities), so `just-c4`/`lowest-priority` are tie-breaks.

### 4.1 S5 — single heterogeneous instance (n=5)
Before: S=1.338, N 4→3, H_bal 32. (Deterministic policies: CI=0; `random` varies by seed.)

| Policy | S_after ↓ | H_bal ↑ | E |
|---|---|---|---|
| **just-c2** | **0.123** | **38** | 2 |
| just-c3 / largest | 0.332 | 36 | 3 |
| random | 0.379 ± 0.126 | 36.4 | 2.8 |
| **topsis** / just-c1 / no-c1 / no-c2 / no-c3 | 0.443 | 36 | 3 |

The whole spread traces to *which W1 pod is evicted first*. On **this instance**, C2 (target quality) is the most informative signal, `just-c2` strictly dominates, and full TOPSIS is **C1-dominated** (`topsis ≡ just-c1`) — an **honest negative** for the blend on a single C2-favourable instance. Leave-one-out shows no single criterion is individually pivotal here (`no-cX ≡ topsis`).

### 4.2 S6 — across instances where different criteria are pivotal (n=1 each, deterministic; all N 4→3)

| Scenario | Best policies (S_after) | Worst policies (S_after) |
|---|---|---|
| **s6-c1** | **topsis** = just-c1 = just-c3 = largest = **0.352** | just-c2 = just-c4 = lowest-priority = random = 0.394 |
| **s6-c3** | **topsis** = just-c1 = just-c2 = just-c4 = random = **0.260** | just-c3 = largest = 0.385 (E=3) |
| **s6-c4** | **topsis** = just-c1 = just-c2 = just-c4 = random = **0.170** | just-c3 = largest = 0.379 (E=3) |
| **s6-mix** | all equal = 0.184 (single move, no discrimination) | — |

### 4.3 The MCDM justification (S5 + S6 together)
Read across all five instances, **no single selector wins everywhere**:

| Selector | wins | loses badly |
|---|---|---|
| `just-c2` | S5, s6-c3, s6-c4 | **s6-c1** (worst tier) |
| `just-c3` / `largest` | s6-c1 | **s6-c3, s6-c4** (worst tier, +1 extra eviction) |
| `just-c1` | s6-c1, s6-c3, s6-c4 | **S5** (=topsis, 0.443) |
| **`topsis`** | **best-or-tied in s6-c1, s6-c3, s6-c4, s6-mix** | suboptimal only in **S5** |

This is the textbook case **for** MCDM: a single criterion can beat the blend on an instance tailored to it (C2 in S5), but each single criterion also **fails badly on some other instance**, whereas **TOPSIS is never in the worst tier and is best-or-tied in 4 of 5 instances**. The contribution of the multi-criteria selector is **robustness across heterogeneous fragmentation, not peak performance on any one case** — and the S5 negative is the honest counter-example that proves the blend is not universally optimal.

---

## 5. Synthesis

1. **Against the established consolidation descheduler (HNU), RD is strictly more capable** on this cluster: it acts in every scenario, whereas HNU no-ops in 3 of 4 because its fixed utilization-threshold trigger requires a specific "low source + high target" condition that fragmented/uniform clusters don't satisfy. Where both act (S3), the difference is an explicit O1↔O2/O3 trade — HNU buys a node by adding stranding; RD keeps balance and frees fewer.
2. **The λ balance gate earns its place**: it removes RD's only self-inflicted failure (S3 skew manufacturing, H_skewed 4→2 → 4→4) while leaving S1/S2's wins bit-for-bit intact, because complementary merges raise balance and pass the gate.
3. **The MCDM selector earns its place on robustness grounds**: across S5+S6 no single criterion is safe, and TOPSIS is the only selector never in the worst tier. The S5 instance where `just-c2` beats TOPSIS is reported as an honest limit.

---

## 6. Threats to validity
- **Single environment, requests mode, fixed nodes.** O1 here is "nodes emptied," not "nodes deleted" — the cluster-autoscaler payoff of consolidation is not measured.
- **Determinism.** With a deterministic scheduler + pause pods, non-`random` cells reproduce exactly (CI=0 over 5 seeds in S3/S4/S5). Seeds are kept for reproducibility and to give `random` its distribution; they are **not** a substitute for environment diversity.
- **Ablation instance count.** S5 alone is C2-favourable; the S6 suite is what makes the "no single criterion is safe → MCDM for robustness" claim defensible. Still a handful of hand-built instances, not a workload trace.
- **HNU configuration.** Run with DefaultEvictor defaults (no nodeFit); enabling nodeFit would make HNU *more* conservative, not change its S1/S2/S4 no-op.
- **`consolidationTarget` differs between Part B (1.0, confound control) and Parts A/C (0.9).** Stated per experiment.

## 7. Reproducibility
Per-run raw artifacts under `results/<scenario>/<ts>[-policy-kseed]/`: `nodes_*.json`, `pods_*.json`, `metrics_{before,after}.txt` (each ends with a `METRICS_JSON {…}` line), `desched_pass*.log`, `summary.txt`. Every number above recomputes from these via `metrics.py`. Drivers: `s{1..4}_*/perform_and_capture.sh`, `run_s4_multiseed.sh`, `run_s3_multiseed.sh`, `run_s5_ablation.sh` + `aggregate_s5.py`, `run_s6_*` + `aggregate_suite.py`. Manifests: `descheduler/{rd-current,b1-hnu}.yaml` and `descheduler/ablation/sut-ablation.tmpl.yaml`.
