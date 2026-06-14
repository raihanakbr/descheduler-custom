# HNU ActualUsageEvictor Experiment Plan

## Core Claim

> ActualUsageEvictor protects a busy application Pod from HNU eviction while
> allowing HNU to continue consolidating idle source nodes. The protection
> avoids application disruption at the cost of keeping one additional node
> active.

## Comparison

| ID | Balance strategy | Pre-eviction filters | Expected result |
|---|---|---|---|
| H0 | HighNodeUtilization | DefaultEvictor | 3 evictions, active nodes 6 -> 3 |
| H1 | HighNodeUtilization | DefaultEvictor + ActualUsageEvictor | 2 evictions, 1 block, active nodes 6 -> 4 |

Only the API Pod receives load. The two other source Pods remain idle.

## Initial Placement

The experiment uses the same six reference workers as the main v2 experiment:
approximately 2000m CPU and 811Mi allocatable per worker, including an
approximate 100m CPU / 50Mi DaemonSet baseline.

HNU uses request-based thresholds of 40% CPU and 40% memory. A node is a source
only when both dimensions are below their thresholds.

| Worker | Role | Experiment Pods | Approx. total requests | HNU classification |
|---|---|---|---|---|
| worker-1 | destination | 2 x 650m/150Mi | 1400m/350Mi | destination: CPU > 40% |
| worker-2 | destination | 2 x 650m/150Mi | 1400m/350Mi | destination: CPU > 40% |
| worker-3 | destination | 2 x 650m/150Mi | 1400m/350Mi | destination: CPU > 40% |
| worker-4 | idle source A | 1 x 50m/160Mi | 150m/210Mi | source |
| worker-5 | idle source B | 1 x 50m/160Mi | 150m/210Mi | source |
| worker-6 | busy source | API 50m/240Mi | 150m/290Mi | source |

There are nine experiment Pods across six active workers. Sequential
cordon/uncordon placement makes the initial layout deterministic.

Worker-4 and worker-5 have lower requested usage than worker-6, so HNU should
process the idle sources before the busy source. The exact order between the
two idle sources is irrelevant.

## Load Model

One k6 process sends constant-arrival-rate HTTP traffic only to
`workload-api`. The runner waits until the API Pod has two consecutive Metrics
API CPU samples with:

```text
actual CPU / requested CPU >= 0.80
```

The HNU source classification still uses requests. ActualUsageEvictor alone
uses runtime Metrics API observations.

## Expected H0 Behavior

```text
1. HNU identifies worker-4, worker-5, and worker-6 as sources.
2. It evicts the idle Pod from worker-4.
3. It evicts the idle Pod from worker-5.
4. It evicts the busy API Pod from worker-6.
5. The packing scheduler places all replacements on worker-1..3.
6. worker-4..6 become empty.
7. Active nodes decrease from 6 to 3.
8. API eviction produces HTTP failures and a latency spike.
```

Expected final placement:

```text
worker-1..3: original destination Pods plus replacements
worker-4..6: no experiment Pods
```

## Expected H1 Behavior

```text
1. HNU evicts both idle source Pods.
2. HNU evaluates the API Pod on worker-6.
3. ActualUsageEvictor observes CPU ratio >= 0.80 and blocks eviction.
4. HNU has no alternative Pod on worker-6.
5. Idle replacements land on worker-1..3.
6. worker-4 and worker-5 become empty.
7. The original API remains on worker-6.
8. Active nodes decrease from 6 to 4.
```

## Expected Trade-off

| Metric | H0 | H1 |
|---|---:|---:|
| Busy Pods | 1 | 1 |
| Successful evictions | 3 | 2 |
| Actual usage blocks | 0 | >= 1 |
| Idle sources emptied | 2 | 2 |
| Busy source emptied | yes | no |
| Active nodes after | 3 | 4 |
| API HTTP failures | > 0 | approximately 0 |
| API p95 latency | eviction spike | stable |
| Consolidation | maximum | one fewer node removed |

## Execution

Each comparison is a single independent run:

```bash
./hnu/scripts/run-h0-with-load.sh
./hnu/scripts/run-h1-with-load.sh
```

There is no repeat loop or run suite. Each runner resets and recreates the
layout before running.

Each H0/H1 runner automatically installs
`hnu/scheduler/most-allocated-config.yaml` as
`/etc/kubernetes/scheduler-config.yaml`, patches the control-plane
kube-scheduler static Pod idempotently, and waits for it to become Ready. The
original manifest is backed up once outside the static-manifest directory as
`/etc/kubernetes/kube-scheduler.yaml.pre-hnu`.

The scheduler configuration sets
`clientConnection.kubeconfig: /etc/kubernetes/scheduler.conf`; static
kube-scheduler Pods do not have an in-cluster service-account token.

## Reused Parent Components

The HNU runner reuses the parent v2 preflight, cleanup, k6 workload, Metrics
API threshold waiter, snapshots, cluster metrics, lifecycle watcher,
descheduler Job runner, and single-run summarizer.

HNU-specific files only define its placement, policies, result assertions,
plan, walkthrough, and entrypoint scripts.

The runner defaults to
`docker.io/matthewhjt/descheduler-custom:actual-usage-v1` and allows
`DESCHEDULER_IMAGE` to override it.

## Acceptance Criteria

1. Initial validation finds exactly three source and three destination nodes.
2. The API is the only Pod on the busy source and reaches CPU ratio >= 0.80.
3. H0 produces exactly three evictions and finishes with three active nodes.
4. H1 produces exactly two evictions, at least one block, and four active nodes.
5. H0 replaces the API on worker-1..3; H1 preserves its original UID on worker-6.
6. H0 has greater application disruption than H1.

## Threats to Validity

1. The active-node result assumes the cluster scheduler uses
   NodeResourcesFit with the MostAllocated scoring strategy.
2. HNU source ordering uses aggregate requested resources. The unequal source
   memory requests make the busy source sort after the idle sources.
3. A single API replica intentionally amplifies eviction impact. The experiment
   measures singleton disruption, not a replicated rolling workload.
4. Metrics Server sampling can miss short usage changes. Two consecutive
   above-threshold samples are required before the event.
