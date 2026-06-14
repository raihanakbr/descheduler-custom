# Walkthrough: HNU With and Without ActualUsageEvictor

## Goal

Run one H0 comparison and one H1 comparison under sustained API load.

- H0 should empty three source nodes and reduce active nodes from 6 to 3.
- H1 should empty only the two idle sources, block the busy API eviction, and
  reduce active nodes from 6 to 4.

## Prerequisites

Run from `experiments/actual-usage-evictor-v2`. The scripts default to:

```bash
DESCHEDULER_IMAGE=docker.io/matthewhjt/descheduler-custom:actual-usage-v1
WORKLOAD_IMAGE=docker.io/matthewhjt/workload-http:actual-usage-v1
```

Export either variable only when overriding the default.

Optionally select six workers explicitly:

```bash
export ACTIVE_WORKERS="worker-1 worker-2 worker-3 worker-4 worker-5 worker-6"
```

The order matters: the first three are destinations, workers four and five
are idle sources, and worker six is the busy API source.

## Control-Plane Scheduler

Run the experiment from the control plane. Each H0/H1 runner automatically:

1. Installs the NodeResourcesFit/MostAllocated configuration from
   `hnu/scheduler/most-allocated-config.yaml` as
   `/etc/kubernetes/scheduler-config.yaml`.
2. Creates the one-time backup
   `/etc/kubernetes/kube-scheduler.yaml.pre-hnu`.
3. Adds the scheduler `--config` argument and hostPath mount idempotently.
4. Waits until the recreated kube-scheduler Pod is Ready.

The setup requires passwordless `sudo` and `python3-yaml` on the control plane.
All privileged commands use `sudo -n`, so the script never prompts for a
password. It is safe to run again: existing arguments, mounts, and volumes are
replaced rather than duplicated, and an unchanged manifest is not rewritten.
If the scheduler fails to become Ready, the script restores the backup.

To run only the scheduler setup or inspect its output:

```bash
./hnu/scripts/setup-scheduler.sh

kubectl -n kube-system get pod -l component=kube-scheduler \
  -o jsonpath='{.items[0].spec.containers[0].command}'
echo
```

## Run H0

```bash
./hnu/scripts/run-h0-with-load.sh
```

The script:

1. Configures and verifies the control-plane packing scheduler.
2. Cleans the previous namespace and descheduler Job.
3. Runs the parent six-worker preflight.
4. Creates and validates the HNU layout.
5. Starts the API lifecycle watcher and k6.
6. Waits for two API CPU samples at or above ratio 0.80.
7. Runs HNU without ActualUsageEvictor.
8. Captures the final layout and validates the expected result.

Expected validation:

```json
{
  "system": "H0",
  "evictions": 3,
  "actual_usage_blocks": 0,
  "active_nodes_after": 3,
  "original_api_remains": false
}
```

All experiment Pods should be on the first three workers. The original API UID
must disappear and its replacement must land on a destination.

## Run H1

```bash
./hnu/scripts/run-h1-with-load.sh
```

This command performs a full reset before rebuilding the same initial layout.

Expected validation:

```json
{
  "system": "H1",
  "evictions": 2,
  "actual_usage_blocks": 1,
  "active_nodes_after": 4,
  "original_api_remains": true
}
```

Workers four and five should be empty. The original API should remain on worker
six while replacements for the idle Pods land on workers one through three.

## Output

Each run creates one timestamped directory:

```text
hnu/results/h0-load/<timestamp>/
hnu/results/h1-load/<timestamp>/
```

Important files:

| File | Purpose |
|---|---|
| `layout-validation.json` | Initial source/destination classification |
| `scheduler-setup.log` | Automatic scheduler installation and readiness |
| `threshold-samples.tsv` | Actual API CPU ratio before descheduling |
| `descheduler.log` | HNU evictions and ActualUsageEvictor decisions |
| `pod-lifecycle.tsv` | Original and replacement API lifecycle |
| `cluster-metrics-before.json` | Initial active nodes, stranding, headroom |
| `cluster-metrics-after.json` | Final cluster metrics |
| `result-validation.json` | H0/H1 acceptance result |
| `summary.json` | Single-run HTTP, lifecycle, and cluster summary |

## Useful Checks

```bash
grep -E 'Evicted pod|blocking eviction|ActualUsageEvictor' \
  hnu/results/h1-load/<timestamp>/descheduler.log

cat hnu/results/h0-load/<timestamp>/result-validation.json
cat hnu/results/h1-load/<timestamp>/result-validation.json
```

## Calibration

Defaults match the parent load walkthrough:

```text
API_RPS=8
API_CPU_UNITS=900
THRESHOLD=0.80
```

If the threshold is not reached:

```bash
API_RPS=12 API_CPU_UNITS=1200 ./hnu/scripts/run-h1-with-load.sh
```

Do not increase load until the Pod is continuously throttled. The target is
actual CPU slightly above 80% of its 50m request, not saturation of its 1000m
limit.

## Failure Conditions

The runner exits non-zero when:

- initial placement does not contain exactly three HNU sources;
- API load fails to reach the threshold;
- H0 does not produce three evictions and three active nodes;
- H1 does not produce two evictions, one or more blocks, and four active nodes;
- a replacement lands back on a source node;
- the H1 API UID or placement changes.
