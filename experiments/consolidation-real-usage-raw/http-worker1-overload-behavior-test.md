# HTTP worker-1 overload behavior test

Dokumen ini merancang test pendahuluan untuk mengukur efek runtime ketika
`worker-1` mengalami CPU atau memory overload. Berbeda dari
`actual-overload-behavior-test.md` yang memakai busy loop Pod, test ini memakai
HTTP workload dan k6 supaya dampaknya bisa dilihat dari metrik request:

```text
latency, request failure, HTTP status, node usage, pod usage, OOM/restart/event
```

Tujuan test ini belum menjalankan descheduler. Tujuannya adalah membuktikan
apakah actual overload menghasilkan dampak yang measurable pada workload.

## Framing

Framing yang ingin diuji:

```text
Pod bisa tetap Running saat node actual usage tinggi, tetapi kualitas runtime
belum tentu baik. Untuk CPU, dampak yang dicari adalah latency/throughput
degradation. Untuk memory, dampak yang dicari adalah HTTP failure, OOMKilled,
CrashLoopBackOff, MemoryPressure, atau eviction.
```

Jika hasil CPU menunjukkan latency/failure memburuk saat `worker-1` saturated,
hasil ini bisa menjadi bridge yang lebih kuat untuk `usageMode: actual-raw`:

```text
actual usage bukan hanya angka observability; actual usage dapat memprediksi
runtime risk dari placement/eviction target.
```

## Cluster target

| Role | Node |
|------|------|
| Control plane | `controlplane` |
| Worker target | `worker-1` |
| Worker yang dikosongkan sementara | `worker-2` |

Placement sengaja dibuat hanya ke `worker-1` dengan `cordon worker-2`. k6 harus
dijalankan dari luar `worker-1`, misalnya dari control plane atau laptop, supaya
CPU/memory k6 tidak ikut masuk ke metrik `worker-1`.

## Prasyarat

Metrics Server harus aktif:

```bash
kubectl get apiservice v1beta1.metrics.k8s.io
kubectl top nodes
kubectl top pods -A
```

k6 tersedia di host tempat load generator dijalankan:

```bash
k6 version
```

Opsional, jika k6 belum terinstall di Ubuntu/Debian:

```bash
sudo gpg -k || true

curl -fsSL https://dl.k6.io/key.gpg \
  | sudo gpg --dearmor -o /usr/share/keyrings/k6-archive-keyring.gpg

echo "deb [signed-by=/usr/share/keyrings/k6-archive-keyring.gpg] https://dl.k6.io/deb stable main" \
  | sudo tee /etc/apt/sources.list.d/k6.list

sudo apt update
sudo apt install -y k6

k6 version
```

Image workload sudah tersedia:

```text
docker.io/matthewhjt/workload-http-fixed:manual-v1
```

Opsional, build dan push image fixed-work jika belum tersedia:

```bash
export DOCKERHUB_USER=matthewhjt
export WORKLOAD_FIXED_IMAGE=docker.io/${DOCKERHUB_USER}/workload-http-fixed:manual-v1

sudo docker build \
  -t "$WORKLOAD_FIXED_IMAGE" \
  experiments/consolidation-real-usage-raw/cmd/workload-http-fixed

sudo docker push "$WORKLOAD_FIXED_IMAGE"
```

Set variable:

```bash
export NODE_A=worker-1
export NODE_B=worker-2
export WORKER_1_IP=$(kubectl get node "$NODE_A" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
echo "$WORKER_1_IP"
```

Jika k6 dijalankan dari laptop, gunakan public IP worker-1 untuk `WORKER_1_IP`.
Jika k6 dijalankan dari control plane, gunakan internal IP worker-1.

## Result directory

Jika command dijalankan dari repo di control plane:

```bash
export EXPERIMENT_START_DATE=$(date +%Y%m%d-%H%M%S)
export EXPERIMENT_DIR=experiments/consolidation-real-usage-raw
export RESULT_DIR=${EXPERIMENT_DIR}/results/http-worker1-overload-${EXPERIMENT_START_DATE}

mkdir -p \
  "$RESULT_DIR/00-baseline" \
  "$RESULT_DIR/01-cpu" \
  "$RESULT_DIR/02-memory"
```

## Cleanup and placement control

```bash
kubectl delete ns http-overload --ignore-not-found
kubectl wait --for=delete ns/http-overload --timeout=120s || true

kubectl cordon "$NODE_B"
kubectl uncordon "$NODE_A"
```

Create namespace:

```bash
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: http-overload
EOF
```

## Deploy HTTP workload on worker-1

This Deployment intentionally uses small requests. The Service uses
`externalTrafficPolicy: Local` so requests sent to `worker-1:NodePort` are served
only by local endpoints on `worker-1`.

The image is the fixed-work variant. It remains compatible with the existing k6
query parameter `cpu_ms`, but internally `cpu_ms` is interpreted as fixed CPU
work units rather than wall-clock milliseconds. This is intentional: if CPU
contention reduces CPU share, the same fixed work should take longer and become
visible in k6 latency.

```bash
kubectl apply -n http-overload -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workload-http
spec:
  replicas: 2
  selector:
    matchLabels:
      app: workload-http
  template:
    metadata:
      labels:
        app: workload-http
    spec:
      containers:
        - name: workload
          image: docker.io/matthewhjt/workload-http-fixed:manual-v1
          imagePullPolicy: Always
          ports:
            - containerPort: 8080
          readinessProbe:
            httpGet:
              path: /healthz
              port: 8080
            initialDelaySeconds: 3
            periodSeconds: 5
          env:
            - name: MAX_CPU_UNITS
              value: "5000"
            - name: ITERATIONS_PER_CPU_UNIT
              value: "200000"
            - name: MAX_MEM_MB
              value: "160"
            - name: MAX_HOLD_MS
              value: "5000"
            - name: MAX_INFLIGHT
              value: "64"
            - name: MAX_TOTAL_ALLOC_MB
              value: "640"
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              memory: 760Mi
---
apiVersion: v1
kind: Service
metadata:
  name: workload-http
spec:
  type: NodePort
  externalTrafficPolicy: Local
  selector:
    app: workload-http
  ports:
    - name: http
      port: 80
      targetPort: 8080
EOF

kubectl rollout status deployment/workload-http -n http-overload --timeout=180s
kubectl get pods -n http-overload -o wide
kubectl get svc -n http-overload workload-http -o wide
```

Verify both Pods are on `worker-1`:

```bash
kubectl get pods -n http-overload -o wide
```

Get NodePort:

```bash
export NODEPORT=$(kubectl get svc -n http-overload workload-http -o jsonpath='{.spec.ports[0].nodePort}')
echo "$NODEPORT"
```

Smoke test:

```bash
curl -sS "http://${WORKER_1_IP}:${NODEPORT}/healthz"
curl -sS "http://${WORKER_1_IP}:${NODEPORT}/cpu/work?cpu_ms=100&mem_mb=0&hold_ms=0"
```

## Baseline capture

```bash
kubectl get nodes -o wide 2>&1 \
  | tee "$RESULT_DIR/00-baseline/nodes-wide.txt"
kubectl get pods -n http-overload -o wide 2>&1 \
  | tee "$RESULT_DIR/00-baseline/pods-wide.txt"
kubectl describe node "$NODE_A" 2>&1 \
  | tee "$RESULT_DIR/00-baseline/node-a-describe.txt"
kubectl top nodes 2>&1 \
  | tee "$RESULT_DIR/00-baseline/top-nodes.txt"
kubectl top pods -n http-overload --containers 2>&1 \
  | tee "$RESULT_DIR/00-baseline/top-pods.txt"
```

Optional baseline k6 with low load:

```bash
WORKER_IP="$WORKER_1_IP" \
NODEPORT="$NODEPORT" \
MODE=cpu \
TARGET_RPS=1 \
WARMUP_RPS=1 \
WARMUP=5s \
RAMP_TO_PEAK=5s \
HOLD=30s \
RAMP_DOWN=5s \
k6 run experiments/consolidation-real-usage-raw/k6/worker1-overload.js \
  2>&1 | tee "$RESULT_DIR/00-baseline/k6-low-cpu.log"
```

## Test A: CPU overload with HTTP latency

### Hypothesis

When `worker-1` actual CPU reaches saturation, HTTP request latency should
increase and possibly produce request failures/timeouts. Pods may remain
`Running` because CPU is compressible.

### Stage model

The k6 script uses explicit stages:

```text
WARMUP       -> low load
RAMP_TO_PEAK -> ramp from warmup RPS to target RPS
HOLD         -> stable peak load
RAMP_DOWN    -> cooldown to 0 RPS
```

Use a longer `HOLD` for background load and a shorter `HOLD` for victim load.
The CPU flow has three steps:

```text
1. Capture victim load without background load.
2. Run background load until worker-1 reaches a stable high actual CPU level.
3. Capture the same victim load while background load is still running.
```

### Step 1: capture victim load without background load

Run this once before starting background load. This gives a fair baseline for
the exact CPU load profile that will also be used as the background load.

The only difference between victim and background commands is duration:

```text
victim:     short HOLD, used for latency measurement
background: longer HOLD, kept active while victim is measured
```

Use a separate output directory for Step 1:

```bash
export STEP1_DIR="$RESULT_DIR/01-cpu/01-victim-baseline"
mkdir -p "$STEP1_DIR"

WORKER_IP="$WORKER_1_IP" \
NODEPORT="$NODEPORT" \
MODE=cpu \
CPU_MS=500 \
WARMUP_RPS=1 \
TARGET_RPS=3 \
WARMUP=10s \
RAMP_TO_PEAK=20s \
HOLD=60s \
RAMP_DOWN=10s \
PREALLOCATED_VUS=50 \
MAX_VUS=100 \
k6 run experiments/consolidation-real-usage-raw/k6/worker1-overload.js \
  2>&1 | tee "$STEP1_DIR/k6-victim-baseline.log"
```

Capture node state while the victim baseline is running. Start this command as
soon as the k6 victim baseline starts. The sleep intervals follow the victim
stage boundaries: `10s warmup`, `20s ramp`, `60s hold`, `10s rampdown`.

```bash
export STEP1_DIR="$RESULT_DIR/01-cpu/01-victim-baseline"
mkdir -p "$STEP1_DIR"

capture_cpu_state() {
  label="$1"
  {
    echo "stage=${label}"
    date -Is
    kubectl top nodes
    kubectl top pods -n http-overload --containers
  } 2>&1 | tee "$STEP1_DIR/${label}.txt"
}

capture_cpu_state "00-start"
sleep 10
capture_cpu_state "01-warmup-end"
sleep 20
capture_cpu_state "02-ramp-to-peak-end"
sleep 30
capture_cpu_state "03-hold-mid"
sleep 40
capture_cpu_state "04-run-end"
```

### Step 2: run background CPU load

Run this in one shell and keep it running while the victim test is executed.

This uses the same CPU profile as Step 1, but with a longer `HOLD`. The purpose
is to create the same kind of work as the victim, then measure another identical
victim run while that background work is already active.

For a short validation run, use `HOLD=120s`. For a full run where the victim
test may need more time, increase background `HOLD` to `5m` and stop k6 with
`Ctrl-C` after the victim test finishes.

```bash
export STEP2_DIR="$RESULT_DIR/01-cpu/02-background"
mkdir -p "$STEP2_DIR"

WORKER_IP="$WORKER_1_IP" \
NODEPORT="$NODEPORT" \
MODE=cpu \
CPU_MS=500 \
WARMUP_RPS=1 \
TARGET_RPS=3 \
WARMUP=10s \
RAMP_TO_PEAK=20s \
HOLD=120s \
RAMP_DOWN=10s \
PREALLOCATED_VUS=50 \
MAX_VUS=100 \
k6 run experiments/consolidation-real-usage-raw/k6/worker1-overload.js \
  2>&1 | tee "$STEP2_DIR/k6-background-cpu.log"
```

Capture background state from another shell. For background load, the important
state is the peak/hold region, because that is the condition we want the victim
load to run under.

Start this command at the same time as the background k6 run. It waits for
warmup plus ramp-to-peak (`10s + 20s`), then captures three samples during the
hold phase.

Start the victim-under-background command after the first peak sample confirms
that `worker-1` is in the desired high-CPU region.

```bash
export STEP2_DIR="$RESULT_DIR/01-cpu/02-background"
mkdir -p "$STEP2_DIR"

capture_background_state() {
  label="$1"
  {
    echo "stage=${label}"
    date -Is
    kubectl get pods -n http-overload -o wide
    kubectl top nodes
    kubectl top pods -n http-overload --containers
  } 2>&1 | tee "$STEP2_DIR/${label}.txt"
}

sleep 30
capture_background_state "00-peak-start"
sleep 30
capture_background_state "01-peak-mid"
sleep 30
capture_background_state "02-peak-late"
```

### Step 3: capture victim load with background load

Run Step 3 with three terminals started at roughly the same time:

```text
Terminal 1: run background load
Terminal 2: sleep 30s, then run victim load
Terminal 3: sleep 30s, then capture the victim-under-background window
```

The `30s` delay matches background warmup plus ramp-to-peak:

```text
10s WARMUP + 20s RAMP_TO_PEAK = 30s
```

This means the victim starts when background load is already in the peak/hold
region.

Step 2 already validates the background-only peak. Step 3 does not need a
separate background-only capture. Each victim-under-background capture still
includes `kubectl top nodes`, so the combined background plus victim condition
is visible.

Use a separate output directory for this Step 3 run so the logs do not
overwrite the earlier under-background attempt. Run this in each terminal used
for Step 3:

```bash
export STEP3_DIR="$RESULT_DIR/01-cpu/04-under-background-same-profile"
mkdir -p "$STEP3_DIR"
```

#### Terminal 1: background load

Run the same tuned background profile from Step 2:

```bash
export STEP3_DIR="$RESULT_DIR/01-cpu/04-under-background-same-profile"
mkdir -p "$STEP3_DIR"

WORKER_IP="$WORKER_1_IP" \
NODEPORT="$NODEPORT" \
MODE=cpu \
CPU_MS=500 \
WARMUP_RPS=1 \
TARGET_RPS=3 \
WARMUP=10s \
RAMP_TO_PEAK=20s \
HOLD=120s \
RAMP_DOWN=10s \
PREALLOCATED_VUS=50 \
MAX_VUS=100 \
k6 run experiments/consolidation-real-usage-raw/k6/worker1-overload.js \
  2>&1 | tee "$STEP3_DIR/k6-background-cpu.log"
```

#### Terminal 2: delayed victim load

This command waits `30s`, then starts the victim profile. The victim command
must use the same parameters as the Step 1 victim baseline.

```bash
export STEP3_DIR="$RESULT_DIR/01-cpu/04-under-background-same-profile"
mkdir -p "$STEP3_DIR"

echo "Waiting 30s before starting victim load..."
sleep 30

WORKER_IP="$WORKER_1_IP" \
NODEPORT="$NODEPORT" \
MODE=cpu \
CPU_MS=500 \
WARMUP_RPS=1 \
TARGET_RPS=3 \
WARMUP=10s \
RAMP_TO_PEAK=20s \
HOLD=60s \
RAMP_DOWN=10s \
PREALLOCATED_VUS=50 \
MAX_VUS=100 \
k6 run experiments/consolidation-real-usage-raw/k6/worker1-overload.js \
  2>&1 | tee "$STEP3_DIR/k6-victim-under-background.log"
```

#### Terminal 3: delayed capture

This command also waits `30s`, then captures the victim-under-background window
while the victim load is running.

```bash
export STEP3_DIR="$RESULT_DIR/01-cpu/04-under-background-same-profile"
mkdir -p "$STEP3_DIR"

capture_step3_state() {
  label="$1"
  {
    echo "stage=${label}"
    date -Is
    kubectl get pods -n http-overload -o wide
    kubectl top nodes
    kubectl top pods -n http-overload --containers
    kubectl describe node "$NODE_A" | sed -n '/Conditions:/,/Addresses:/p'
    kubectl get events -n http-overload --sort-by=.lastTimestamp
  } 2>&1 | tee "$STEP3_DIR/${label}.txt"
}

echo "Waiting 30s for background load to reach peak and victim load to start..."
sleep 30

capture_step3_state "00-victim-start"
sleep 10
capture_step3_state "01-victim-warmup-end"
sleep 20
capture_step3_state "02-victim-ramp-to-peak-end"
sleep 30
capture_step3_state "03-victim-hold-mid"
sleep 40
capture_step3_state "04-victim-run-end"
```

Compare:

```text
01-victim-baseline/k6-victim-baseline.log
01-victim-baseline/*.txt
02-background/k6-background-cpu.log
02-background/*.txt
04-under-background-same-profile/k6-victim-under-background.log
04-under-background-same-profile/*.txt
```

The victim comparison is fair because `CPU_MS`, `TARGET_RPS`, stage durations,
and VU limits are the same. The only intended difference is that the second
victim run happens while background CPU load is active on `worker-1`.

Expected observation:

```text
worker-1 actual CPU is high during background load
workload Pods remain Running
k6 victim http_req_duration p95/p99 increases compared to victim baseline
http_req_failed may increase if requests exceed timeout or server capacity
```

Interpretation:

```text
CPU overload is measurable as service degradation, not only as kubectl top CPU.
```

## Cooldown before memory test

Wait until CPU returns near baseline:

```bash
for sample in 1 2 3; do
  date -Is
  kubectl top nodes
  kubectl top pods -n http-overload --containers
  sleep 20
done
```

## Test B: Memory overload with HTTP failures/runtime effects

### Hypothesis

When memory-heavy HTTP requests accumulate on `worker-1`, the runtime impact may
show up as one or more of:

```text
higher latency because requests hold memory longer
HTTP 429 from workload memory budget guard
container OOMKilled
CrashLoopBackOff
node MemoryPressure=True
kubelet eviction
```

The exact outcome depends on timing, request concurrency, cgroup limits, and
kubelet thresholds. This test records the actual observed outcome rather than
assuming a specific one.

### Run k6 memory load

Start with this profile:

```bash
WORKER_IP="$WORKER_1_IP" \
NODEPORT="$NODEPORT" \
MODE=memory \
CPU_MS=20 \
MEM_MB=96 \
HOLD_MS=3000 \
TARGET_RPS=8 \
WARMUP_RPS=4 \
WARMUP=10s \
RAMP_TO_PEAK=15s \
PREALLOCATED_VUS=100 \
MAX_VUS=220 \
HOLD=60s \
RAMP_DOWN=10s \
k6 run experiments/consolidation-real-usage-raw/k6/worker1-overload.js \
  2>&1 | tee "$RESULT_DIR/02-memory/k6-memory-overload.log"
```

While k6 is running, capture metrics from another shell:

```bash
for sample in 1 2 3 4 5; do
  {
    date -Is
    kubectl get pods -n http-overload -o wide
    kubectl describe pod -n http-overload -l app=workload-http
    kubectl top nodes
    kubectl top pods -n http-overload --containers
    kubectl describe node "$NODE_A" | sed -n '/Conditions:/,/Addresses:/p'
    kubectl get events -n http-overload --sort-by=.lastTimestamp
    kubectl logs -n http-overload -l app=workload-http --tail=80
  } 2>&1 | tee "$RESULT_DIR/02-memory/memory-sample-${sample}.txt"
  sleep 20
done
```

Expected observation:

```text
worker-1 memory rises compared to baseline
k6 may show higher http_req_duration
k6 may show non-200 responses, especially 429 if MAX_TOTAL_ALLOC_MB is hit
Pods may remain Running, restart, OOMKill, or trigger MemoryPressure depending
on actual pressure
```

Interpretation:

```text
Memory overload is not safely compressible. The result must distinguish between
application-level guard failures, container OOMKilled, kubelet eviction, and
node MemoryPressure.
```

## Pull results

From local machine:

```bash
export EXPERIMENT_START_DATE=<same-start-date-from-control-plane>

mkdir -p experiments/consolidation-real-usage-raw/results

scp -i /path/to/key.pem -r \
  ubuntu@<control-plane-public-ip>:/home/ubuntu/descheduler-custom/experiments/consolidation-real-usage-raw/results/http-worker1-overload-${EXPERIMENT_START_DATE} \
  experiments/consolidation-real-usage-raw/results/
```

## Cleanup

```bash
kubectl delete ns http-overload --ignore-not-found
kubectl wait --for=delete ns/http-overload --timeout=120s || true
kubectl uncordon "$NODE_A" || true
kubectl uncordon "$NODE_B" || true
```

## Success criteria

CPU test is successful if:

- `workload-http` Pods run only on `worker-1`;
- `worker-1` actual CPU becomes high or saturated;
- k6 captures worse latency or failure behavior compared to baseline;
- Pods remain `Running` unless the application becomes too slow for probes.

Memory test is successful if one or more runtime effects are captured:

- `worker-1` memory rises significantly;
- k6 captures higher latency;
- k6 captures non-200 responses, especially `429`;
- one or more Pods become `OOMKilled` or `CrashLoopBackOff`;
- node condition becomes `MemoryPressure=True`;
- kubelet eviction appears in events.

Do not overclaim memory results. If the only outcome is `429`, frame it as
application-level memory protection. If the outcome is `OOMKilled`, frame it as
container-level memory failure. Only claim node memory pressure if
`MemoryPressure=True` or eviction events are actually observed.
