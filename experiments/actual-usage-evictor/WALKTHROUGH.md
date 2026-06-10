# Experiment execution walkthrough

Run all commands from a machine that:

- has this repository and the two images from `IMAGE_BUILD.md`;
- has `kubectl` access to the cluster;
- has `k6`, `python3`, and Docker;
- can reach the workers' NodePort addresses.

The simplest choice is the control-plane machine. The current runner starts k6
locally, so do not run the orchestration script on one machine and k6 manually
on another.

## 1. Enter the repository

```bash
cd /home/matthewhjt/TA/descheduler-custom-real-usage-fixed

export EXP_DIR="$PWD/experiments/actual-usage-evictor"
```

## 2. Check local tools and cluster access

```bash
kubectl config current-context
kubectl get nodes -o wide
kubectl top nodes

k6 version
python3 --version
docker version
```

The experiment needs at least five workers and a functioning Metrics API.

Check it directly:

```bash
kubectl get apiservice v1beta1.metrics.k8s.io
kubectl get --raw /apis/metrics.k8s.io/v1beta1/nodes | head
```

Do not continue until `kubectl top nodes` and the raw Metrics API call work.

## 3. Select the prebuilt images

Use the immutable tags produced by `IMAGE_BUILD.md`:

```bash
export WORKLOAD_IMAGE="docker.io/matthewhjt/workload-http:actual-usage-v1"
export DESCHEDULER_IMAGE="docker.io/matthewhjt/descheduler-custom:actual-usage-v1"
```

No image is built during experiment execution.

## 4. Pin the worker roles

List workers:

```bash
kubectl get nodes \
  -l '!node-role.kubernetes.io/control-plane,!node-role.kubernetes.io/master' \
  -o custom-columns='NAME:.metadata.name,INTERNAL_IP:.status.addresses[0].address'
```

Choose five active workers. Keep this mapping unchanged for every cell:

```bash
export ACTIVE_WORKERS="worker-1 worker-2 worker-3 worker-4 worker-5"
export SOURCE_NODE="worker-1"
```

`SOURCE_NODE` receives the hotspot Pod. It must be one of `ACTIVE_WORKERS`.

If your node names differ, replace the example values.

## 5. Export common experiment configuration

```bash
export WORKLOAD_IMAGE
export DESCHEDULER_IMAGE
export ACTIVE_WORKERS
export SOURCE_NODE
```

By default, k6 accesses the source worker's InternalIP. This is appropriate when
running from the control plane or the same private network.

If running from a machine that cannot reach worker InternalIPs, expose a
reachable address for the source worker:

```bash
export FOREGROUND_HOST="<reachable-source-worker-ip>"
export HOTSPOT_HOST="<reachable-source-worker-ip>"
```

The hotspot Service uses `externalTrafficPolicy: Local`, so
`HOTSPOT_HOST` must address `SOURCE_NODE`.

## 6. Run preflight

```bash
"$EXP_DIR/scripts/preflight.sh"
```

Expected output:

- current Kubernetes context;
- five or more workers;
- CPU and memory allocatable values;
- `Metrics API: available`;
- the selected descheduler image.

Preflight does not deploy the experiment.

## 7. Run a short CPU smoke test

Start with one `R1` run before running the full suite:

```bash
FOREGROUND_STABILIZE_SECONDS=20 \
PRE_EVENT_SECONDS=20 \
POST_EVENT_SECONDS=30 \
FOREGROUND_DURATION=5m \
HOTSPOT_DURATION=4m \
"$EXP_DIR/scripts/run-cell.sh" cpu R1 1
```

The script automatically:

1. Deletes the previous experiment namespaces.
2. Places seven HTTP Pods on five workers in a complementary fragmented layout.
3. Verifies the hotspot and companion are on the memory-skewed source.
4. Starts foreground k6.
5. Confirms two CPU samples remain below `usage/request = 0.80`.
6. Starts CPU hotspot k6.
7. Waits for two CPU samples with `usage/request >= 0.80`.
8. Records the pre-event window.
9. Runs one RDC2 pass with ActualUsageEvictor.
10. Records the post-event window.
11. Writes a run summary.

Find the newest result:

```bash
export RUN_DIR="$(find "$EXP_DIR/results/sustained/cpu/R1/repeat-1" \
  -mindepth 1 -maxdepth 1 -type d | sort | tail -1)"

echo "$RUN_DIR"
cat "$RUN_DIR/summary.txt"
cat "$RUN_DIR/threshold-samples.tsv"
grep -E 'blocking eviction|Evicted pod|Eviction decision' \
  "$RUN_DIR/descheduler.log" || true
```

For `R1`, the expected smoke-test evidence is:

- two threshold samples are `true`;
- the hotspot Pod is logged as blocked;
- the hotspot Pod is not deleted;
- any fallback eviction, if present, is for a non-hotspot Pod whose actual usage
  is below the configured threshold;
- foreground k6 continues through the event.

## 8. Run baseline smoke tests

Run the matching RDC2 cell without ActualUsageEvictor:

```bash
FOREGROUND_STABILIZE_SECONDS=20 \
PRE_EVENT_SECONDS=20 \
POST_EVENT_SECONDS=30 \
FOREGROUND_DURATION=5m \
HOTSPOT_DURATION=4m \
"$EXP_DIR/scripts/run-cell.sh" cpu R0 1
```

Expected: `R0` evicts the hotspot Pod.

The after snapshot should also show that the replacement lands on a CPU-skewed
worker, the companion remains on the source, request-space stranding
decreases, and balanced headroom increases.

Run the no-descheduler control:

```bash
FOREGROUND_STABILIZE_SECONDS=20 \
PRE_EVENT_SECONDS=20 \
POST_EVENT_SECONDS=30 \
FOREGROUND_DURATION=5m \
HOTSPOT_DURATION=4m \
"$EXP_DIR/scripts/run-cell.sh" cpu N0 1
```

Expected: hotspot load exists, but no eviction occurs. This separates natural
load degradation from additional eviction disruption.

Run the HNU negative-baseline cells:

```bash
FOREGROUND_STABILIZE_SECONDS=20 \
PRE_EVENT_SECONDS=20 \
POST_EVENT_SECONDS=30 \
FOREGROUND_DURATION=5m \
HOTSPOT_DURATION=4m \
"$EXP_DIR/scripts/run-cell.sh" cpu H0 1

FOREGROUND_STABILIZE_SECONDS=20 \
PRE_EVENT_SECONDS=20 \
POST_EVENT_SECONDS=30 \
FOREGROUND_DURATION=5m \
HOTSPOT_DURATION=4m \
"$EXP_DIR/scripts/run-cell.sh" cpu H1 1
```

Expected: both HNU cells evict zero Pods because no worker is below `40%` on
both CPU and memory. `H1` therefore does not reach an eviction decision for
ActualUsageEvictor to block.

Do not start the full experiment until `R0` evicts the intended hotspot Pod,
`R1` blocks that same hotspot Pod without deleting it, and `H0`/`H1` both report
zero evictions.

## 9. Calibrate CPU load if needed

The defaults are:

```text
HOTSPOT_RPS=8
HOTSPOT_CPU_UNITS=900
CPU request=100m
CPU threshold=0.80
```

If `wait-threshold.py` times out, increase load gradually:

```bash
export HOTSPOT_RPS=10
export HOTSPOT_CPU_UNITS=1100
```

If the hotspot produces excessive timeout/429 responses before the event, lower
one value:

```bash
export HOTSPOT_RPS=6
export HOTSPOT_CPU_UNITS=800
```

Once calibrated, keep the same values for `N0`, `R0`, `R1`, `H0`, and `H1`.
Record the exported values in the paper's experiment configuration.

## 10. Calibrate memory load

Run one memory `R1` smoke test:

```bash
HOTSPOT_RPS=1 \
HOTSPOT_MEM_MB=23 \
HOTSPOT_HOLD_MS=9000 \
FOREGROUND_STABILIZE_SECONDS=20 \
PRE_EVENT_SECONDS=20 \
POST_EVENT_SECONDS=30 \
FOREGROUND_DURATION=5m \
HOTSPOT_DURATION=4m \
"$EXP_DIR/scripts/run-cell.sh" memory R1 1
```

Recommended memory calibration:

```text
HOTSPOT_RPS=1
HOTSPOT_MEM_MB=23
HOTSPOT_HOLD_MS=9000
Memory request=250Mi
Memory threshold=0.80
Approximate threshold usage=200Mi
```

Use the lower-churn memory settings explicitly for the smoke test:

```bash
export HOTSPOT_RPS=1
export HOTSPOT_MEM_MB=23
export HOTSPOT_HOLD_MS=9000
```

If the ratio does not reach `0.80`, increase `HOTSPOT_MEM_MB` in small steps,
for example from `23` to `25`, while keeping RPS at `1`. The threshold is based
on the Pod's `250Mi` memory request, not the node's allocatable memory. Avoid
increasing load until the Pod is OOMKilled or the node enters `MemoryPressure`.
Check:

```bash
kubectl get pods -n actual-usage-exp
kubectl describe node "$SOURCE_NODE" | sed -n '/Conditions:/,/Addresses:/p'
kubectl get events -n actual-usage-exp --sort-by=.lastTimestamp
```

After calibration, keep the same memory values across all systems.

## 11. Run the full sustained CPU suite

Remove smoke-test timing overrides or start a fresh shell with the common
exports from Steps 4-5.

Recommended full timing:

```bash
export FOREGROUND_STABILIZE_SECONDS=60
export PRE_EVENT_SECONDS=60
export POST_EVENT_SECONDS=120
export FOREGROUND_DURATION=12m
export HOTSPOT_DURATION=10m
export LOAD_PATTERN=sustained
```

Run five repeats:

```bash
REPEATS=5 "$EXP_DIR/scripts/run-suite.sh" cpu
```

The fixed order for every repeat is:

```text
N0 -> R0 -> R1 -> H0 -> H1
```

One repeat means a complete reset and new run for every system. It is not five
descheduler passes.

## 12. Run the full sustained memory suite

Keep the calibrated memory parameters exported:

```bash
REPEATS=5 "$EXP_DIR/scripts/run-suite.sh" memory
```

## 13. Run idle controls

Idle control starts only foreground load. It verifies that ActualUsageEvictor
does not block a Pod below the thresholds.

```bash
export LOAD_PATTERN=idle
export IDLE_OBSERVE_SECONDS=45

"$EXP_DIR/scripts/run-cell.sh" cpu R0 1
"$EXP_DIR/scripts/run-cell.sh" cpu R1 1
"$EXP_DIR/scripts/run-cell.sh" cpu H0 1
"$EXP_DIR/scripts/run-cell.sh" cpu H1 1
```

Expected: filtered and unfiltered variants behave similarly because the Pod is
not busy.

Restore sustained mode afterward:

```bash
export LOAD_PATTERN=sustained
```

## 14. Run transient checks

Transient mode stops hotspot k6 after the threshold is observed and waits five
seconds before the event:

```bash
export LOAD_PATTERN=transient
export TRANSIENT_GAP_SECONDS=5

"$EXP_DIR/scripts/run-cell.sh" cpu R1 1
"$EXP_DIR/scripts/run-cell.sh" memory R1 1
```

This checks whether stale Metrics Server or retained memory still blocks an
otherwise no-longer-busy Pod.

Restore:

```bash
export LOAD_PATTERN=sustained
```

## 15. Inspect results

Per-run summary:

```bash
cat "$RUN_DIR/summary.txt"
```

Important artifacts:

```text
threshold-samples.tsv        actual/request threshold evidence
baseline-samples.tsv         pre-hotspot samples below the busy threshold
layout-validation.json       predicted RDC2 selection and HNU source check
descheduler.log              selected, blocked, and evicted Pods
foreground.json              raw foreground k6 time series
foreground-summary.json      k6 whole-run summary
pod-lifecycle.tsv            deletion, replacement scheduling, readiness
cluster-metrics-before.json  stranding/headroom before
cluster-metrics-event.json   metrics at event time
cluster-metrics-after.json   stranding/headroom after
layout-before.txt            initial Pod placement
layout-after.txt             placement after the event
events.txt                   Kubernetes events
summary.txt                  pre/post application and lifecycle summary
```

`summary.txt` includes the before/event/after cluster metrics. For the primary
comparison, verify:

```text
R0: eviction=1 for the hotspot, S decreases, H_balanced increases
R1: blocked>=1 for the hotspot; hotspot deletion is not observed; any eviction
    is a below-threshold fallback Pod, not the hotspot
H0/H1: eviction=0 (negative HNU baseline)
```

Aggregated suite results:

```bash
cat "$EXP_DIR/results/sustained/cpu/aggregate.json"
cat "$EXP_DIR/results/sustained/memory/aggregate.json"
```

Rebuild aggregation without rerunning experiments:

```bash
python3 "$EXP_DIR/scripts/aggregate-results.py" cpu --pattern sustained
python3 "$EXP_DIR/scripts/aggregate-results.py" memory --pattern sustained
```

## 16. Cleanup

After finishing or after an interrupted run:

```bash
"$EXP_DIR/scripts/cleanup.sh"
```

Verify:

```bash
kubectl get ns actual-usage-exp actual-usage-system
kubectl get nodes
kubectl -n kube-system get job actual-usage-descheduler
```

The cleanup script removes experiment namespaces and the descheduler Job, then
uncordons all workers. It does not delete result files or pushed images.

## Minimum execution order

For the shortest defensible workflow:

```text
1. Select the prebuilt immutable images
2. Export fixed worker mapping
3. Run preflight
4. CPU smoke: R0, R1, N0, H0, H1
5. Memory smoke: R0, R1, N0
6. Calibrate and freeze load parameters
7. Full CPU suite, five repeats
8. Full memory suite, five repeats
9. Idle controls
10. Transient checks
11. Inspect aggregate.json and raw evidence
12. Cleanup
```
