# S5 — Selector ablation (does the MCDM pod-selector earn its place?)

**Question.** In `ResourceDefragmentation`, when an under-utilized node is drained,
*which pod do we move first* is decided by a TOPSIS multi-criteria selector over
four criteria (C1–C4). This ablation isolates that selector: one image, one
scenario, **only `selectionPolicy` changes per run**, so any metric difference is
attributable to the selector and nothing else.

* **Image:** `keegou/descheduler-custom:alpha-9` (the customizable build that
  exposes `selectionPolicy` / `selectionSeed`).
* **Fixed args every run:** `usageMode: requests`, `consolidationThreshold: 0.40`,
  `consolidationTarget: 0.90`, `maxEvictions: 50`, scope `namespaces.include:
  [defrag-exp]`.
* **Scenario:** S5 heterogeneous drain node (below). Fixed across all runs — the
  before-state is identical every run (S=1.338, N_active=4, N_empty=2).
* **Seeds:** 5 per policy. Deterministic policies show zero variance (expected,
  see below); `random` varies via `selectionSeed = seed`.
* **Harness:** plain `kubectl` + `metrics.py`, same as S1–S4. One image, N runs.
  Reproduce: `SEEDS=5 ./run_s5_ablation.sh` then `python3 aggregate_s5.py`.

## The scenario (`s5_heterogeneous/`)

6 workers, each 2000m / ~811Mi (≈100m/50Mi already held by daemonsets). Setup
cordons/uncordons to seed a controlled layout; pods carry no affinity so the
descheduler may move them freely.

| Worker | Seed pods | ≈ cpu / mem util | Role |
|---|---|---|---|
| **W1 (drain)** | cpu `1×(400m/20Mi)` + mem `1×(40m/160Mi)` + bal `1×(160m/80Mi)` | 0.35 / 0.38 | **heterogeneous, < threshold → drained** |
| W2 | cpu-skewed `1×(1300m/40Mi)` | 0.70 / 0.11 | spare **mem** (complements mem-pod) |
| W3 | mem-skewed `1×(60m/560Mi)` | 0.08 / 0.75 | spare **cpu** (complements cpu-pod) |
| W4 | balanced `2×(650m/250Mi)` | 0.65 / 0.65 | balanced spare |
| W5, W6 | — | empty | — |

The drain node carries all three pod shapes on the **same** node, so draining it
forces the selector to choose *which pod to evict first*. The receivers have
complementary stranded headroom **within the 0.90 ceiling**, so the first-pick
cascades into different final packings (and thus different S/H). All other nodes
are also imbalanced enough to be drain candidates, but that part of the pipeline
is identical across policies; the **differentiating** decision is the per-pod
ordering on W1.

> An earlier, fuller receiver design (W2 cpu 0.90, W3 mem 0.96, W4 0.85/0.85) was
> rejected during bring-up: every candidate hit *"No feasible scheduler target
> within ceiling"* and **all** policies produced 0 evictions — no signal. Receivers
> were relaxed to leave headroom under the 0.90 ceiling.

## Results (mean ± 95% CI over 5 seeds)

Before (constant): **S=1.338**, N_active=4, N_empty=2, H_bal=32, H_skew=7.

| Policy | S_after ↓ | H_bal ↑ | H_skew ↑ | N_active ↓ | N_empty ↑ | E |
|---|---|---|---|---|---|---|
| **just-c2** | **0.123 ± 0.000** | **38.0** | 8.0 | 3.0 | 3.0 | **2.0** |
| just-c3 | 0.332 ± 0.000 | 36.0 | 8.0 | 3.0 | 3.0 | 3.0 |
| largest | 0.332 ± 0.000 | 36.0 | 8.0 | 3.0 | 3.0 | 3.0 |
| random | 0.379 ± 0.126 | 36.4 ± 0.8 | 8.0 | 3.0 | 3.0 | 2.8 ± 0.4 |
| **topsis** (default) | 0.443 ± 0.000 | 36.0 | 8.0 | 3.0 | 3.0 | 3.0 |
| just-c1 | 0.443 ± 0.000 | 36.0 | 8.0 | 3.0 | 3.0 | 3.0 |
| no-c1 | 0.443 ± 0.000 | 36.0 | 8.0 | 3.0 | 3.0 | 3.0 |
| no-c2 | 0.443 ± 0.000 | 36.0 | 8.0 | 3.0 | 3.0 | 3.0 |
| no-c3 | 0.443 ± 0.000 | 36.0 | 8.0 | 3.0 | 3.0 | 3.0 |

(Rows sorted by S_after. Lower S = less stranding; higher H = more schedulable
headroom. All policies reclaim the same node count here: N_active 4→3, N_empty
2→3.)

## What drives the spread — the mechanism

The three W1 pods score (first evaluation, identical across policies — the
*scenario* fixes the scores; the *policy* only chooses how to rank them):

| W1 pod | C1 | C2 | C3 | C4 |
|---|---|---|---|---|
| mem `(40m/160Mi)` | **+0.00112** | 0.821 | 0.1445 | 0 |
| cpu `(400m/20Mi)` | −0.00113 | 0.802 | 0.1445 | 0 |
| bal `(160m/80Mi)` | +0.00006 | **0.887** | 0.1214 | 0 |

* **C1 → mem-pod first.** TOPSIS, `just-c1`, and all `no-cX` variants all evict
  the **mem-pod** first (highest C1). The mem-pod goes to W4; the cpu-pod is then
  left with *no feasible target under the ceiling* and is stranded back on W1 →
  residual stranding **S=0.443**, E=3.
* **C2 → bal-pod first.** `just-c2` evicts the **bal-pod** first (highest C2),
  which leaves the cpu+mem pods together on W1 as a near-balanced residual
  (0.27/0.28) and never needs a third move → **S=0.123**, E=2, H_bal=38. Best on
  every axis here.
* **C3 / largest → cpu-pod first.** Both key on footprint-like quantities and
  evict the cpu-pod first → **S=0.332**.
* **random** is a coin-flip over the same first-pick: seed 4 happened to pick the
  bal-pod (S=0.123); seeds 1,2,3,5 picked the mem-pod (S=0.443) → mean 0.379 with
  real variance, straddling the deterministic outcomes.

So the selector demonstrably changes the result, and the entire spread traces to
**which W1 pod is evicted first**.

## Findings

1. **The selector is causal and non-trivial.** Holding everything else fixed,
   S_after ranges **0.123 → 0.443** purely by swapping `selectionPolicy`. The
   choice of pod-selection rule materially changes consolidation quality.

2. **TOPSIS ≡ just-c1 ≡ no-c1 ≡ no-c2 ≡ no-c3 (all 0.443) in this scenario.**
   * `topsis == just-c1` ⇒ **C1 dominates the TOPSIS decision** here.
   * `no-c1 == no-c2 == no-c3 == topsis` ⇒ dropping any *single* criterion does
     not change the argmax — i.e. no single criterion is individually *pivotal*
     to the full selector on this scenario (necessity is not demonstrated by the
     leave-one-out variants here).

3. **TOPSIS is not the best selector in this scenario.** `just-c2` strictly
   dominates it (lower S, higher H, fewer evictions). This is an *honest negative*
   result for the MCDM on **this single heterogeneous instance**: C2 (the
   placement/target-quality criterion — note it returns the −999.9 sentinel when a
   pod has no feasible target) is the most informative signal here, and the
   default TOPSIS weighting dilutes it behind C1.

4. **C4 is inert here.** All pods report C4=0 (equal priority), so `just-c4` /
   `lowest-priority` would be arbitrary tie-breaks — correctly excluded from the
   sweep. C2's −999.9 sentinel confirms C2 encodes target feasibility.

## Caveats / honest scope

* This is **one** heterogeneous scenario. A single instance can favor a single
  criterion (here C2) by construction; it does **not** show TOPSIS is bad in
  general, only that on this instance the multi-criteria blend is beaten by
  optimizing C2 alone and is C1-dominated. The proper MCDM-justification claim
  needs this ablation across **several** distinct mixes (e.g. vary which axis is
  scarce, add priority spread to activate C4, vary `consolidationTarget`). The
  harness is parameterized to do exactly that — add scenarios / change the seed
  set and re-run.
* Determinism: for non-`random` policies the before-state, the plugin, and (given
  MostAllocated + BalancedAllocation) the scheduler are all deterministic here, so
  the 5 seeds reproduce identically (CI = 0). The seeds are still worth keeping as
  a reproducibility check and to give `random` its distribution.
* The plugin also drains the imbalanced receiver nodes (W2/W3), not just W1. That
  behavior is **identical across policies**, so it doesn't confound the ablation —
  the differentiating decision is isolated to the W1 first-pick (verified in the
  per-run logs).

## Files

* Scenario: `s5_heterogeneous/{scenario_s5_setup,perform_and_capture,cleanup}.sh`
* Manifest template (alpha-9): `descheduler/ablation/sut-ablation.tmpl.yaml`
  (`__POLICY__` / `__SEED__` substituted per run into `…/generated/`)
* Sweep: `run_s5_ablation.sh` (`POLICIES`, `SEEDS` overridable) · probe:
  `probe_s5.sh`
* Aggregation: `aggregate_s5.py` (prints the table above + `AGG_JSON`)
* Raw per-run data: `results/s5/<ts>-<policy>-k<seed>/`
