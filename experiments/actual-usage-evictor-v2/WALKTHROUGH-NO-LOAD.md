# Walkthrough: ActualUsageEvictor v2 - Without Load Test

## Tujuan

Test scenario tanpa traffic (no k6 load) untuk memverifikasi bahwa R0 dan R1 berperilaku identik ketika api pod idle.

## Expected Behavior (No Load)

**R0 (ResourceDefragmentationC2 only):**
1. C2 mengevaluasi worker-6 (source node, mem-skewed)
2. api pod (50m/240Mi) terpilih pertama (memory lebih besar = stranding relief lebih baik)
3. api pod di-evict
4. Pod pengganti dijadwalkan di node CPU-skewed (worker-1/2/3)
5. Tidak ada disruption traffic (api pod idle, tidak ada k6 load)
6. Stranding membaik (defrag sukses)

**R1 (ResourceDefragmentationC2 + ActualUsageEvictor):**
1. C2 mengevaluasi worker-6 (source node, mem-skewed)
2. api pod (50m/240Mi) terpilih pertama
3. ActualUsageEvictor check: cpuRatio ~0 (idle, tidak ada traffic) → **ALLOWED**
4. api pod di-evict
5. Pod pengganti dijadwalkan di node CPU-skewed (worker-1/2/3)
6. Tidak ada disruption traffic (api pod idle, tidak ada k6 load)
7. Stranding membaik (defrag sukses)

**Key Point:** Tanpa load, ActualUsageEvictor tidak memblokir karena actual usage di bawah threshold 0.80.

## Prerequisites

1. Kubernetes cluster dengan 6 worker nodes
2. Metrics Server terinstall dan berjalan
3. kubectl terkonfigurasi
4. Descheduler image tersedia: `DESCHEDULER_IMAGE` environment variable
5. Workload image: `docker.io/matthewhjt/workload-http:actual-usage-v1`

## Step-by-Step Execution

### 1. Setup Environment

```bash
cd descheduler-custom/experiments/actual-usage-evictor-v2

export WORKLOAD_IMAGE="docker.io/matthewhjt/workload-http:actual-usage-v1"
export DESCHEDULER_IMAGE="docker.io/matthewhjt/descheduler-custom:actual-usage-v1"
```

### 2. Cleanup Previous State

```bash
./scripts/cleanup.sh
```

### 3. Deploy Layout

```bash
./scripts/setup-layout.sh
```

**Verifikasi:**
```bash
# Check pod distribution
kubectl -n actual-usage-exp get pods -o wide --sort-by=.spec.nodeName

# Expected output:
# - worker-1,2,3: masing-masing 2 cpu pods (750m/20Mi)
# - worker-4,5: masing-masing 2 mem pods (50m/240Mi)
# - worker-6: 1 api pod (50m/240Mi) + 1 mem pod (50m/200Mi)

# Verify api pod location
kubectl -n actual-usage-exp get pod -l app=workload-api -o wide

# Check node allocatable
kubectl get nodes -o custom-columns='NAME:.metadata.name,CPU:.status.allocatable.cpu,MEM:.status.allocatable.memory'
```

### 4. Verify Metrics Server

```bash
# Wait for metrics to be available (baru deploy, perlu ~30s)
sleep 30

# Check api pod metrics
kubectl -n actual-usage-exp top pod -l app=workload-api

# Expected: CPU usage sangat rendah (< 10m), karena tidak ada traffic
# Example output:
# NAME                            CPU(cores)   MEMORY(%)
# workload-api-xxxxxxxxxx-xxxxx   2m           12%
```

**Calculate CPU ratio:**
```bash
# CPU request = 50m
# Jika actual = 2m, maka ratio = 2/50 = 0.04 (jauh di bawah threshold 0.80)
```

### 5. Take Before Snapshot

```bash
OUTPUT_DIR="results/walkthrough/no-load"
mkdir -p "$OUTPUT_DIR"

./scripts/snapshot.sh before "$OUTPUT_DIR"

# Manual snapshot untuk analisis
kubectl get nodes -o json > "$OUTPUT_DIR/nodes-before.json"
kubectl get pods -A -o json > "$OUTPUT_DIR/pods-before.json"
```

### 6. Run Descheduler - R0 (Control, No ActualUsageEvictor)

```bash
# Apply R0 policy
./scripts/run-descheduler.sh policies/r0-rdc2.yaml "$OUTPUT_DIR"

# Check descheduler logs
cat "$OUTPUT_DIR/descheduler.log" | grep -E "(Evicting pod|pod selected|Processing node)"
```

**Expected in logs:**
- C2 identifies worker-6 as source node
- api pod selected (karena memory request lebih besar: 240Mi vs 200Mi)
- api pod evicted
- No blocking from ActualUsageEvictor (karena tidak ada plugin ini di R0)

**Verifikasi setelah R0:**
```bash
# Check which pod was evicted
kubectl -n actual-usage-exp get pods -o wide

# Check events
kubectl get events -n actual-usage-exp --sort-by='.lastTimestamp' | tail -20

# Expected: api pod terminated, new pod scheduled on worker-1/2/3
kubectl -n actual-usage-exp get pod -l app=workload-api -o wide
# Should show api pod now on different node (worker-1, worker-2, or worker-3)
```

### 7. Cleanup and Redeploy for R1

```bash
# Full cleanup
./scripts/cleanup.sh

# Redeploy layout
./scripts/setup-layout.sh

# Verify fresh state
kubectl -n actual-usage-exp get pods -o wide --sort-by=.spec.nodeName
```

### 8. Run Descheduler - R1 (Treatment, With ActualUsageEvictor)

```bash
OUTPUT_DIR_R1="results/walkthrough/no-load-r1"
mkdir -p "$OUTPUT_DIR_R1"

# Take before snapshot
./scripts/snapshot.sh before "$OUTPUT_DIR_R1"
kubectl get nodes -o json > "$OUTPUT_DIR_R1/nodes-before.json"
kubectl get pods -A -o json > "$OUTPUT_DIR_R1/pods-before.json"

# Apply R1 policy
./scripts/run-descheduler.sh policies/r1-rdc2-actual.yaml "$OUTPUT_DIR_R1"

# Check descheduler logs
cat "$OUTPUT_DIR_R1/descheduler.log" | grep -E "(Evicting pod|pod selected|Processing node|ActualUsageEvictor|ALLOWED|BLOCKED)"
```

**Expected in logs:**
- C2 identifies worker-6 as source node
- api pod selected first
- **ActualUsageEvictor checks api pod: cpuRatio ~0.04 → ALLOWED**
- api pod evicted
- No blocking terjadi

**Verifikasi setelah R1:**
```bash
# Check which pod was evicted
kubectl -n actual-usage-exp get pods -o wide

# Expected: api pod terminated (same as R0), new pod on worker-1/2/3
kubectl -n actual-usage-exp get pod -l app=workload-api -o wide
```

## Verification Checklist

### Tanpa Load, R0 dan R1 Harus:

- [ ] **Both evict api pod** (bukan mem pod)
- [ ] **Actual usage blocks = 0** untuk R1 (karena cpu ratio < 0.80)
- [ ] **Api pod rescheduled** ke CPU-skewed node (worker-1/2/3)
- [ ] **No traffic disruption** (tidak ada k6 load anyway)
- [ ] **Stranding improved** di kedua case

### Metrics to Compare (R0 vs R1)

```bash
# 1. Check eviction target
echo "=== R0 Eviction ==="
grep "Evicting pod" "$OUTPUT_DIR/descheduler.log"

echo "=== R1 Eviction ==="
grep "Evicting pod" "$OUTPUT_DIR_R1/descheduler.log"

# 2. Check ActualUsageEvictor behavior (R1 only)
echo "=== R1 ActualUsageEvictor ==="
grep -E "(ActualUsageEvictor|ALLOWED|BLOCKED)" "$OUTPUT_DIR_R1/descheduler.log"

# 3. Compare pod placement
echo "=== R0 Final Layout ==="
kubectl get pods -o wide --sort-by=.spec.nodeName

echo "=== R1 Final Layout ==="
# (setelah redeploy dan run R1)
```

## Expected Log Output

### R0 Logs (No ActualUsageEvictor):

```
I0610 10:00:00.000000       1 resourcedefragmentationc2.go:123] "Processing node" node="worker-6"
I0610 10:00:00.100000       1 resourcedefragmentationc2.go:230] "pod selected" pod="actual-usage-exp/workload-api-xxx"
I0610 10:00:00.200000       1 resourcedefragmentationc2.go:250] "Evicting pod" pod="actual-usage-exp/workload-api-xxx"
```

### R1 Logs (With ActualUsageEvictor):

```
I0610 10:00:00.000000       1 resourcedefragmentationc2.go:123] "Processing node" node="worker-6"
I0610 10:00:00.100000       1 resourcedefragmentationc2.go:230] "pod selected" pod="actual-usage-exp/workload-api-xxx"
I0610 10:00:00.150000       1 actualusageevictor.go:85] "ActualUsageEvictor checking pod" pod="actual-usage-exp/workload-api-xxx" cpuRatio=0.04 threshold=0.80
I0610 10:00:00.160000       1 actualusageevictor.go:90] "ActualUsageEvictor ALLOWED" pod="actual-usage-exp/workload-api-xxx" reason="cpu usage below threshold"
I0610 10:00:00.200000       1 resourcedefragmentationc2.go:250] "Evicting pod" pod="actual-usage-exp/workload-api-xxx"
```

## Success Criteria

Test "Without load" dianggap sukses jika:

1. ✅ R0 evicts api pod (bukan mem pod)
2. ✅ R1 evicts api pod (bukan mem pod) - **same as R0**
3. ✅ R1 ActualUsageEvictor checks api pod → ALLOWED (cpu ratio < 0.80)
4. ✅ Both R0 and R1 improve stranding
5. ✅ Tidak ada perbedaan behavior antara R0 dan R1

**Key Takeaway:** Tanpa traffic, ActualUsageEvictor tidak mengubah eviction decision karena actual usage rendah. Ini adalah **baseline test** untuk memvalidasi bahwa plugin tidak interfere ketika tidak diperlukan.

## Next Step

Setelah "Without load" test berhasil, lanjut ke:
- **With load test**: jalankan k6 api-load.js untuk membuat api pod busy
- Expected: R0 tetap evict api pod (traffic disruption), R1 block api pod dan evict mem pod (no traffic disruption)

## Troubleshooting

### Metrics Server tidak tersedia
```bash
# Check metrics server
kubectl get pods -n kube-system | grep metrics-server

# If not running, install:
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

### Pod tidak ter-schedule dengan benar
```bash
# Check node taints
kubectl describe nodes | grep -A 5 Taints

# Uncordon all nodes
kubectl uncordon $(kubectl get nodes -o name | grep worker)
```

### ActualUsageEvictor tidak muncul di R1 logs
```bash
# Check policy applied correctly
kubectl -n kube-system get configmap actual-usage-policy -o yaml

# Should show ActualUsageEvictor in preevictionfilter section
```
