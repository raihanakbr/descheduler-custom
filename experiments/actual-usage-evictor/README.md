# ActualUsageEvictor real-cluster experiment

Build images with [`IMAGE_BUILD.md`](IMAGE_BUILD.md). Run the reproducible
experiment with [`WALKTHROUGH.md`](WALKTHROUGH.md).

This experiment evaluates whether `ActualUsageEvictor` reduces application
disruption when a requests-based defragmentation strategy selects a busy HTTP
Pod.

The claim is deliberately narrow:

> Requests-based defragmentation does not account for the dynamic runtime cost
> of eviction. Blocking a currently busy Pod can reduce request latency and
> failures, at the cost of deferring a measurable request-space improvement.

The experiment does not claim to fix request-versus-actual mismatch in the
scheduler. Replacement Pods are still scheduled from resource requests.

## Comparison

Each resource scenario is run in this fixed order:

| ID | Balance strategy | Pre-eviction filters |
|---|---|---|
| `N0` | none | none |
| `R0` | ResourceDefragmentationC2 | DefaultEvictor |
| `R1` | ResourceDefragmentationC2 | DefaultEvictor + ActualUsageEvictor |
| `H0` | HighNodeUtilization | DefaultEvictor |
| `H1` | HighNodeUtilization | DefaultEvictor + ActualUsageEvictor |

`DefaultEvictor` is always present in a real descheduler profile. It applies
general Kubernetes eviction protections. `ActualUsageEvictor` is an additional
AND condition:

```text
eviction allowed =
    DefaultEvictor allows the Pod
    AND ActualUsageEvictor considers the Pod not busy
```

The current defaults are CPU ratio `0.80` and memory ratio `0.90`.

## Controlled layout

The active experiment uses five homogeneous workers in an S2-like complementary
fragmentation layout:

```text
source worker:       1 hotspot Pod    x 100m CPU / 480Mi memory
memory peer:         2 regular Pods   x  50m CPU / 220Mi memory
three target workers:
                     1 CPU-heavy Pod  x 1550m CPU / 40Mi memory each
```

Including the reference cluster's approximately `100m/50Mi` daemonset baseline,
the source and memory peer are memory-skewed while the three targets are
CPU-skewed. The hotspot fits on a CPU-skewed target and makes it more balanced.
The CPU-heavy Pods have no valid relocation target. The source is more
memory-skewed than its peer and contains only the hotspot, making the `R0`
selection deterministic with `maxEvictions: 1`.

The normal Service selects all six HTTP Pods. A separate hotspot Service selects
only the source Pod.

`HighNodeUtilization` is retained as a negative strategy baseline. Every worker
is above its `40%` threshold on either CPU or memory, so `H0` and `H1` are
expected to perform no eviction. They demonstrate that utilization-threshold
consolidation does not act on this complementary fragmentation. The direct
ActualUsageEvictor comparison is `R0` versus `R1`.

## Load model

Two k6 processes run concurrently:

1. **Foreground load** sends a constant request rate to the normal Service. Its
   latency and failure metrics are the application result.
2. **Hotspot load** sends extra work only to the source Pod. It raises either
   CPU usage/request above `0.80` or memory usage/request above `0.90`.

The run timeline is:

```text
deploy and place Pods
  -> start foreground load
  -> wait for foreground stability
  -> observe two Metrics API samples below the busy threshold
  -> start hotspot load
  -> observe two consecutive Metrics API samples above the threshold
  -> record a 60-second pre-event window
  -> run one descheduler pass (N0 only records the event time)
  -> record a 120-second post-event window
```

The workload changes readiness on SIGTERM, waits for endpoint propagation, and
then performs an HTTP graceful shutdown. This tests the plugin on top of normal
Kubernetes shutdown handling rather than relying on an intentionally broken
application.

## Repeats

A repeat is a complete clean run, not another descheduler pass:

```text
reset -> place workload -> start load -> one event -> collect -> cleanup
```

The suite defaults to five repeats per cell because k6 tail latency, Metrics
Server sampling, scheduling, and Pod readiness timing vary between runs.

Example:

```text
cpu/R1/repeat-1
cpu/R1/repeat-2
...
cpu/R1/repeat-5
```

## Metrics

Primary application metrics:

- k6 `http_req_duration` p50, p95, and p99
- request failure rate, HTTP status counts, dropped iterations
- successful request rate
- pre-event versus post-event degradation

Eviction evidence:

- selected, blocked, and evicted Pod
- actual CPU and memory, requests, and usage/request ratios
- descheduler log and Kubernetes events
- old Pod deletion time and replacement Pod Scheduled/Ready time

The lifecycle timing is explanatory rather than a primary success metric. It
shows how long the Deployment operated without the evicted replica.

Cluster trade-off metrics reuse Ian's definitions:

- active and empty workers
- request-space stranding
- balanced and CPU-skewed schedulability headroom
- total evictions and pending Pod-seconds

For `R0`, the expected request-space result is eviction of the busy hotspot,
one fewer active application worker, lower `S`, and higher balanced headroom.
For `R1`, the busy hotspot must be blocked. `ResourceDefragmentationC2` may then
continue to another feasible Pod; that fallback eviction is acceptable only when
the fallback Pod is below the actual-usage thresholds. The primary R1 evidence is
therefore hotspot protection, not necessarily zero total evictions.

## Prerequisites

- `kubectl`, `k6`, `python3`, and a working kubeconfig
- Metrics Server (`metrics.k8s.io/v1beta1`)
- five homogeneous, reference-sized workers near `2000m` CPU and `811Mi`
  allocatable memory each
- the workload image built from
  `experiments/actual-usage-evictor/cmd/workload-http`
- the slim custom descheduler image described in `IMAGE_BUILD.md`

Set the descheduler image and run preflight:

```bash
export DESCHEDULER_IMAGE=<registry>/descheduler-custom:<tag>
scripts/preflight.sh
```

## Running

Run one cell:

```bash
DESCHEDULER_IMAGE=<image> \
WORKLOAD_IMAGE=docker.io/matthewhjt/workload-http:actual-usage-v1 \
scripts/run-cell.sh cpu R1 1
```

Run the fixed-order suite:

```bash
REPEATS=5 scripts/run-suite.sh cpu
REPEATS=5 scripts/run-suite.sh memory
```

Run the idle control by setting `LOAD_PATTERN=idle`. Run the transient check by
setting `LOAD_PATTERN=transient`; the runner stops the hotspot before the event.

Every run writes raw artifacts below:

```text
results/<load-pattern>/<resource>/<system>/repeat-<n>/<timestamp>/
```

After a suite completes, `results/<load-pattern>/<resource>/aggregate.json`
reports the mean, median, and 95% confidence interval for the main per-system
metrics.

Do not compare cells unless `preflight.txt`, `layout-validation.json`, and
`threshold-samples.tsv` show the same worker set, the hotspot as RDC2's first
feasible selection, no HNU source, and a valid threshold state.
