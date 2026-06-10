# ActualUsageEvictor v2 Experiment Plan

## Core Claim

> Evicting a currently busy Pod degrades application performance more than
> evicting an idle Pod. ActualUsageEvictor prevents this degradation by
> excluding busy Pods from eviction candidates based on real resource usage.

## Why the Previous Design Was Insufficient

The previous experiment (`experiments/actual-usage-evictor`) had two problems:

1. **Artificial busyness**: the hotspot pod was made busy by a separate k6
   stream (`hotspot.js`) rather than by natural application traffic.

2. **Diluted impact measurement**: foreground traffic was load-balanced across
   all 7 pods via a single Service. Evicting any one pod removed only 1/7 of
   the traffic capacity, making the performance difference between R0 and R1
   nearly invisible.

Evidence from the CPU sustained single-repeat run:

| Metric | R0 | R1 |
|---|---|---|
| Evictions | 1 | 1 |
| Actual usage blocks | 0 | 1 |
| HTTP 503 (after) | 14 | 14 |
| p95 delta ms | +0.099 | +0.918 |

R0 evicted the busy hotspot; R1 blocked the hotspot but evicted the companion.
Both removed one foreground-serving endpoint, so the failure counts were
identical. The experiment proved the mechanism but not the performance benefit.

---

## New Design: Two Pod Types on One Node

### Concept

A source node carries two different types of Pods:

- **api Pod**: serves user-facing HTTP traffic, naturally busy.
- **worker Pod**: serves background/internal traffic, currently idle.

Both Pods live on the same node and have the same resource requests. They are
different Deployments with different Services, so traffic routing is
deterministic and independent of kube-proxy round-robin.

`ResourceDefragmentationC2` selects the api Pod for eviction based on
request-space contribution. Without `ActualUsageEvictor`, the busy api Pod is
evicted and its traffic is disrupted. With `ActualUsageEvictor`, the api Pod
is blocked because its actual CPU usage exceeds the threshold, and C2 falls
back to the idle worker Pod.

This models a realistic production scenario: a node carries both a
high-traffic API service and a low-traffic batch worker. The defragmentation
plugin sees them as equivalent candidates by request-space. The actual usage
filter distinguishes them by runtime cost.

### Layout (6 Workers, S2 Fragmented)

Exactly matches the S2 fragmented/complementary scenario. 6 workers, 2 Pods
per worker, 12 Pods total.

Reference worker: ~2000m CPU / ~811Mi allocatable (including ~100m/50Mi
DaemonSet baseline).

```text
Node          Role            Pods                                        CPU frac  Mem frac
worker-1      cpu-skewed      2 cpu-pods  (750m/20Mi each)  = 1500m/40Mi   ~0.80    ~0.11
worker-2      cpu-skewed      2 cpu-pods  (750m/20Mi each)  = 1500m/40Mi   ~0.80    ~0.11
worker-3      cpu-skewed      2 cpu-pods  (750m/20Mi each)  = 1500m/40Mi   ~0.80    ~0.11
worker-4      mem-skewed      2 mem-pods  (50m/240Mi each)  =  100m/480Mi  ~0.10    ~0.65
worker-5      mem-skewed      2 mem-pods  (50m/240Mi each)  =  100m/480Mi  ~0.10    ~0.65
worker-6      source (mem)    1 api (50m/240Mi) + 1 mem (50m/200Mi)       ~0.10    ~0.60
```

Total workload: 12 Pods.

Worker-6 is the source node (mem-skewed). The api Pod (50m/240Mi) has a larger
memory request than the idle mem Pod (50m/200Mi), so C2 deterministically selects
the api Pod first (bigger memory = bigger stranding relief). Without
ActualUsageEvictor, C2 evicts the busy api Pod. With ActualUsageEvictor, the
api Pod is blocked and C2 falls back to the idle mem Pod.

#### Why C2 Selects the api Pod First

C2's `selectBestPod` (resourcedefragmentationc2.go:228-252) does NOT score pods
by their own request size. Instead, it scores the **binScore at the predicted
landing node** after placement:

```text
binScore = (density + balance) / 2
density  = (cpuFrac + memFrac) / 2
balance  = 1 - |cpuFrac - memFrac|
```

For pods on worker-6 (mem-skewed), C2 predicts placement on a cpu-skewed node
(worker-1/2/3). Each target has ~1600m/90Mi requested (including ~100m/50Mi
daemonset baseline) before placement.

**api pod (50m/240Mi) on worker-1:**
- Post-placement: 1750m/380Mi → cpuFrac=0.875, memFrac=0.469
- balance = 1 - 0.406 = 0.594
- binScore = (0.672 + 0.594) / 2 = **0.633**

**mem pod (50m/200Mi) on worker-1:**
- Post-placement: 1750m/340Mi → cpuFrac=0.875, memFrac=0.419
- balance = 1 - 0.456 = 0.544
- binScore = (0.647 + 0.544) / 2 = **0.596**

**api pod wins (0.633 > 0.596)** because its larger memory request (240Mi vs
200Mi) better balances the cpu-skewed target node. The 40Mi difference creates
a deterministic tie-breaker without relying on labels or heuristics.

The stranding is REDUCIBLE: moving a mem-skewed Pod onto a cpu-skewed node
balances both dimensions. A pure packer can't exploit that; a defragmenter can.

### Pod Types and Services

| Deployment | Replicas | Node(s) | Request | Role | Traffic |
|---|---|---|---|---|---|
| workload-cpu-1 | 2 | worker-1 | 750m/20Mi | compute workload | none (idle) |
| workload-cpu-2 | 2 | worker-2 | 750m/20Mi | compute workload | none (idle) |
| workload-cpu-3 | 2 | worker-3 | 750m/20Mi | compute workload | none (idle) |
| workload-mem-4 | 2 | worker-4 | 50m/240Mi | memory workload | none (idle) |
| workload-mem-5 | 2 | worker-5 | 50m/240Mi | memory workload | none (idle) |
| workload-api | 1 | worker-6 | 50m/240Mi | user-facing HTTP | api-load k6 (busy) |
| workload-mem-6 | 1 | worker-6 | 50m/200Mi | memory workload | none (idle) |

The api Pod (hotspot=true) is one of the mem-skewed pods on worker-6 and receives
all k6 traffic. It becomes naturally busy through normal HTTP request processing.

Services:

```yaml
# User-facing traffic: api pods only
apiVersion: v1
kind: Service
metadata:
  name: workload-api
  namespace: actual-usage-exp
spec:
  type: NodePort
  selector:
    app: workload-api
    experiment: actual-usage-evictor
  ports:
  - name: http
    port: 80
    targetPort: 8080
```

No Service for mem or cpu pods. They are not traffic targets.

### Load Model

One k6 process sends constant-arrival-rate traffic to the api Pod's Service:

```text
k6 api-load:
  target: workload-api Service (NodePort)
  rate: sufficient to push api pod actual CPU usage above 0.80 ratio
  duration: full experiment duration (foreground duration)
```

The api Pod is the only Pod receiving traffic. It becomes naturally busy
through normal HTTP request processing. No separate "hotspot" load stream.

### Expected C2 Behavior

**Without load (purely request-based, no k6 traffic):**

```text
R0 (no plugin):
1. C2 evaluates source node (worker-6, mem-skewed)
2. api pod (50m/240Mi) selected first (larger memory = bigger stranding relief)
3. api pod is evicted
4. Replacement pod is scheduled on a CPU-skewed target node (worker-1/2/3)
5. No traffic disruption (api pod was idle, no k6 load)
6. Stranding improves (defrag success)

R1 (with plugin):
1. C2 evaluates source node (worker-6, mem-skewed)
2. api pod (50m/240Mi) selected first
3. ActualUsageEvictor checks: cpuRatio ~0 (idle, no traffic) → ALLOWED
4. api pod is evicted
5. Replacement pod is scheduled on a CPU-skewed target node (worker-1/2/3)
6. No traffic disruption (api pod was idle, no k6 load)
7. Stranding improves (defrag success)
```

R0 and R1 behave identically without load: both evict the api pod based purely
on request-space, and ActualUsageEvictor does not block because actual usage
is below threshold. This confirms that without load, there is no difference
between R0 and R1.

**R0 (ResourceDefragmentationC2, no ActualUsageEvictor, WITH k6 load):**

```text
1. C2 evaluates source node (worker-6, mem-skewed)
2. api pod (50m/240Mi) selected first (larger memory = bigger stranding relief)
3. api pod is evicted
4. Replacement pod is scheduled on a CPU-skewed target node (worker-1/2/3)
5. During eviction + replacement: api traffic fails (503, latency spike)
6. Stranding improves (defrag success)
```

**R1 (ResourceDefragmentationC2 + ActualUsageEvictor, WITH k6 load):**

```text
1. C2 evaluates source node (worker-6, mem-skewed)
2. api pod (50m/240Mi) selected first
3. ActualUsageEvictor checks: cpuRatio > 0.80 → BLOCKED
4. C2 falls back to mem pod (50m/200Mi)
5. ActualUsageEvictor checks: cpuRatio ~0 (idle) → ALLOWED
6. mem pod is evicted
7. api traffic: unaffected (api pod still running)
8. mem pod replacement: no traffic impact (mem pod has no Service, no load)
9. Stranding: may or may not improve as much as R0
```

### Expected Results

| Metric | R0 (no plugin) | R1 (with plugin) | Why |
|---|---|---|---|
| Evictions | 1 | 1 | both evict one pod from source |
| Actual usage blocks | 0 | 1 | plugin blocks busy api pod |
| API pod evicted | yes | no | plugin protects it |
| API HTTP 503 | high (pod terminates) | 0 (pod stays) | direct traffic to evicted pod |
| API p95 latency | spike | stable | eviction disruption vs none |
| Stranding after | improved | same or less improved | R0 gets better defrag |
| Balanced headroom | improved | same or less improved | tradeoff |

The key difference from the previous design: traffic goes only to the api
Pod, so evicting it causes **100% disruption to its traffic**, not 1/7.

---

## Comparison Systems

| ID | Balance strategy | Pre-eviction filters | Purpose |
|---|---|---|---|
| N0 | none | none | baseline, no eviction |
| R0 | ResourceDefragmentationC2 | DefaultEvictor | control: evicts busy pod |
| R1 | ResourceDefragmentationC2 | DefaultEvictor + ActualUsageEvictor | treatment: blocks busy pod |
| H0 | HighNodeUtilization | DefaultEvictor | negative baseline |
| H1 | HighNodeUtilization | DefaultEvictor + ActualUsageEvictor | negative baseline |

Same as previous experiment. H0/H1 remain expected no-ops because all workers
are above the HNU threshold on at least one axis.

---

## Run Timeline

```text
deploy and place Pods
  -> start api-load k6 (constant rate to api Service)
  -> wait for api load stability (60s)
  -> observe two Metrics API samples: api pod CPU below 0.80 threshold (pre-busy baseline)
  -> api load continues, pod CPU usage naturally exceeds 0.80 threshold
  -> observe two consecutive Metrics API samples above 0.80 threshold
  -> record a 60-second pre-event window
  -> run one descheduler pass (N0 only records event time)
  -> record a 120-second post-event window
```

Note: unlike the previous design, there is no separate "hotspot load" phase.
The api Pod becomes busy from its normal application traffic alone. The k6
rate is calibrated so that api Pod actual CPU usage / CPU request exceeds
0.80 during sustained load.

---

## Metrics

### Primary: Application Performance (api Pod traffic)

- k6 `http_req_duration` p50, p95, p99 (before vs after event)
- HTTP status counts (200 vs 503 vs other)
- Request failure rate
- Successful request rate
- Dropped iterations

### Secondary: Cluster Trade-off (Ian's metrics)

- Stranding `S` before and after
- Active nodes `N_active`
- Balanced headroom `H_balanced`
- Skewed headroom `H_skewed`
- Total evictions

### Evidence: ActualUsageEvictor Behavior

- api Pod actual CPU and memory usage at decision time
- CPU ratio and memory ratio vs threshold
- Blocking log entries from ActualUsageEvictor
- Eviction target pod identity (api vs worker)
- Replacement pod lifecycle timing

---

## Calibration

The api-load k6 rate must be calibrated so that:

1. The api Pod's actual CPU usage / CPU request > 0.80 during sustained load.
2. The api Pod's actual memory usage / memory request < 0.80 (for CPU test).
3. The request rate is realistic and not just a stress test.

Start with:

```text
k6 api-load:
  rate: 15-25 RPS
  cpu_units per request: 200-400
  Expected: api pod CPU ~40-45m actual on 50m request = 80-90% ratio
```

Verify with `kubectl top pod` before running the full experiment.

---

## Threats to Validity

1. **Single node source**: all api traffic goes to one Pod on one node. If
   that node has a network blip, the result is confounded. Mitigation:
   multiple repeats, check node metrics.

2. **No replication**: only 1 api Pod replica. This is intentional for clean
   comparison, but it means the experiment measures "single replica eviction
   impact" not "multi-replica rolling disruption".

3. **Calibration sensitivity**: the k6 rate must precisely push actual usage
   above the 0.80 threshold. Too low and R1 does not block; too high and the
   Pod might hit CPU limits and throttle instead.

4. **CPU limit throttling**: the api Pod has `cpu_limit=1000m` on a `50m`
   request. The k6 load must use actual CPU between 40-45m (80-90% of
   request) without hitting the 1000m limit. This is well within the limit.

5. **Stranding trade-off**: R1 may not improve stranding as much as R0
    because it evicts the idle mem Pod instead of the busy api Pod. This is an
    honest limitation: ActualUsageEvictor trades some defragmentation benefit
    for application protection.

6. **Metrics Server sampling**: actual usage is read from Metrics API which
   samples every ~15s. The descheduler must see a sample above the threshold
   at the exact moment of the eviction decision. The two-consecutive-samples
   pre-check mitigates this.

---

## Implementation Plan

### New Files

```text
experiments/actual-usage-evictor/
  scripts/
    setup-layout.sh          # modified: S2 layout with api + mem on worker-6
  k6/
    api-load.js              # new: constant rate to api Service only
  k8s/
    services.yaml            # modified: api Service only (remove foreground + hotspot)
  policies/
    (reuse existing r0-rdc2.yaml, r1-rdc2-actual.yaml, etc.)
```

### Modified Files

- `run-cell.sh`: replace foreground + hotspot k6 with single api-load k6
- `summarize-run.py`: extract api-load metrics instead of foreground
- `aggregate-results.py`: same structure, different metric keys if needed
- `validate-layout.py`: validate api Pod on source, not hotspot

### Unchanged

- `run-suite.sh`: same flow
- `run-descheduler.sh`: same
- `cleanup.sh`: same
- Policy files: same
- Workload image: same (`workload-http`)
- `format-aggregate.py`: same

---

## Expected Decision Criteria

For the TA defense, the experiment must show:

1. **Mechanism works**: R1 has `actual_usage_blocks >= 1`, R0 has 0.
2. **Eviction target changes**: R0 evicts api Pod, R1 evicts mem Pod.
3. **Application protection**: R0 api HTTP 503 > 0, R1 api HTTP 503 = 0.
4. **Latency protection**: R0 api p95 delta > 0, R1 api p95 delta ~ 0.
5. **Honest trade-off**: R0 stranding improvement >= R1 stranding improvement.

If all five hold across multiple repeats, the claim is supported:

> ActualUsageEvictor prevents eviction of busy Pods and protects application
> performance, at the cost of potentially less cluster defragmentation.
