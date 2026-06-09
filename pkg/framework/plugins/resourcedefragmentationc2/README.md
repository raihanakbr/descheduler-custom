# ResourceDefragmentationC2

A single-criterion sibling of the `ResourceDefragmentation` plugin: it is the
TOPSIS plugin's **C2 criterion, standalone**, run on **real usage**.

It keeps the same consolidation/defragmentation pipeline (build node state from
actual usage, pick under-utilized/imbalanced drain candidates, drain worst-first,
partial drain, anti-churn bins) but replaces the multi-criteria TOPSIS selector
with a single rule:

> **Evict the pod whose predicted landing node has the best cpu:mem balance** —
> i.e. the pod with the most balance to gain.

The landing node is predicted with the same lightweight `MostAllocated +
BalancedAllocation` bin score the parent plugin uses (`binScore = (density +
balance)/2`), choosing the highest-scoring feasible target. Because that target is
the bin-score argmax, `binScore(target)` is the pod's **best achievable balance**,
which is aligned with the stranding objective `S = Σ|cpuFrac − memFrac|`.

The ablation on the parent plugin showed C2 is the workhorse; this plugin is that
selector on its own, so it tracks the `just-c2` result while reading **real
(metrics-server) usage** by default.

## Scheduler precondition

> Like `HighNodeUtilization`, this plugin only makes sense under a **packing**
> scheduler: it evicts pods expecting the kube-scheduler to re-pack them onto the
> dense, balanced nodes its bin score models. Run it with a `MostAllocated +
> BalancedAllocation` scheduler profile (see
> `experiments/02_06_26/result/scheduler/most-allocated-config.yaml`). Under a
> spreading (`LeastAllocated`) scheduler the evicted pods scatter back and
> consolidation cannot happen.

The bin score approximates that profile with equal cpu/memory and equal
plugin weighting; it does not reproduce the scheduler's exact integer scoring.

## Example config

```yaml
apiVersion: "descheduler/v1alpha2"
kind: "DeschedulerPolicy"
profiles:
  - name: default
    pluginConfig:
      - name: "ResourceDefragmentationC2"
        args:
          namespaces: { include: [defrag-exp] }
          usageMode: actual-ewma        # real usage (needs metrics-server); else falls back to requests
          consolidationThreshold: 0.40
          consolidationTarget: 0.90
          maxEvictions: 50
    plugins:
      balance:
        enabled: ["ResourceDefragmentationC2"]
```

## How it differs from `ResourceDefragmentation`

| | `ResourceDefragmentation` | `ResourceDefragmentationC2` |
|---|---|---|
| Pod selection | 4-criteria TOPSIS | single criterion (C2: best achievable balance) |
| Placement prediction | lightweight `nodeBinScore` | same lightweight bin score |
| Usage signal | requests (configurable) | actual usage (`actual-ewma`) by default |

The `consolidationThreshold` candidacy gate uses the same lightweight `[0,1]` node
score — the descheduler's own "is this a bad bin" trigger.
