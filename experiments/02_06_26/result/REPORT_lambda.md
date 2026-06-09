# ResourceDefragmentation — Isolated-λ Re-run (balancePenaltyWeight)

**Date:** 2026-06-05
**Change under test:** image `keegou/descheduler-custom:alpha-8`, new arg
**`balancePenaltyWeight: 0.7`** (λ). Everything else identical to the original
`REPORT.md` run: `usageMode: requests`, `consolidationThreshold: 0.40`,
**`consolidationTarget: 1`**, `maxEvictions: 50`, namespace `defrag-exp`, same
6-worker cluster, same MostAllocated+BalancedAllocation scheduler.
**Confound control:** λ is the *only* changed variable vs `REPORT.md` (we did
**not** also drop the target to 0.9). Manifest: `descheduler/sut-requests-lambda.yaml`.

## Headline: λ fixes S3's *behavior* without touching S1/S2

| Scenario | Metric | Original (alpha-7, no λ) | λ=0.7 (alpha-8) | Verdict |
|---|---|---|---|---|
| **S1** | N_active / S / E | 6→2 / 0.049→0.050 / 10 (8·2·0) | 6→2 / 0.049→0.050 / 10 (8·2·0) | identical ✅ |
| **S2** | N_active / **S** / H_bal | 6→3 / **3.73→0.97 (−74%)** / 15→29 | 6→3 / **3.73→0.97 (−74%)** / 15→29 | identical ✅ win preserved |
| **S3** | N_active / S | 6→5 / 1.40→1.39 (flat) | 6→5 / 1.40→**1.40** (flat) | scalar same |
| **S3** | **H_skewed** | **4→2 (degraded)** | **4→4 (no harm)** | improved ✅ |
| **S3** | behavior | emptied a **cpu-skewed** node, **manufactured skew** on 2 clean nodes (0.30/0.31 → 0.68/0.33) | emptied an **under-util** node **cleanly**; balanced-full nodes stay balanced (0.85→0.97/0.97) | fixed ✅ |

S3 verified stable across **3 seeds** — all identical (6→5, S 1.395→1.401,
H_skewed 4→4, E=2). The original S3 was the one scenario flagged as
placement-order sensitive; the λ behavior is deterministic here.

## What λ actually changed in S3 (from `desched_pass1.log`)

Drain **order is unchanged** — λ does not touch `computePriorityIndex`, so the
cpu-skewed nodes still sort first (priority 3.16 vs 1.04 for the under-util
nodes). What changed is **target feasibility**: a placement that would skew a
clean node now scores below the bar, so it is rejected:

```
Draining node ip-172-31-19-34 (cpu-skewed, priority 3.16)
  No feasible scheduler target within ceiling; leaving pod in place  (s3-cpu-34 ×2)
Draining node ip-172-31-3-39  (cpu-skewed, priority 3.16)
  No feasible scheduler target within ceiling; leaving pod in place  (s3-cpu-39 ×2)
Draining node ip-172-31-1-245 (under-util, priority 1.04)
  Eviction decision  s3-under-245 → ip-172-31-31-146   (balanced-full, stays balanced)
  Eviction decision  s3-under-245 → ip-172-31-6-144    (balanced-full, stays balanced)
Draining node ip-172-31-16-198 (under-util)
  No feasible scheduler target within ceiling; leaving pod in place  (bins now full)
```

The cpu-heavy pods that *used* to get dumped onto the clean under-util nodes
(creating the self-inflicted skew the original report called out) now find **no
balance-preserving home**, so partial-drain leaves them in place. The only pods
that move are the balanced under-util pods, onto the balanced-full nodes, which
**stay balanced** (0.97/0.97). No clean node is skewed. Final state:
`146`,`144` → 0.97/0.97 (strand ~0), one under-util node emptied, two
cpu-skewed nodes untouched (0.80/0.11), one under-util node left at 0.30/0.31.

## Honest reading: recoverable vs irreducible stranding

The original report framed S3's flat stranding as a *tunable* miss. The re-run
refines that:

- **Recoverable part — fixed.** The self-inflicted skew (pouring cpu pods onto
  clean nodes) was the tunable component. λ=0.7 eliminates it: `H_skewed` no
  longer degrades (4→4 vs the original's 4→2), and no previously-balanced node
  is made skewed.
- **Irreducible part — not tunable, and λ correctly leaves it.** S3's scalar
  stranding (~1.40) is dominated by **two cpu-skewed nodes (0.80/0.11, strand
  0.69 each = 1.38)**. Unlike S2, S3 contains **no complementary memory-heavy
  pods** to pair with them, so no placement can balance those nodes — it is a
  feasibility floor, not an ordering artifact. λ does the right thing by
  refusing to move their pods (any move only spreads the skew). So scalar S
  staying flat is now the *correct* outcome, not a missed optimum.

Net: in S3, λ converts a "consolidate-by-creating-skew" plan into a
"consolidate-cleanly" plan at the same N_active and the same scalar S, while
**S2's genuine −74% stranding win is untouched** because complementary merges
*raise* balance and pass the penalty. λ distinguishes the two cases exactly as
intended.

## Why S2 didn't regress (the key risk)

S2 and S3 pull opposite directions, so the danger was that λ would also block
S2's complementary merge. It did not: placing a mem-skewed pod onto a cpu-skewed
node moves it from 0.11 → ~1.00 mem, **increasing** target balance — a positive
Δbalance — so the penalty term *helps* rather than blocks it. S2 reproduced
bit-for-bit: 6→3, S 3.73→0.97, H_balanced 15→29.

## Caveats / next steps

- **Single environment, λ=0.7 only.** This run shows λ=0.7 is in the "fixes S3,
  keeps S2" band. The sensitivity sweep (λ ∈ {0, 0.3, 0.7, 1.0, …}) that
  motivated parameterizing the knob is still worth running to map where S2
  starts to suffer at high λ.
- **`consolidationTarget` still 1 here** (confound control). Your
  `descheduler-custom.yaml` also lowers it to 0.9; that's a separate, legitimate
  change for realistic burst headroom — run it next as its own variable.
- **Requests mode / pause pods**, as before — actual-usage modes out of scope.
- Raw artifacts: `results/s{1,2,3}/20260605-*/` (3 S3 seeds, 1 each S1/S2).
