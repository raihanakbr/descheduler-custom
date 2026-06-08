# Consolidation actual-raw workload design

Dokumen ini mengusulkan eksperimen untuk membuktikan dan meremediasi mismatch
antara resource accounting kube-scheduler dan penggunaan resource aktual.

Framing awal:

> Terdapat kondisi ketika kube-scheduler menempatkan Pod baru ke node yang
> sedang memiliki penggunaan resource aktual tinggi karena keputusan scheduling
> CPU dan memory secara default menggunakan `resources.requests`, bukan current
> runtime usage.

Framing ini lebih presisi daripada menyatakan Kubernetes selalu menjadwalkan Pod
ke node yang "penuh". Node harus tetap lolos filter request-space, dan kata
`penuh` pada eksperimen ini berarti actual utilization tinggi pada saat
pengukuran, bukan node sudah mencapai hard capacity atau mengalami
`MemoryPressure`.

## Validasi problem statement

Statement tersebut valid dan merupakan konsekuensi desain request-based
scheduling, bukan bug kube-scheduler.

Dokumentasi Kubernetes menjelaskan bahwa:

- scheduler mengecek apakah jumlah request Pod pada node masih berada di bawah
  allocatable/capacity;
- `NodeResourcesFit` menggunakan request Pod untuk filter dan scoring;
- scoring default `NodeResourcesFit` adalah `LeastAllocated`;
- container diizinkan menggunakan resource lebih besar daripada request selama
  resource dan limit masih memungkinkan.

Konsekuensinya:

```text
actual usage tinggi + request accounting masih rendah
-> node tetap feasible
-> scheduler tetap dapat memilih node tersebut
```

Referensi:

- https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/
- https://kubernetes.io/docs/concepts/architecture/nodes/
- https://kubernetes.io/docs/reference/scheduling/config/

Problem ini relevan ketika:

- request terlalu rendah dibanding runtime usage;
- workload memiliki burst CPU atau memory;
- scheduler memakai bin-packing berdasarkan request;
- penggunaan aktual berubah setelah Pod sudah dijadwalkan;
- metrics runtime tidak menjadi input scheduler profile.

Eksperimen ini tidak mengklaim bahwa request-based scheduling salah. Requests
adalah kontrak deklaratif dan memberi kestabilan terhadap spike. Eksperimen
hanya menunjukkan blind spot ketika requests tidak merepresentasikan actual
usage.

## Research question

```text
RQ1:
Apakah request-based kube-scheduler dapat menempatkan Pod baru ke worker yang
actual CPU usage-nya lebih tinggi daripada worker lain?

RQ2:
Apakah ResourceDefragmentation usageMode=actual-raw dapat membaca mismatch itu,
memilih node actual-hot sebagai drain candidate, dan melakukan tepat satu
eviction terhadap Pod eksperimen?
```

## Hipotesis

```text
H1:
consolidation-scheduler memilih worker-1 karena request-space worker-1 memberi
MostAllocated + BalancedAllocation score lebih tinggi.

H2:
Pada saat yang sama, Metrics API menunjukkan worker-1 memiliki actual CPU usage
lebih tinggi dan resource balance lebih buruk daripada worker-2.

H3:
ResourceDefragmentation usageMode=actual-raw menandai worker-1 sebagai drain
candidate dan mengevict misplaced-probe.
```

## Scope eksperimen

Eksperimen ini menguji:

1. Keputusan placement request-based.
2. Perbedaan request utilization dan actual utilization.
3. Pembacaan raw Metrics API oleh descheduler.
4. Satu keputusan eviction actual-raw.

Eksperimen ini tidak menguji:

- long-term smoothing atau EWMA;
- HPA/VPA;
- memory pressure dan kubelet eviction;
- performa aplikasi setelah relocation;
- stabilitas terhadap transient spike;
- automatic rescheduling ke node lain.

`misplaced-probe` dibuat sebagai standalone Pod. Setelah dievict, Pod tidak
dibuat ulang. Ini disengaja agar eksperimen mengisolasi keputusan actual-raw dan
tidak menghasilkan loop ketika scheduler request-based kembali memilih node
yang sama.

Standalone Pod secara default dilindungi `DefaultEvictor` karena tidak memiliki
owner reference. Probe diberi annotation:

```text
descheduler.alpha.kubernetes.io/evict: "true"
```

Annotation tersebut hanya membuat Pod lolos protection filter. Annotation tidak
menentukan node asal, drain candidate, TOPSIS ranking, atau target prediction.

Eksperimen lanjutan dapat memakai controller dan scheduler actual-aware untuk
menguji relocation end-to-end.

## Cluster eksperimen

Cluster:

| Role | Instance | Jumlah | Catatan |
|------|----------|--------|---------|
| Control plane | `t3.medium` | 1 | Control-plane dan one-shot descheduler Job |
| Worker | `t3.micro` | 2 | Node yang dibandingkan |
| Load generator | `t3.micro` atau lebih besar | 1 | Di luar cluster |

Node:

| Alias | Node |
|-------|------|
| Node A, actual-hot | `worker-1` |
| Node B, balanced | `worker-2` |
| Control plane | `controlplane` |

Load generator tidak boleh join cluster agar CPU dan memory k6/curl tidak masuk
ke Metrics API worker.

## Prasyarat software

### Branch dan image descheduler

Gunakan source yang mengandung consolidation fields dan actual usage support:

```text
branch: feat/consolidation-focus
known commit: f62cf5e06
```

Image descheduler harus dibuild dari commit tersebut dan hanya perlu membawa
binary `/bin/descheduler`.

Contoh image:

```text
docker.io/matthewhjt/descheduler-custom:resource-defrag-f62cf5e06
```

### Metrics Server

`usageMode: actual-raw` membutuhkan Metrics API:

```text
metrics.k8s.io/v1beta1
```

Verifikasi:

```bash
kubectl get apiservice v1beta1.metrics.k8s.io
kubectl top nodes
kubectl top pods -A
```

Jika Metrics API belum ada:

```bash
kubectl apply -f \
  https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

Pada kubeadm learning cluster dengan kubelet serving certificate yang tidak
valid untuk private IP, lab dapat membutuhkan:

```bash
kubectl patch deployment metrics-server -n kube-system --type=json \
  -p='[
    {
      "op": "add",
      "path": "/spec/template/spec/containers/0/args/-",
      "value": "--kubelet-insecure-tls"
    }
  ]'
```

`--kubelet-insecure-tls` hanya untuk lab. Jangan jadikan konfigurasi production.

Tunggu Metrics API:

```bash
kubectl rollout status deployment/metrics-server -n kube-system --timeout=180s

until kubectl top nodes; do
  sleep 5
done
```

### Scheduler profile

Gunakan profile yang sama dengan eksperimen consolidation:

```yaml
apiVersion: kubescheduler.config.k8s.io/v1
kind: KubeSchedulerConfiguration
clientConnection:
  kubeconfig: /etc/kubernetes/scheduler.conf
leaderElection:
  leaderElect: true
profiles:
  - schedulerName: default-scheduler
  - schedulerName: consolidation-scheduler
    plugins:
      score:
        disabled:
          - name: NodeResourcesFit
          - name: NodeResourcesBalancedAllocation
        enabled:
          - name: NodeResourcesFit
            weight: 1
          - name: NodeResourcesBalancedAllocation
            weight: 1
    pluginConfig:
      - name: NodeResourcesFit
        args:
          scoringStrategy:
            type: MostAllocated
            resources:
              - name: cpu
                weight: 1
              - name: memory
                weight: 1
```

Profile ini tetap request-based. `MostAllocated` dan
`NodeResourcesBalancedAllocation` tidak membaca Metrics API.

## Workload model

Eksperimen memakai dua anchor dan satu probe:

| Workload | Placement awal | Requests | Actual target | Evictable |
|----------|-----------------|----------|---------------|-----------|
| `hot-anchor` | `worker-1` | `700m / 280Mi` | CPU tinggi, memory rendah | Tidak |
| `balanced-anchor` | `worker-2` | `400m / 160Mi` | CPU dan memory medium-balanced | Tidak |
| `misplaced-probe` | Dipilih scheduler | `200m / 100Mi` | Hampir idle | Ya |

Anchor berada di namespace `load-source`. Probe berada di namespace
`test-app`. Policy descheduler hanya mengizinkan eviction dari `test-app`.

Dengan worker budget pendekatan:

```text
CPU:    2000m
Memory: 800Mi
```

request state sebelum probe:

```text
worker-1:
hot-anchor = 700m / 280Mi
~= 35% CPU / 35% memory

worker-2:
balanced-anchor = 400m / 160Mi
~= 20% CPU / 20% memory
```

Projected request state jika probe masuk:

```text
worker-1 + probe:
900m / 380Mi
~= 45% CPU / 47.5% memory

worker-2 + probe:
600m / 260Mi
~= 30% CPU / 32.5% memory
```

`MostAllocated + BalancedAllocation` diharapkan memilih `worker-1` karena
worker-1 menjadi bin yang lebih padat dan tetap balanced dalam request-space.

Namun actual target sebelum probe:

```text
worker-1:
CPU    >= 70%
memory 15% - 30%

worker-2:
CPU    25% - 45%
memory 25% - 45%
```

Jadi scheduler memilih node yang request-score-nya lebih baik walaupun current
actual CPU usage-nya lebih tinggi.

## Calibration gate

Actual usage tidak deterministik seperti requests. Jangan membuat probe sebelum
gate berikut terpenuhi selama minimal 3 snapshot berturut-turut:

```text
worker-1 actual CPU >= 70%
worker-1 actual CPU >= worker-2 actual CPU + 25 percentage points
worker-1 request CPU <= 45% sebelum probe
worker-1 actual bin score < 0.50
worker-2 min(actual CPU%, actual memory%) >=
  worker-1 min(actual CPU%, actual memory%)
```

Gate terakhir mengikuti `pack upward` guard plugin: target harus memiliki
least-used actual dimension minimal sama dengan source.

Actual bin score:

```text
density  = (actualCPUFraction + actualMemoryFraction) / 2
balance  = 1 - abs(actualCPUFraction - actualMemoryFraction)
binScore = (density + balance) / 2
```

Hitung dari:

```bash
kubectl top nodes
kubectl top pods -n load-source --containers
kubectl describe node worker-1
kubectl describe node worker-2
```

Jika gate tidak terpenuhi:

- naikkan load CPU `hot-anchor`;
- naikkan load balanced pada `balanced-anchor`;
- kurangi load memory `hot-anchor`;
- jangan mengubah threshold hanya untuk memaksa hasil.

## Expected actual-raw decision

Policy:

```text
usageMode               = actual-raw
consolidationThreshold  = 0.50
consolidationTarget     = 0.90
maxEvictions            = 1
```

Threshold `0.50` digunakan karena Node A dapat memiliki average actual usage di
atas `0.40`, tetapi tetap merupakan bin buruk akibat CPU-memory skew. Candidate
trigger adalah:

```text
avg actual utilization < threshold
OR
actual bin score < threshold
```

`worker-2` mungkin juga berada di bawah average threshold, tetapi tidak memiliki
Pod dari namespace `test-app`, sehingga tidak memiliki candidate yang boleh
dievict.

Expected log:

```text
usageSource="actual-raw"
node="worker-1" actual CPU tinggi dan binScore < 0.50
Node is a drain candidate node="worker-1"
pod="test-app/misplaced-probe"
predictedTarget="worker-2"
Evicted pod
totalEvicted=1
```

## Workload image

Gunakan HTTP load image yang sudah ada:

```text
experiments/e3-k6-http-workload/cmd/workload-http
```

Build dan push dari build VM:

```bash
export WORKLOAD_IMAGE=docker.io/matthewhjt/workload-http:actual-raw-v1

docker build \
  -t "$WORKLOAD_IMAGE" \
  experiments/e3-k6-http-workload/cmd/workload-http

docker push "$WORKLOAD_IMAGE"
```

Image menerima:

```text
/work?cpu_ms=<duration>&mem_mb=<allocation>&hold_ms=<duration>
```

## YAML: namespaces and anchors

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: load-source
---
apiVersion: v1
kind: Namespace
metadata:
  name: test-app
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hot-anchor
  namespace: load-source
spec:
  replicas: 1
  selector:
    matchLabels:
      app: hot-anchor
  template:
    metadata:
      labels:
        app: hot-anchor
    spec:
      schedulerName: consolidation-scheduler
      containers:
        - name: workload
          image: docker.io/matthewhjt/workload-http:actual-raw-v1
          ports:
            - containerPort: 8080
          env:
            - name: MAX_CPU_MS
              value: "2000"
            - name: MAX_MEM_MB
              value: "32"
            - name: MAX_HOLD_MS
              value: "2000"
            - name: MAX_INFLIGHT
              value: "32"
          resources:
            requests:
              cpu: 700m
              memory: 280Mi
            limits:
              memory: 512Mi
---
apiVersion: v1
kind: Service
metadata:
  name: hot-anchor
  namespace: load-source
spec:
  type: NodePort
  selector:
    app: hot-anchor
  ports:
    - name: http
      port: 80
      targetPort: 8080
      nodePort: 30081
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: balanced-anchor
  namespace: load-source
spec:
  replicas: 1
  selector:
    matchLabels:
      app: balanced-anchor
  template:
    metadata:
      labels:
        app: balanced-anchor
    spec:
      schedulerName: consolidation-scheduler
      containers:
        - name: workload
          image: docker.io/matthewhjt/workload-http:actual-raw-v1
          ports:
            - containerPort: 8080
          env:
            - name: MAX_CPU_MS
              value: "1000"
            - name: MAX_MEM_MB
              value: "96"
            - name: MAX_HOLD_MS
              value: "3000"
            - name: MAX_INFLIGHT
              value: "16"
          resources:
            requests:
              cpu: 400m
              memory: 160Mi
            limits:
              memory: 512Mi
---
apiVersion: v1
kind: Service
metadata:
  name: balanced-anchor
  namespace: load-source
spec:
  type: NodePort
  selector:
    app: balanced-anchor
  ports:
    - name: http
      port: 80
      targetPort: 8080
      nodePort: 30082
```

CPU limit sengaja tidak ditetapkan agar `hot-anchor` dapat menggunakan CPU di
atas request. Memory limit tetap digunakan untuk mengurangi risiko OOM node.

## YAML: misplaced probe

Probe dibuat sebagai standalone Pod:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: misplaced-probe
  namespace: test-app
  labels:
    app: misplaced-probe
  annotations:
    descheduler.alpha.kubernetes.io/evict: "true"
spec:
  schedulerName: consolidation-scheduler
  restartPolicy: Never
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
      resources:
        requests:
          cpu: 200m
          memory: 100Mi
```

## YAML: actual-raw policy

```yaml
apiVersion: descheduler/v1alpha2
kind: DeschedulerPolicy
metricsProviders:
  - source: KubernetesMetrics
profiles:
  - name: default
    pluginConfig:
      - name: ResourceDefragmentation
        args:
          namespaces:
            include:
              - test-app
          usageMode: actual-raw
          consolidationThreshold: 0.50
          consolidationTarget: 0.90
          maxEvictions: 1
    plugins:
      balance:
        enabled:
          - ResourceDefragmentation
```

`metricsProviders` wajib. Tanpa field tersebut, metrics collector tidak
tersedia dan plugin fallback ke requests.

## External load generator

Jalankan dari VM di luar cluster. Gunakan private IP salah satu worker karena
NodePort tersedia pada seluruh node dan Service akan meneruskan traffic ke Pod
anchor.

Set endpoint:

```bash
export NODE_IP=<worker-private-ip-reachable-from-load-generator>
```

Hot CPU loop:

```bash
for worker in $(seq 1 12); do
  while true; do
    curl -fsS \
      "http://${NODE_IP}:30081/work?cpu_ms=900&mem_mb=2&hold_ms=0" \
      >/dev/null || true
  done &
done
```

Balanced CPU-memory loop:

```bash
for worker in $(seq 1 4); do
  while true; do
    curl -fsS \
      "http://${NODE_IP}:30082/work?cpu_ms=180&mem_mb=48&hold_ms=900" \
      >/dev/null || true
  done &
done
```

Simpan PID jika load perlu dihentikan:

```bash
jobs -p > actual-raw-load.pids
```

Stop:

```bash
xargs -r kill < actual-raw-load.pids
```

Parameter di atas adalah starting point. Gunakan calibration gate, bukan asumsi
bahwa nilai tersebut selalu menghasilkan utilization yang sama.

## Walkthrough

### 1. Environment

Pada control plane, dari root repository:

```bash
export NODE_A=worker-1
export NODE_B=worker-2
export DESCHEDULER_IMAGE=docker.io/matthewhjt/descheduler-custom:resource-defrag-f62cf5e06

test -n "$NODE_A"
test -n "$NODE_B"
test -n "$DESCHEDULER_IMAGE"
```

### 2. Reset

```bash
kubectl delete job descheduler-job -n kube-system --ignore-not-found
kubectl delete ns test-app load-source --ignore-not-found
kubectl wait --for=delete ns/test-app --timeout=120s || true
kubectl wait --for=delete ns/load-source --timeout=120s || true
kubectl uncordon "$NODE_A" || true
kubectl uncordon "$NODE_B" || true
```

### 3. Create namespaces

```bash
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: load-source
---
apiVersion: v1
kind: Namespace
metadata:
  name: test-app
EOF
```

### 4. Place hot-anchor on worker-1

```bash
kubectl cordon "$NODE_B"
kubectl uncordon "$NODE_A"

kubectl apply -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hot-anchor
  namespace: load-source
spec:
  replicas: 1
  selector:
    matchLabels:
      app: hot-anchor
  template:
    metadata:
      labels:
        app: hot-anchor
    spec:
      schedulerName: consolidation-scheduler
      containers:
        - name: workload
          image: docker.io/matthewhjt/workload-http:actual-raw-v1
          ports:
            - containerPort: 8080
          env:
            - name: MAX_CPU_MS
              value: "2000"
            - name: MAX_MEM_MB
              value: "32"
            - name: MAX_HOLD_MS
              value: "2000"
            - name: MAX_INFLIGHT
              value: "32"
          resources:
            requests:
              cpu: 700m
              memory: 280Mi
            limits:
              memory: 512Mi
---
apiVersion: v1
kind: Service
metadata:
  name: hot-anchor
  namespace: load-source
spec:
  type: NodePort
  selector:
    app: hot-anchor
  ports:
    - port: 80
      targetPort: 8080
      nodePort: 30081
EOF

kubectl rollout status deployment/hot-anchor -n load-source --timeout=180s
kubectl get pods -n load-source -o wide
```

Expected:

```text
hot-anchor -> worker-1
```

### 5. Place balanced-anchor on worker-2

```bash
kubectl cordon "$NODE_A"
kubectl uncordon "$NODE_B"

kubectl apply -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: balanced-anchor
  namespace: load-source
spec:
  replicas: 1
  selector:
    matchLabels:
      app: balanced-anchor
  template:
    metadata:
      labels:
        app: balanced-anchor
    spec:
      schedulerName: consolidation-scheduler
      containers:
        - name: workload
          image: docker.io/matthewhjt/workload-http:actual-raw-v1
          ports:
            - containerPort: 8080
          env:
            - name: MAX_CPU_MS
              value: "1000"
            - name: MAX_MEM_MB
              value: "96"
            - name: MAX_HOLD_MS
              value: "3000"
            - name: MAX_INFLIGHT
              value: "16"
          resources:
            requests:
              cpu: 400m
              memory: 160Mi
            limits:
              memory: 512Mi
---
apiVersion: v1
kind: Service
metadata:
  name: balanced-anchor
  namespace: load-source
spec:
  type: NodePort
  selector:
    app: balanced-anchor
  ports:
    - port: 80
      targetPort: 8080
      nodePort: 30082
EOF

kubectl rollout status deployment/balanced-anchor -n load-source --timeout=180s
kubectl get pods -n load-source -o wide
```

Expected:

```text
balanced-anchor -> worker-2
```

Restore scheduling:

```bash
kubectl uncordon "$NODE_A"
kubectl uncordon "$NODE_B"
```

### 6. Start and calibrate actual load

Start external load loops, then collect at least 3 samples:

```bash
mkdir -p /tmp/consolidation-real-usage-raw-calibration

for sample in 1 2 3; do
  {
    date -Is
    kubectl top nodes
    kubectl top pods -n load-source --containers
  } 2>&1 | tee "/tmp/consolidation-real-usage-raw-calibration/sample-${sample}.txt"
  sleep 15
done
```

Do not continue until calibration gate is met.

### 7. Create experiment ID and capture pre-placement state

```bash
export EXPERIMENT_START_DATE=$(date +%Y%m%d-%H%M%S)
export EXPERIMENT_DIR=experiments/consolidation-real-usage-raw
export RESULT_DIR=${EXPERIMENT_DIR}/results/consolidation-real-usage-raw-${EXPERIMENT_START_DATE}

mkdir -p \
  "$RESULT_DIR/00-pre-placement" \
  "$RESULT_DIR/01-after-placement" \
  "$RESULT_DIR/02-descheduler" \
  "$RESULT_DIR/03-after"

kubectl get pods -A -o wide 2>&1 \
  | tee "$RESULT_DIR/00-pre-placement/pods-wide.txt"
kubectl top nodes 2>&1 \
  | tee "$RESULT_DIR/00-pre-placement/top-nodes.txt"
kubectl top pods -n load-source --containers 2>&1 \
  | tee "$RESULT_DIR/00-pre-placement/top-load-source.txt"
kubectl describe node "$NODE_A" 2>&1 \
  | tee "$RESULT_DIR/00-pre-placement/node-a-describe.txt"
kubectl describe node "$NODE_B" 2>&1 \
  | tee "$RESULT_DIR/00-pre-placement/node-b-describe.txt"
```

### 8. Demonstrate the scheduler blind spot

Create the probe without cordon, affinity, or node selector:

```bash
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: misplaced-probe
  namespace: test-app
  labels:
    app: misplaced-probe
  annotations:
    descheduler.alpha.kubernetes.io/evict: "true"
spec:
  schedulerName: consolidation-scheduler
  restartPolicy: Never
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
      resources:
        requests:
          cpu: 200m
          memory: 100Mi
EOF

kubectl wait --for=condition=Ready pod/misplaced-probe \
  -n test-app --timeout=120s

kubectl get pod misplaced-probe -n test-app -o wide 2>&1 \
  | tee "$RESULT_DIR/01-after-placement/misplaced-probe-wide.txt"
kubectl describe pod misplaced-probe -n test-app 2>&1 \
  | tee "$RESULT_DIR/01-after-placement/misplaced-probe-describe.txt"
kubectl top nodes 2>&1 \
  | tee "$RESULT_DIR/01-after-placement/top-nodes.txt"
kubectl top pods -A --containers 2>&1 \
  | tee "$RESULT_DIR/01-after-placement/top-pods.txt"
kubectl get events -n test-app --sort-by=.lastTimestamp 2>&1 \
  | tee "$RESULT_DIR/01-after-placement/events.txt"
```

Expected proof:

```text
misplaced-probe -> worker-1
worker-1 actual CPU > worker-2 actual CPU
```

Jika probe tidak masuk `worker-1`, run tidak membuktikan RQ1 dan harus diulang
setelah request/load calibration diperbaiki. Jangan memaksa probe dengan
`nodeName`.

### 9. Apply actual-raw policy and RBAC

Apply RBAC dari repository:

```bash
kubectl apply -n kube-system -f kubernetes/base/rbac.yaml
```

Apply policy:

```bash
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: descheduler-policy-configmap
  namespace: kube-system
data:
  policy.yaml: |
    apiVersion: descheduler/v1alpha2
    kind: DeschedulerPolicy
    metricsProviders:
      - source: KubernetesMetrics
    profiles:
      - name: default
        pluginConfig:
          - name: ResourceDefragmentation
            args:
              namespaces:
                include:
                  - test-app
              usageMode: actual-raw
              consolidationThreshold: 0.50
              consolidationTarget: 0.90
              maxEvictions: 1
        plugins:
          balance:
            enabled:
              - ResourceDefragmentation
EOF
```

### 10. Run one-shot descheduler

Descheduler harus berjalan di control-plane agar request dan actual usage
descheduler tidak mengubah worker yang sedang diukur.

```bash
kubectl delete job descheduler-job -n kube-system --ignore-not-found

kubectl apply -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: descheduler-job
  namespace: kube-system
  labels:
    app: descheduler
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app: descheduler
    spec:
      nodeSelector:
        kubernetes.io/hostname: controlplane
      tolerations:
        - key: node-role.kubernetes.io/control-plane
          operator: Exists
          effect: NoSchedule
      restartPolicy: Never
      serviceAccountName: descheduler-sa
      containers:
        - name: descheduler
          image: ${DESCHEDULER_IMAGE}
          imagePullPolicy: Always
          command:
            - /bin/descheduler
          args:
            - --policy-config-file
            - /policy-dir/policy.yaml
            - --v
            - "4"
          volumeMounts:
            - name: policy-volume
              mountPath: /policy-dir
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
      volumes:
        - name: policy-volume
          configMap:
            name: descheduler-policy-configmap
EOF

until kubectl get pod -n kube-system -l app=descheduler \
  -o name | grep -q .; do
  sleep 1
done

DESCHEDULER_POD=$(
  kubectl get pod -n kube-system -l app=descheduler \
    -o jsonpath='{.items[0].metadata.name}'
)

kubectl get job,pod -n kube-system -l app=descheduler -o wide 2>&1 \
  | tee "$RESULT_DIR/02-descheduler/job-pod-start.txt"
kubectl describe pod "$DESCHEDULER_POD" -n kube-system 2>&1 \
  | tee "$RESULT_DIR/02-descheduler/pod-describe.txt"
kubectl logs -f "$DESCHEDULER_POD" -n kube-system 2>&1 \
  | tee "$RESULT_DIR/02-descheduler/descheduler.log" || true
kubectl wait --for=condition=complete job/descheduler-job \
  -n kube-system --timeout=120s 2>&1 \
  | tee "$RESULT_DIR/02-descheduler/wait.txt" || true
```

### 11. Capture after state

```bash
kubectl get pods -A -o wide 2>&1 \
  | tee "$RESULT_DIR/03-after/pods-wide.txt"
kubectl get pod misplaced-probe -n test-app -o wide 2>&1 \
  | tee "$RESULT_DIR/03-after/misplaced-probe.txt" || true
kubectl top nodes 2>&1 \
  | tee "$RESULT_DIR/03-after/top-nodes.txt"
kubectl top pods -A --containers 2>&1 \
  | tee "$RESULT_DIR/03-after/top-pods.txt"
kubectl get events -n test-app --sort-by=.lastTimestamp 2>&1 \
  | tee "$RESULT_DIR/03-after/events.txt"
kubectl describe node "$NODE_A" 2>&1 \
  | tee "$RESULT_DIR/03-after/node-a-describe.txt"
kubectl describe node "$NODE_B" 2>&1 \
  | tee "$RESULT_DIR/03-after/node-b-describe.txt"
kubectl logs job/descheduler-job -n kube-system 2>&1 \
  | tee "$RESULT_DIR/02-descheduler/descheduler-final.log"
```

Expected:

```text
misplaced-probe NotFound
totalEvicted=1
descheduler Job Complete di controlplane
```

### 12. Pull results

Jalankan dari local repository:

```bash
export EXPERIMENT_START_DATE=<same-start-date-from-control-plane>

mkdir -p experiments/consolidation-real-usage-raw/results

scp -i /path/to/key.pem -r \
  ubuntu@<control-plane-public-ip>:/home/ubuntu/descheduler-custom/experiments/consolidation-real-usage-raw/results/consolidation-real-usage-raw-${EXPERIMENT_START_DATE} \
  experiments/consolidation-real-usage-raw/results/
```

## Success criteria

Run sukses hanya jika seluruh kondisi berikut terpenuhi:

1. Metrics API tersedia dan `kubectl top` menghasilkan angka.
2. `hot-anchor` berada di `worker-1`.
3. `balanced-anchor` berada di `worker-2`.
4. Actual CPU `worker-1` lebih tinggi minimal 25 percentage points dari
   `worker-2` sebelum probe.
5. Probe dibuat tanpa placement constraint.
6. Scheduler menempatkan `misplaced-probe` ke `worker-1`.
7. Log descheduler menyatakan `usageSource="actual-raw"`.
8. `worker-1` menjadi drain candidate.
9. Pod yang dievict adalah `test-app/misplaced-probe`.
10. `totalEvicted=1`.
11. Job descheduler berjalan di `controlplane`.

## Failure and invalid-run criteria

Run tidak valid jika:

- Metrics API unavailable;
- log menunjukkan fallback ke requests;
- actual load belum stabil;
- probe masuk `worker-2`;
- probe dipaksa ke worker-1 dengan affinity atau `nodeName`;
- anchor ikut menjadi kandidat eviction;
- descheduler berjalan di worker;
- eviction lebih dari satu;
- node mengalami `MemoryPressure`, OOMKill, atau CPU load generator gagal;
- actual CPU gap tidak mencapai calibration gate.

## Evidence minimum

Evidence yang wajib disimpan:

```text
00-pre-placement/
  pods-wide.txt
  top-nodes.txt
  top-load-source.txt
  node-a-describe.txt
  node-b-describe.txt

01-after-placement/
  misplaced-probe-wide.txt
  misplaced-probe-describe.txt
  top-nodes.txt
  top-pods.txt
  events.txt

02-descheduler/
  job-pod-start.txt
  pod-describe.txt
  descheduler.log
  descheduler-final.log
  wait.txt

03-after/
  pods-wide.txt
  misplaced-probe.txt
  top-nodes.txt
  top-pods.txt
  events.txt
  node-a-describe.txt
  node-b-describe.txt
```

## Analysis outline

Analisis hasil harus membandingkan:

| Dimension | worker-1 | worker-2 |
|-----------|----------|----------|
| Request CPU sebelum probe | dari `describe node` | dari `describe node` |
| Request memory sebelum probe | dari `describe node` | dari `describe node` |
| Actual CPU sebelum probe | dari Metrics API | dari Metrics API |
| Actual memory sebelum probe | dari Metrics API | dari Metrics API |
| Scheduler placement | selected atau tidak | selected atau tidak |
| Actual-raw candidate | candidate atau tidak | candidate atau tidak |

Statement akhir yang boleh dibuat jika success criteria terpenuhi:

> Pada run ini, request-based kube-scheduler menempatkan Pod baru ke worker-1
> karena request-space scoring, walaupun Metrics API menunjukkan worker-1
> memiliki actual CPU usage lebih tinggi daripada worker-2. Descheduler
> `ResourceDefragmentation` dengan `usageMode: actual-raw` membaca runtime usage
> tersebut dan mengevict Pod eksperimen dalam satu one-shot run.

Jangan menggeneralisasi satu run menjadi bukti bahwa scheduler selalu membuat
placement buruk. Untuk hasil tesis yang lebih kuat, ulangi minimal 5 kali dan
laporkan:

- placement success rate ke actual-hot node;
- actual CPU gap saat placement;
- eviction success rate;
- false candidate/fallback count;
- waktu dari probe creation sampai eviction.

## Threats to validity

### Metrics sampling delay

Metrics Server bukan monitoring system presisi tinggi. Snapshot dapat tertinggal
dari load aktual. Gunakan beberapa snapshot stabil sebelum placement dan
descheduler.

### Burstable EC2 CPU

`t3.micro` memakai CPU credits. Credit depletion dapat mengubah throughput.
Rekam:

```bash
kubectl top nodes
```

dan CloudWatch `CPUCreditBalance` jika tersedia.

### Node system overhead

NodeMetrics mencakup whole-node runtime usage, termasuk overhead sistem. Gunakan
worker dengan setup homogen dan simpan baseline sebelum workload.

### Scheduler score plugins

Placement dipengaruhi seluruh score plugin aktif, bukan hanya
`NodeResourcesFit`. Profile eksperimen membatasi resource scoring agar alasan
placement lebih mudah dipertanggungjawabkan.

### Raw metric sensitivity

`actual-raw` sengaja sensitif terhadap spike. Hasil ini membuktikan kemampuan
membaca current runtime state, bukan kestabilan keputusan. Stabilitas diuji
terpisah dengan EWMA.

## Cleanup

Pada control plane:

```bash
kubectl delete job descheduler-job -n kube-system --ignore-not-found
kubectl delete configmap descheduler-policy-configmap -n kube-system --ignore-not-found
kubectl delete ns test-app load-source --ignore-not-found
kubectl uncordon worker-1 || true
kubectl uncordon worker-2 || true
```

Pada load-generator VM:

```bash
xargs -r kill < actual-raw-load.pids
```
