# Actual overload behavior test

Dokumen ini merancang test pendahuluan sebelum eksperimen
`usageMode: actual-raw`.

Tujuan test ini bukan menjalankan descheduler, tetapi mengamati behavior
Kubernetes ketika node memiliki actual usage tinggi sementara request-space
masih terlihat schedulable.

Framing yang ingin dibuktikan:

```text
Scheduler admission/fit memakai resources.requests.
Actual CPU/memory yang sedang dipakai node bukan input utama scheduling.
Karena itu, Pod baru tetap bisa Running pada node yang actual usage-nya tinggi
selama request-space masih muat.
```

Ekspektasi behavior:

| Resource | Jika actual demand melebihi kapasitas |
|----------|----------------------------------------|
| CPU | Pod tetap Running, CPU dibagi/throttled, latency naik |
| Memory | Pod bisa Running sebentar, lalu MemoryPressure, eviction, atau OOMKilled |

Test ini dilakukan supaya alasan `usageMode: actual-raw` lebih grounded:

```text
Actual usage bukan sekadar metrik tambahan untuk ranking.
Actual usage memengaruhi risiko runtime setelah placement/eviction.
```

## Cluster target

Gunakan cluster yang sama dengan eksperimen consolidation:

| Role | Node |
|------|------|
| Control plane | `controlplane` |
| Worker target | `worker-1` |
| Worker pembanding | `worker-2` |

Untuk test ini, placement sengaja dikontrol dengan temporary cordon agar efek
runtime mudah diamati pada satu node.

## Prasyarat

Metrics Server harus aktif:

```bash
kubectl get apiservice v1beta1.metrics.k8s.io
kubectl top nodes
kubectl top pods -A
```

Jika `kubectl top` belum tersedia, install Metrics Server dulu. Tanpa Metrics
API, test masih bisa melihat status Pod/OOM/event, tetapi tidak bisa membuktikan
actual usage tinggi secara kuantitatif.

Set variable:

```bash
export NODE_A=worker-1
export NODE_B=worker-2
```

## Namespace

```bash
kubectl delete ns actual-overload --ignore-not-found
kubectl wait --for=delete ns/actual-overload --timeout=120s || true

kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: actual-overload
EOF
```

## Result directory

```bash
export EXPERIMENT_START_DATE=$(date +%Y%m%d-%H%M%S)
export EXPERIMENT_DIR=experiments/consolidation-real-usage-raw
export RESULT_DIR=${EXPERIMENT_DIR}/results/actual-overload-${EXPERIMENT_START_DATE}

mkdir -p \
  "$RESULT_DIR/00-baseline" \
  "$RESULT_DIR/01-cpu" \
  "$RESULT_DIR/02-memory"
```

Baseline:

```bash
kubectl get nodes -o wide 2>&1 \
  | tee "$RESULT_DIR/00-baseline/nodes-wide.txt"
kubectl describe node "$NODE_A" 2>&1 \
  | tee "$RESULT_DIR/00-baseline/node-a-describe.txt"
kubectl top nodes 2>&1 \
  | tee "$RESULT_DIR/00-baseline/top-nodes.txt"
kubectl top pods -A --containers 2>&1 \
  | tee "$RESULT_DIR/00-baseline/top-pods.txt"
```

## Test A: CPU overcommit behavior

### Hypothesis

Jika `worker-1` sudah memiliki actual CPU tinggi, Pod CPU tambahan tetap bisa
dijadwalkan dan Running selama request-space masih muat. Dampaknya adalah CPU
contention, bukan langsung eviction.

### Placement control

```bash
kubectl cordon "$NODE_B"
kubectl uncordon "$NODE_A"
```

### Create CPU hog

`cpu-hog` memakai request kecil tetapi melakukan busy loop tanpa CPU limit.

```bash
kubectl apply -n actual-overload -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cpu-hog
spec:
  replicas: 2
  selector:
    matchLabels:
      app: cpu-hog
  template:
    metadata:
      labels:
        app: cpu-hog
    spec:
      containers:
        - name: cpu
          image: python:3.12-alpine
          command:
            - python
            - -c
            - |
              while True:
                  pass
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              memory: 128Mi
EOF

kubectl rollout status deployment/cpu-hog -n actual-overload --timeout=180s
```

Tunggu Metrics API melihat load:

```bash
for sample in 1 2 3; do
  {
    date -Is
    kubectl get pods -n actual-overload -o wide
    kubectl top nodes
    kubectl top pods -n actual-overload --containers
  } 2>&1 | tee "$RESULT_DIR/01-cpu/cpu-hog-sample-${sample}.txt"
  sleep 15
done
```

Expected:

```text
cpu-hog Running di worker-1
worker-1 actual CPU tinggi
request CPU tetap rendah dibanding actual CPU
```

### Add CPU victim

`cpu-victim` juga request kecil dan CPU-heavy.

```bash
kubectl apply -n actual-overload -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: cpu-victim
  labels:
    app: cpu-victim
spec:
  restartPolicy: Never
  containers:
    - name: cpu
      image: python:3.12-alpine
      command:
        - python
        - -c
        - |
          while True:
              pass
      resources:
        requests:
          cpu: 50m
          memory: 64Mi
        limits:
          memory: 128Mi
EOF

kubectl wait --for=condition=Ready pod/cpu-victim \
  -n actual-overload --timeout=120s || true
```

Capture:

```bash
for sample in 1 2 3; do
  {
    date -Is
    kubectl get pods -n actual-overload -o wide
    kubectl describe pod cpu-victim -n actual-overload
    kubectl top nodes
    kubectl top pods -n actual-overload --containers
    kubectl get events -n actual-overload --sort-by=.lastTimestamp
  } 2>&1 | tee "$RESULT_DIR/01-cpu/cpu-victim-sample-${sample}.txt"
  sleep 15
done
```

Expected observation:

```text
cpu-victim Running
worker-1 CPU near saturation
no kubelet eviction because CPU is compressible
possible slower startup/readiness or lower per-container CPU share
```

Interpretation:

```text
CPU over-demand does not make scheduler reject the Pod.
Runtime effect appears as contention/throttling, not necessarily eviction.
```

## Test B: Memory pressure behavior

Memory test lebih berisiko daripada CPU test. Jalankan hanya setelah CPU test
selesai dan cluster stabil.

### Cleanup CPU test first

```bash
kubectl delete pod cpu-victim -n actual-overload --ignore-not-found
kubectl delete deployment cpu-hog -n actual-overload --ignore-not-found
kubectl wait --for=delete pod -n actual-overload -l app=cpu-hog --timeout=120s || true
```

### Hypothesis

Jika `worker-1` actual memory sudah tinggi, Pod memory tambahan bisa tetap
dijadwalkan karena request-space masih muat. Setelah container mengalokasikan
memory, salah satu outcome berikut dapat terjadi:

```text
Pod Running tetapi node memory makin tinggi
Pod/container OOMKilled karena melewati limit
kubelet menandai MemoryPressure
kubelet melakukan eviction
```

Untuk `t3.micro`, jangan langsung mencoba melewati kapasitas node terlalu jauh.
Mulai dari angka konservatif.

### Create memory hog

`memory-hog` request kecil, lalu mengalokasikan memory dan sleep.

```bash
kubectl apply -n actual-overload -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: memory-hog
spec:
  replicas: 1
  selector:
    matchLabels:
      app: memory-hog
  template:
    metadata:
      labels:
        app: memory-hog
    spec:
      containers:
        - name: memory
          image: python:3.12-alpine
          command:
            - python
            - -c
            - |
              import time
              size = 420 * 1024 * 1024
              data = bytearray(size)
              for i in range(0, len(data), 4096):
                  data[i] = 1
              time.sleep(3600)
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              memory: 520Mi
EOF

kubectl rollout status deployment/memory-hog -n actual-overload --timeout=180s
```

Capture:

```bash
for sample in 1 2 3; do
  {
    date -Is
    kubectl get pods -n actual-overload -o wide
    kubectl top nodes
    kubectl top pods -n actual-overload --containers
    kubectl describe node "$NODE_A" | sed -n '/Conditions:/,/Addresses:/p'
  } 2>&1 | tee "$RESULT_DIR/02-memory/memory-hog-sample-${sample}.txt"
  sleep 15
done
```

### Add memory victim

`memory-victim` request kecil tetapi mengalokasikan memory tambahan.

```bash
kubectl apply -n actual-overload -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: memory-victim
  labels:
    app: memory-victim
spec:
  restartPolicy: Never
  containers:
    - name: memory
      image: python:3.12-alpine
      command:
        - python
        - -c
        - |
          import time
          size = 260 * 1024 * 1024
          data = bytearray(size)
          for i in range(0, len(data), 4096):
              data[i] = 1
          time.sleep(3600)
      resources:
        requests:
          cpu: 50m
          memory: 64Mi
        limits:
          memory: 320Mi
EOF

kubectl wait --for=condition=Ready pod/memory-victim \
  -n actual-overload --timeout=120s || true
```

Capture:

```bash
for sample in 1 2 3 4 5; do
  {
    date -Is
    kubectl get pods -n actual-overload -o wide
    kubectl describe pod memory-victim -n actual-overload
    kubectl top nodes
    kubectl top pods -n actual-overload --containers
    kubectl describe node "$NODE_A" | sed -n '/Conditions:/,/Addresses:/p'
    kubectl get events -n actual-overload --sort-by=.lastTimestamp
  } 2>&1 | tee "$RESULT_DIR/02-memory/memory-victim-sample-${sample}.txt"
  sleep 15
done
```

Expected observation:

```text
memory-victim may run if actual memory still physically available
or memory-victim may become OOMKilled / Evicted
node may show MemoryPressure=True if pressure crosses kubelet thresholds
```

Interpretation:

```text
Unlike CPU, memory over-demand is not safely compressible.
When actual memory approaches capacity, adding another memory-heavy Pod can
produce OOMKill or kubelet eviction even though scheduler admitted it by request.
```

## Pull results

From local machine:

```bash
export EXPERIMENT_START_DATE=<same-start-date-from-control-plane>

mkdir -p experiments/consolidation-real-usage-raw/results

scp -i /path/to/key.pem -r \
  ubuntu@<control-plane-public-ip>:/home/ubuntu/descheduler-custom/experiments/consolidation-real-usage-raw/results/actual-overload-${EXPERIMENT_START_DATE} \
  experiments/consolidation-real-usage-raw/results/
```

## Cleanup

```bash
kubectl delete ns actual-overload --ignore-not-found
kubectl wait --for=delete ns/actual-overload --timeout=120s || true
kubectl uncordon "$NODE_A" || true
kubectl uncordon "$NODE_B" || true
```

## Success criteria

CPU test sukses jika:

- `cpu-victim` dijadwalkan ke `worker-1`;
- `worker-1` actual CPU tinggi dari `kubectl top nodes`;
- Pod tetap `Running`;
- tidak ada eviction karena CPU pressure.

Memory test sukses jika salah satu runtime consequence terlihat:

- node memory usage naik signifikan setelah `memory-victim`;
- `memory-victim` Running tetapi memory headroom menurun;
- `memory-victim` OOMKilled;
- Pod dievict;
- node condition menunjukkan `MemoryPressure=True`.

Tidak semua outcome memory harus terjadi. Yang penting adalah membuktikan bahwa
scheduler admission tidak memakai actual memory sebagai filter utama, sementara
efek runtime baru terlihat setelah container mengalokasikan memory.

## Notes for actual-raw descheduler design

Test ini memberi dasar untuk framing `usageMode: actual-raw`:

```text
Requests tetap dibutuhkan untuk scheduling feasibility.
Actual usage dibutuhkan untuk mengetahui risiko runtime pada node source/target.
```

Untuk CPU, actual usage membantu menghindari relocation ke node yang sedang
contention-heavy.

Untuk memory, actual usage membantu menghindari relocation ke node yang dekat
dengan pressure/OOM risk.

Karena memory pressure dapat membuat run tidak stabil, eksperimen descheduler
utama sebaiknya mulai dari CPU-heavy actual imbalance, lalu memory-heavy case
dibuat sebagai follow-up setelah safety margin jelas.
