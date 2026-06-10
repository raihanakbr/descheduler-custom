# Walkthrough: R0 - ResourceDefragmentationC2 With k6 Load

## Tujuan

Test scenario dengan k6 load untuk memverifikasi bahwa R0 (ResourceDefragmentationC2 tanpa ActualUsageEvictor) mengevict api pod yang sedang busy, menyebabkan traffic disruption (HTTP 503, latency spike).

## Expected Behavior (With k6 Load)

**R0 (ResourceDefragmentationC2 only, WITH k6 load):**

1. C2 mengevaluasi worker-6 (source node, mem-skewed)
2. api pod (50m/240Mi) terpilih pertama (memory lebih besar = stranding relief lebih baik)
3. api pod di-evict
4. Pod pengganti dijadwalkan di node CPU-skewed (worker-1/2/3)
5. **During eviction + replacement: api traffic fails (503, latency spike)**
6. Stranding membaik (defrag sukses)

**Key Point:** Tanpa ActualUsageEvictor, C2 tidak peduli bahwa api pod sedang busy. Api pod tetap di-evict, menyebabkan disruption ke traffic yang sedang berjalan.

## Prerequisites

1. Kubernetes cluster dengan 6 worker nodes
2. Metrics Server terinstall dan berjalan
3. kubectl terkonfigurasi
4. k6 terinstall (`k6 version`)
5. Descheduler image tersedia: `DESCHEDULER_IMAGE` environment variable
6. Workload image: `docker.io/matthewhjt/workload-http:actual-usage-v1`

## Quick Run (Single Script)

```bash
cd descheduler-custom/experiments/actual-usage-evictor-v2

export WORKLOAD_IMAGE="docker.io/matthewhjt/workload-http:actual-usage-v1"
export DESCHEDULER_IMAGE="docker.io/matthewhjt/descheduler-custom:actual-usage-v1"

./scripts/run-r0-with-load.sh
```

Output akan disimpan di `results/walkthrough/r0-load/<timestamp>/`.

## Step-by-Step Execution (Manual)

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
# - worker-1,2,3: masing-masing 2 cpu pods (750m/25Mi)
# - worker-4,5: masing-masing 2 mem pods (50m/240Mi)
# - worker-6: 1 api pod (50m/350Mi) + 1 mem pod (50m/250Mi)

# Verify api pod location
kubectl -n actual-usage-exp get pod -l app=workload-api -o wide

# Check node allocatable
kubectl get nodes -o custom-columns='NAME:.metadata.name,CPU:.status.allocatable.cpu,MEM:.status.allocatable.memory'
```

### 4. Verify Metrics Server

```bash
# Wait for metrics to be available
sleep 30

# Check api pod metrics (sebelum load)
kubectl -n actual-usage-exp top pod -l app=workload-api

# Expected: CPU usage sangat rendah (< 10m), karena belum ada traffic
# Example output:
# NAME                            CPU(cores)   MEMORY(%)
# workload-api-xxxxxxxxxx-xxxxx   2m           12%
```

### 5. Get API Service Endpoint

```bash
# Get source node IP
source_node="$(cat "$OUTPUT_DIR/source-node.txt")"
source_ip="$(kubectl get node "$source_node" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')"

# Get NodePort
api_port="$(kubectl -n actual-usage-exp get svc workload-api -o jsonpath='{.spec.ports[0].nodePort}')"

# Construct API URL
api_url="http://${source_ip}:${api_port}"
echo "API URL: $api_url"

# Test connectivity
curl -s "${api_url}/readyz" | head -1
# Expected: "ok"
```

### 6. Take Before Snapshot

```bash
OUTPUT_DIR="results/walkthrough/r0-load"
mkdir -p "$OUTPUT_DIR"

./scripts/snapshot.sh before "$OUTPUT_DIR"

# Manual snapshot untuk analisis
kubectl get nodes -o json > "$OUTPUT_DIR/nodes-before.json"
kubectl get pods -A -o json > "$OUTPUT_DIR/pods-before.json"
```

### 7. Start k6 Load Generator

```bash
# Start k6 in background
API_URL="$api_url" \
API_DURATION="10m" \
API_RPS=8 \
API_CPU_UNITS=900 \
API_VUS=60 \
API_MAX_VUS=160 \
k6 run \
  --out "json=$OUTPUT_DIR/api-load.json" \
  --summary-export "$OUTPUT_DIR/api-load-summary.json" \
  k6/api-load.js > "$OUTPUT_DIR/api-load.log" 2>&1 &

K6_PID=$!
echo "k6 started with PID: $K6_PID"
```

### 8. Wait for Load Stabilization (~30s)

```bash
# Wait 30 seconds for load to stabilize
echo "Waiting 30s for load stabilization..."
sleep 30

# Check api pod metrics (setelah load berjalan)
kubectl -n actual-usage-exp top pod -l app=workload-api

# Expected: CPU usage meningkat signifikan (> 40m)
# Example output:
# NAME                            CPU(cores)   MEMORY(%)
# workload-api-xxxxxxxxxx-xxxxx   45m          15%

# Calculate CPU ratio:
# CPU request = 50m
# Jika actual = 45m, maka ratio = 45/50 = 0.90 (di atas threshold 0.80)
```

**Verifikasi api pod busy:**

```bash
# Check k6 progress
tail -20 "$OUTPUT_DIR/api-load.log"

# Expected: k6 menunjukkan request rate stabil
# Example:
# ✓ api status 200...: 100.00% ✓ 480 out of 480
# http_req_duration...: avg=12.5ms min=8ms med=11ms max=45ms p(95)=25ms
```

### 10. Run Descheduler - R0 (Control, No ActualUsageEvictor)

```bash
# Apply R0 policy
./scripts/run-descheduler.sh policies/r0-rdc2.yaml "$OUTPUT_DIR"

# Check descheduler logs
cat "$OUTPUT_DIR/descheduler.log" | grep -E "(Evicting pod|pod selected|Processing node)"
```

**Expected in logs:**

- C2 identifies worker-6 as source node
- api pod selected (karena memory request lebih besar: 350Mi vs 250Mi)
- api pod evicted
- **No blocking from ActualUsageEvictor** (karena tidak ada plugin ini di R0)

**Verifikasi setelah R0:**

```bash
# Check which pod was evicted
kubectl -n actual-usage-exp get pods -o wide

# Check events
kubectl get events -n actual-usage-exp --sort-by='.lastTimestamp' | tail -20

# Expected: api pod terminated, new pod scheduled on worker-1/2/3
kubectl -n actual-usage-exp get pod -l app=workload-api -o wide
# Should show api pod now on different node (worker-1, worker-2, or worker-3)
# OR api pod still terminating (if check immediately)
```

### 11. Monitor Traffic Disruption

```bash
# Wait 30 seconds to capture disruption
sleep 30

# Check k6 logs for failures
tail -30 "$OUTPUT_DIR/api-load.log"

# Expected: k6 menunjukkan HTTP 503 dan latency spike
# Example:
# ✗ api status 200...: 85.00% ✓ 408 out of 480
# http_req_duration...: avg=250.5ms min=8ms med=150ms max=1200ms p(95)=800ms
# http_req_failed.....: 15.00% ✓ 72 out of 480

# Check k6 summary JSON
cat "$OUTPUT_DIR/api-load-summary.json" | jq '.metrics.http_req_duration.values["p(95)"]'
# Expected: p95 latency spike (> 500ms)

cat "$OUTPUT_DIR/api-load-summary.json" | jq '.metrics.http_req_failed.values'
# Expected: failure rate > 0 (misalnya 0.15 = 15%)
```

### 12. Take After Snapshot

```bash
# Wait 30 seconds for pod replacement to complete
echo "Waiting 30s for pod replacement..."
sleep 30

./scripts/snapshot.sh after "$OUTPUT_DIR"

# Check final pod layout
kubectl -n actual-usage-exp get pods -o wide --sort-by=.spec.nodeName
```

### 13. Stop k6 Load Generator

```bash
# Stop k6 gracefully
kill -INT "$K6_PID"
wait "$K6_PID" 2>/dev/null || true
echo "k6 stopped"

# Check final k6 summary
cat "$OUTPUT_DIR/api-load-summary.json" | jq '.'
```

## Verification Checklist

### R0 With Load Harus:

- [ ] **Evicts api pod** (bukan mem pod)
- [ ] **Actual usage blocks = 0** (tidak ada ActualUsageEvictor)
- [ ] **Api pod rescheduled** ke CPU-skewed node (worker-1/2/3)
- [ ] **HTTP 503 errors > 0** (traffic disruption saat eviction)
- [ ] **p95 latency spike** (> 200ms, normal < 50ms)
- [ ] **Stranding improved** di cluster

### Metrics to Analyze

```bash
# 1. Check eviction target
echo "=== R0 Eviction ==="
grep "Evicting pod" "$OUTPUT_DIR/descheduler.log"

# 2. Check k6 traffic disruption
echo "=== k6 Summary ==="
cat "$OUTPUT_DIR/api-load-summary.json" | jq '{
  http_req_duration_p95: .metrics.http_req_duration.values["p(95)"],
  http_req_failed: .metrics.http_req_failed.values,
  http_reqs: .metrics.http_reqs.values
}'

# 3. Compare before vs after stranding
echo "=== Stranding Before ==="
cat "$OUTPUT_DIR/cluster-metrics-before.json" | jq '.stranding'

echo "=== Stranding After ==="
cat "$OUTPUT_DIR/cluster-metrics-after.json" | jq '.stranding'

# 4. Check pod placement
echo "=== Final Layout ==="
cat "$OUTPUT_DIR/layout-after.txt"
```

## Expected Log Output

### R0 Logs (No ActualUsageEvictor):

```
I0610 10:00:00.000000       1 resourcedefragmentationc2.go:123] "Processing node" node="worker-6"
I0610 10:00:00.100000       1 resourcedefragmentationc2.go:230] "pod selected" pod="actual-usage-exp/workload-api-xxx"
I0610 10:00:00.200000       1 resourcedefragmentationc2.go:250] "Evicting pod" pod="actual-usage-exp/workload-api-xxx"
```

### k6 Summary (Example):

```json
{
  "metrics": {
    "http_req_duration": {
      "values": {
        "avg": 125.5,
        "min": 8,
        "med": 45,
        "max": 1200,
        "p(90)": 450,
        "p(95)": 800
      }
    },
    "http_req_failed": {
      "values": {
        "rate": 0.15,
        "passes": 72,
        "fails": 408
      }
    },
    "http_reqs": {
      "values": {
        "count": 480,
        "rate": 8
      }
    }
  }
}
```

## Success Criteria

Test "R0 With Load" dianggap sukses jika:

1. ✅ R0 evicts api pod (bukan mem pod)
2. ✅ No ActualUsageEvictor blocking (0 blocks)
3. ✅ Api pod rescheduled ke CPU-skewed node
4. ✅ **HTTP 503 errors > 0** (traffic disruption terjadi)
5. ✅ **p95 latency spike > 200ms** (normal < 50ms)
6. ✅ Stranding improved (defrag success)

**Key Takeaway:** R0 tanpa ActualUsageEvictor mengevict api pod yang sedang busy, menyebabkan traffic disruption. Ini adalah **control test** untuk menunjukkan bahwa tanpa protection, busy pod tetap di-evict.

## Next Step

Setelah "R0 With Load" test berhasil, lanjut ke:

- **R1 With Load test**: jalankan k6 api-load.js + R1 policy (dengan ActualUsageEvictor)
- Expected: R1 block api pod, evict mem pod, no traffic disruption (HTTP 503 = 0)

## Troubleshooting

### k6 tidak bisa connect ke API Service

```bash
# Check service endpoint
kubectl -n actual-usage-exp get svc workload-api

# Check NodePort
kubectl -n actual-usage-exp get svc workload-api -o jsonpath='{.spec.ports[0].nodePort}'

# Test manually
curl -v "${api_url}/readyz"
```

### API pod tidak busy (CPU ratio < 0.80)

```bash
# Increase k6 load
export API_RPS=12
export API_CPU_UNITS=1200

# Restart k6 with higher load
kill -INT "$K6_PID"
# ... restart k6 with new parameters
```

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

### Descheduler tidak mengevict api pod

```bash
# Check descheduler logs
cat "$OUTPUT_DIR/descheduler.log"

# Check policy applied correctly
kubectl -n kube-system get configmap actual-usage-policy -o yaml

# Should show ResourceDefragmentationC2 in balance section
```
