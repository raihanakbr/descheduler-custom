# Actual Usage Evictor Evaluation Metrics

## Common Application Metrics

The same application metrics are used for every descheduling strategy.
Availability is evaluated primarily with HTTP failure rate and service
disruption duration. Latency is a secondary metric.

| Metric | Data | Definition |
|---|---|---|
| HTTP Failure Rate | Percentage (%) | Percentage of HTTP requests that fail during the post-event observation window. Lower is better. |
| Service Disruption Duration | Duration (s) | Time from the first failed request to the last failed request following the descheduler event. Zero means no observed outage. Lower is better. |
| Successful-Request Tail Latency | p95 (ms) | Response time below which 95% of successful HTTP requests complete. Failed responses are excluded. Lower is better. |
| Eviction Outcome | Pod identity, eviction count, and block count | Identifies which Pod was evicted or protected and summarizes successful evictions and ActualUsageEvictor blocks. |

### Why Failure and Disruption Are Primary

An unavailable service can reject requests with HTTP 503 faster than it serves
successful requests. If successful and failed responses are combined, p95 or
p99 latency can therefore decrease during an outage. A lower raw latency
percentile would incorrectly suggest an improvement even though availability
became worse.

For this reason:

1. HTTP failure rate measures how much traffic was not served successfully.
2. Service disruption duration measures how long the observed outage lasted.
3. Latency is calculated only from successful requests and is interpreted as a
   secondary performance metric.

### Why p99 Is Diagnostic Only

p99 is retained in raw output for diagnosis, but it is not used as a headline
evaluation metric. These experiments use one run and a short event window at
approximately 8 requests per second. The 99th percentile is consequently
determined by only a few extreme observations and is highly sensitive to one
slow request, connection reuse, scheduler timing, or other transient noise.

p95 uses more observations and is more stable, while failure rate and
disruption duration directly represent eviction-induced availability loss.

## Strategy-Specific Metrics

| Strategy | Metric | Data | Definition |
|---|---|---|---|
| ResourceDefragmentation | Stranding Reduction | Delta stranding score or percentage | Reduction in resource stranding after descheduling. A larger reduction indicates better defragmentation. |
| HighNodeUtilization | Active Node Reduction | Delta active-node count | Number of active worker nodes eliminated after descheduling. A larger reduction indicates better consolidation. |
| LowNodeUtilization | Utilization Deviation Reduction | Delta CPU and memory standard deviation | Reduction in normalized CPU and memory request variation across nodes. A larger reduction indicates a more balanced placement. |

## Delta Definitions

```text
Stranding Reduction = S_before - S_after

Active Node Reduction = N_active,before - N_active,after

CPU Deviation Reduction = sigma_CPU,before - sigma_CPU,after

Memory Deviation Reduction = sigma_memory,before - sigma_memory,after
```

Positive values indicate improvement toward the strategy's placement objective.
For LowNodeUtilization, the standard deviation is calculated from each node's
normalized requests:

```text
Normalized CPU request = requested CPU / allocatable CPU

Normalized memory request = requested memory / allocatable memory
```
