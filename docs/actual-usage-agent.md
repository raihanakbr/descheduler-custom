# Actual Usage Agent

The actual usage agent is a thesis-oriented monitoring component for the `ResourceDefragmentation` work. It follows the monitoring-agent idea from Marchese and Tomarchio's load-aware Kubernetes orchestration work: observe runtime infrastructure/application metrics, keep history, and feed scheduling/descheduling decisions. It observes Kubernetes runtime metrics over time, smooths node CPU/memory usage with EWMA, computes per-node Resource Imbalance Index (RII), records evidence files, and reports when remediation by the PR1 descheduler plugin is recommended.

## Relation to PR1

PR1 adds the `ResourceDefragmentation` balance plugin and lets the plugin read actual runtime usage from metrics-server when the descheduler policy includes:

```yaml
metricsProviders:
  - source: KubernetesMetrics
```

This agent is intentionally separate from that plugin logic:

- PR1 plugin: active remediation path; selects pods and evicts via descheduler.
- PR2 agent: observation/evidence path; periodically reads actual metrics, computes RII history, and writes experiment artifacts.

The agent does **not** compute RII from Kubernetes requests/limits. Requests and limits remain relevant to Kubernetes scheduling feasibility, but this agent's RII values use runtime CPU/memory usage from `metrics.k8s.io`.

## Metrics source

Current implementation supports metrics-server through the Kubernetes Metrics API:

- Node metrics: `metrics.k8s.io/v1beta1` `NodeMetrics`
- Pod metrics: `metrics.k8s.io/v1beta1` `PodMetrics`

Prometheus is left as future work because the repository already has metrics-server support in PR1 and metrics-server is enough for the thesis AWS Learning experiment.

## RII calculation

For each observed node, raw metrics-server node usage is first smoothed with EWMA using the same default beta as the PR1 descheduler `MetricsCollector` (`--ewma-beta=0.9`):

```text
smoothed_value = beta * previous_smoothed_value + (1 - beta) * current_raw_value
```

The first observation initializes the smoothed value from the raw value. RII is then computed from the smoothed node usage:

```text
cpu_usage_ratio = actual_cpu_used_millicores / node_allocatable_cpu_millicores
memory_usage_ratio = actual_memory_used_bytes / node_allocatable_memory_bytes
RII = cpu_usage_ratio - memory_usage_ratio
```

A node is marked fragmented when:

```text
abs(RII) > --fragmentation-threshold
```

Default threshold: `0.20`.

## Output format

The agent writes two evidence files under `--output-dir`.

### `snapshots.jsonl`

One JSON object per collection tick. Fields include:

- `timestamp`
- `metricsSource`
- `fragmentationThreshold`
- `nodeCount`
- `podCount`
- `fragmentedNodeCount`
- `smoothingMethod` and `ewmaBeta`
- `nodes[]` with raw CPU/memory usage, EWMA-smoothed CPU/memory usage, capacity, ratios, RII, and fragmented status
- `pods[]` with actual CPU/memory usage and node placement
- `remediationRecommended`
- `recommendation`

### `node_rii_history.csv`

Append-only CSV row per node per collection tick:

```text
timestamp,node,metrics_source,smoothing_method,ewma_beta,raw_cpu_used_milli,raw_memory_used_bytes,cpu_used_milli,memory_used_bytes,cpu_capacity_milli,memory_capacity_bytes,cpu_usage_ratio,memory_usage_ratio,rii,fragmented
```

This CSV is intended for thesis plots/tables showing RII over time.

## Running as a Deployment

```bash
kubectl apply -k kubernetes/actual-usage-agent
```

The Deployment writes into an `emptyDir` at `/var/log/actual-usage-agent`. For long experiments, replace the volume with a PVC or copy files out before deleting the pod.

## Running as a CronJob

Use the CronJob manifest if each schedule should collect one snapshot and exit:

```bash
kubectl apply -f kubernetes/actual-usage-agent/rbac.yaml
kubectl apply -f kubernetes/actual-usage-agent/cronjob.yaml
```

## Local run

```bash
go run ./cmd/actual-usage-agent \
  --kubeconfig ~/.kube/config \
  --run-once=true \
  --output-dir ./actual-usage-agent-output \
  --fragmentation-threshold 0.20
```

## Remediation behavior

Current remediation mode is `report`. When fragmented nodes are detected, the agent records a recommendation to run the descheduler `ResourceDefragmentation` policy. It does not evict pods directly and does not mutate cluster objects.

This is deliberate for PR2 clean separation: the monitoring agent produces evidence/history, while PR1 remains the remediation implementation.


## Publishing EWMA state for loose descheduling

For the E5 experiment group, run the agent as a long-running Deployment and publish its latest EWMA node usage to node annotations:

```bash
actual-usage-agent --publish-target=node-annotations --interval=30s
```

The descheduler can then run as a CronJob with `ResourceDefragmentation` configured as `usageMode: published-ewma`. The consumer reads:

- `descheduler.thesis/actual-cpu-milli`
- `descheduler.thesis/actual-memory-bytes`
- `descheduler.thesis/timestamp`
- supporting metadata such as raw usage, RII, smoothing method, EWMA beta, and metrics source

Set `publishedUsageMaxAgeSeconds` in the descheduler plugin args to reject stale agent state. This keeps EWMA state in the long-running monitoring agent instead of resetting on every descheduler CronJob execution.
