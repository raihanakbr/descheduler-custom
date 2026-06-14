# LNU ActualUsageEvictor Experiment Plan

## Core Claim

> ActualUsageEvictor changes which Pod LowNodeUtilization evicts from a busy
> source. Without the filter, LNU evicts the busy API Pod. With the filter, it
> protects the API and evicts an idle fallback while preserving the same
> request-based rebalancing result.

## Comparison

| ID | Balance strategy | Pre-eviction filters | Expected result |
|---|---|---|---|
| L0 | LowNodeUtilization | DefaultEvictor | 3 evictions; busy API replaced |
| L1 | LowNodeUtilization | DefaultEvictor + ActualUsageEvictor | 3 evictions; API blocked and idle fallback replaced |

Only the API Pod receives load. Every other experiment Pod remains idle.

## Initial Placement

The experiment uses six reference workers with approximately 2000m CPU and
811Mi allocatable per worker, including an approximate 100m CPU / 50Mi
DaemonSet baseline.

LNU uses request-based thresholds of 20% CPU/memory and target thresholds of
50% CPU/memory. A destination must be below both thresholds. A source is above
target when either dimension exceeds its target.

| Worker | Role | Experiment Pods | Approx. total requests | LNU classification |
|---|---|---|---|---|
| worker-1 | destination | 1 x 50m/50Mi | 150m/100Mi | underutilized |
| worker-2 | destination | 1 x 50m/50Mi | 150m/100Mi | underutilized |
| worker-3 | destination | 1 x 50m/50Mi | 150m/100Mi | underutilized |
| worker-4 | idle source A | 2 x 50m/200Mi | 200m/450Mi | overutilized: memory > 50% |
| worker-5 | idle source B | 2 x 50m/200Mi | 200m/450Mi | overutilized: memory > 50% |
| worker-6 | busy source | API 50m/200Mi + idle fallback 50m/200Mi | 200m/450Mi | overutilized: memory > 50% |

There are nine experiment Pods. Removing one 200Mi Pod leaves a source near
150m/250Mi, below the 50% target on both dimensions. LNU therefore stops after
one successful eviction from each source without an eviction limit.

The API is Burstable, matching the API in the RDC2 and HNU experiments. The
idle fallback has requests equal to limits and is Guaranteed. LNU orders equal
priority Pods by QoS, so it evaluates the Burstable API before the Guaranteed
fallback. This makes the target deterministic:

- L0 evicts the API.
- L1 blocks the API and continues to the idle fallback.

## Load Model

The runner reuses the parent `k6/api-load.js` with the same defaults as HNU:

```text
API_RPS=8
API_CPU_UNITS=900
CPU request=50m
ActualUsageEvictor threshold=0.80
```

Observed CPU is expected near 500m, so the CPU usage/request ratio may be near
10. The experiment intentionally preserves the treatment used by RDC2 and HNU.

## Expected L0 Behavior

```text
1. LNU classifies worker-1..3 as underutilized destinations.
2. LNU classifies worker-4..6 as overutilized sources.
3. It evicts one idle Pod from worker-4 and one from worker-5.
4. It evaluates and evicts the Burstable API first on worker-6.
5. Each source falls below the 50% target after one eviction.
6. The default LeastAllocated scheduler spreads replacements to worker-1..3.
7. The API eviction produces HTTP failures and a latency spike.
```

## Expected L1 Behavior

```text
1. LNU evicts one idle Pod from worker-4 and worker-5.
2. LNU evaluates the API first on worker-6.
3. ActualUsageEvictor observes CPU ratio >= 0.80 and blocks eviction.
4. LNU evaluates and evicts the Guaranteed idle fallback.
5. Each source still falls below the 50% target after one successful eviction.
6. LeastAllocated spreads all replacements to worker-1..3.
7. The original API remains available on worker-6.
```

## Expected Trade-off

| Metric | L0 | L1 |
|---|---:|---:|
| Successful evictions | 3 | 3 |
| Actual usage blocks | 0 | >= 1 |
| Original API remains | no | yes |
| Active nodes after | 6 | 6 |
| API HTTP failures | > 0 | approximately 0 |
| Rebalancing result | balanced | balanced |

Unlike HNU, the treatment does not reduce the eviction count. It changes the
selected Pod while preserving LNU's spreading objective, matching the core
RDC2 comparison.

## Execution

```bash
./lnu/scripts/run-l0-with-load.sh
./lnu/scripts/run-l1-with-load.sh
```

The runners require default LeastAllocated behavior. Their first step checks
the active scheduler. If the HNU MostAllocated config is still active, the LNU
setup automatically restores the original static Pod manifest through
`hnu/scripts/restore-scheduler.sh` and waits for scheduler readiness. An
unrecognized custom scheduler config is not overwritten automatically.

## Acceptance Criteria

1. Initial validation finds exactly three underutilized destinations and three overutilized sources.
2. Each source has two experiment Pods and falls below target after one candidate is removed.
3. The singleton API reaches two consecutive CPU ratio samples >= 0.80.
4. L0 produces three evictions, no blocks, and replaces the API on a destination.
5. L1 produces three evictions, at least one block, and preserves the API UID on worker-6.
6. Every destination finishes with two experiment Pods and every source with one.
7. L0 has greater application disruption than L1.

## Threats to Validity

1. Final spreading assumes the default scheduler uses LeastAllocated scoring.
2. LNU Pod ordering depends on the documented priority/QoS ordering; the API
   is Burstable and the fallback is Guaranteed to make the comparison deterministic.
3. A singleton API intentionally amplifies eviction impact.
4. Metrics Server sampling can miss short usage changes, so two consecutive
   above-threshold samples are required.
