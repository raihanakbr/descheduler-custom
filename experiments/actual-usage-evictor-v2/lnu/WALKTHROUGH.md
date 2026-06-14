# Walkthrough: LNU With and Without ActualUsageEvictor

## Goal

Run one L0 comparison and one L1 comparison under sustained API load.

- L0 should rebalance three sources but evict the busy API.
- L1 should produce the same three successful evictions while blocking the API
  and evicting its idle fallback instead.

## Prerequisites

Run from `experiments/actual-usage-evictor-v2`. The scripts default to:

```bash
DESCHEDULER_IMAGE=docker.io/matthewhjt/descheduler-custom:actual-usage-v2
WORKLOAD_IMAGE=docker.io/matthewhjt/workload-http:actual-usage-v1
```

Optionally select six workers explicitly:

```bash
export ACTIVE_WORKERS="worker-1 worker-2 worker-3 worker-4 worker-5 worker-6"
```

The first three workers are underutilized destinations. Workers four and five
are idle overutilized sources. Worker six contains the busy API and its idle
fallback.

The scheduler must use its default LeastAllocated behavior. LNU does not
install scheduler configuration. If the HNU experiment changed the scheduler,
restore it before this experiment:

```bash
./hnu/scripts/restore-scheduler.sh
```

The LNU runner exits before changing workload state when it detects the HNU
`/etc/kubernetes/scheduler-config.yaml` argument.

## Run L0

```bash
./lnu/scripts/run-l0-with-load.sh
```

Expected validation:

```json
{
  "system": "L0",
  "evictions": 3,
  "actual_usage_blocks": 0,
  "active_nodes_after": 6,
  "original_api_remains": false
}
```

Each source retains one Pod, each destination receives one replacement, and
the API replacement lands on a destination.

## Run L1

```bash
./lnu/scripts/run-l1-with-load.sh
```

This command performs a full reset before recreating the same layout.

Expected validation:

```json
{
  "system": "L1",
  "evictions": 3,
  "actual_usage_blocks": 1,
  "active_nodes_after": 6,
  "original_api_remains": true
}
```

The original API remains on worker six. Its idle Guaranteed fallback is
replaced on a destination.

## Why LNU Evicts One Pod Per Source

The source starts near 200m CPU / 450Mi memory including system baseline.
Memory is above the 50% target. After one 50m/200Mi Pod is removed, the source
is near 150m/250Mi and below both target thresholds. LNU then stops naturally.

No eviction annotation, priority class, filler Pod, or per-node eviction limit
is used. QoS only determines which of the two worker-six candidates is
evaluated first.

## Output

Each run creates:

```text
lnu/results/l0-load/<timestamp>/
lnu/results/l1-load/<timestamp>/
```

Important files:

| File | Purpose |
|---|---|
| `layout-validation.json` | Initial LNU source/destination classification |
| `threshold-samples.tsv` | Actual API CPU ratio before descheduling |
| `descheduler.log` | LNU evictions and ActualUsageEvictor decisions |
| `pod-lifecycle.tsv` | Original and replacement API lifecycle |
| `cluster-metrics-before.json` | Initial cluster metrics |
| `cluster-metrics-after.json` | Final cluster metrics |
| `result-validation.json` | L0/L1 acceptance result |
| `summary.json` | HTTP, lifecycle, and cluster summary |

## Calibration

Defaults match HNU:

```text
API_RPS=8
API_CPU_UNITS=900
THRESHOLD=0.80
```

The 50m API request means observed ratios near 10 are expected and consistent
with the existing RDC2 and HNU results.

## Failure Conditions

The runner exits non-zero when:

- the initial layout is not exactly three underutilized destinations and three overutilized sources;
- a candidate cannot fit within destination capacity capped at the 50% target;
- the API does not reach the usage threshold;
- L0 does not evict and replace the API;
- L1 does not block and preserve the API;
- the final layout is not one Pod per source and two Pods per destination.
