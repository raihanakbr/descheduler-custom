# E3 k6 HTTP workload experiment

This experiment isolates the load-generation design for E3 actual-usage testing.
It follows the Tomarchio-style idea of driving runtime resource use with k6, but it does not copy the microservice benchmark topology.

## Architecture

```text
k6 runner VM, outside cluster
  -> ingress endpoint
  -> ClusterIP Services
  -> workload-http pods on Kubernetes workers
```

The k6 runner must be in the same AWS zone/network as the cluster, but it should not join the Kubernetes cluster. That keeps k6 CPU and memory usage out of worker-node metrics.

## Components

- `cmd/workload-http`: small Go HTTP server with `GET /work`.
- `k8s/workload.yaml`: four Deployment profiles using the same image.
- `k8s/ingress.yaml`: ingress paths for `/hot`, `/warm`, `/mem`, and `/idle`.
- `k8s/probe.yaml`: schedulability probe pod for fragmentation evidence.
- `k6/scenario.js`: phased k6 load script.
- `scripts/`: build, image import, deploy, k6 run, probe, and snapshot helpers.

## Workload server

`/work` accepts query parameters:

- `cpu_ms`: approximate CPU busy-loop duration.
- `mem_mb`: transient memory allocation size (capped at 128 MB for t3.small workers).
- `hold_ms`: how long allocated memory is held before the response returns.

The server accepts prefixed ingress paths such as `/hot/work` and `/mem/work`.

Guards are configured with environment variables:

- `MAX_CPU_MS`, default `2000`
- `MAX_MEM_MB`, default `128`
- `MAX_HOLD_MS`, default `5000`
- `MAX_INFLIGHT`, default `64`

## Kubernetes profiles

All profiles run the same image, `workload-http:local`.
Sized for **t3.small workers** (2 vCPU / 2 GiB, ~1800m CPU / ~1.7 GiB allocatable).

| Profile | Replicas | Request | Actual behavior |
|---------|----------|---------|----------------|
| `workload-hot` | 4 | 100m / 64Mi | CPU-heavy via k6 |
| `workload-warm` | 3 | 250m / 128Mi | Moderate CPU+mem |
| `workload-mem` | 3 | 100m / 64Mi | Memory-heavy (80-100MB holds) |
| `workload-idle-overrequest` | 2 | 700m / 512Mi | Near-idle actual usage |

Total request across 12 pods: ~2850m CPU / ~1856Mi.
With 3 workers (~5400m total allocatable), request-space looks manageable but
actual runtime usage creates imbalance that the scheduler cannot see.

The `idle-overrequest` profile intentionally reserves large request-space while
using almost nothing at runtime — this is the classic over-provisioning pattern
that causes request-vs-actual mismatch.

## Typical flow

From the control-plane VM:

```bash
cd experiments/e3-k6-http-workload
scripts/build-workload-image.sh
WORKERS="10.0.1.11 10.0.1.12 10.0.1.13" scripts/import-image-to-workers.sh
scripts/deploy-workload.sh
```

From the k6 runner VM:

```bash
cd experiments/e3-k6-http-workload
BASE_URL=http://<ingress-endpoint> scripts/run-k6-load.sh
```

During the run, collect cluster snapshots from the control-plane VM:

```bash
RESULT_DIR=/tmp/e3-k6-results scripts/collect-snapshot.sh before-load
RESULT_DIR=/tmp/e3-k6-results scripts/collect-snapshot.sh during-burst
RESULT_DIR=/tmp/e3-k6-results scripts/create-probe.sh
RESULT_DIR=/tmp/e3-k6-results scripts/collect-snapshot.sh probe-before-defrag
```

Then run the E3 actual-usage descheduler policy and collect `after-defrag`.

## Notes

- Ingress controller (nginx) is exposed via NodePort on workers. k6 hits `<worker-ip>:<nodeport>` and nginx routes by path.
- k6 should randomize across all worker IPs for even ingress distribution.
- Workers are **t3.small** (2 vCPU / 2 GiB). Memory burn capped at 100-128 MB per pod to avoid OOMKill.
- k6 runs on a **separate AWS instance** (t3.small) outside the cluster to avoid polluting worker metrics.
- Disk and network throughput are not experiment targets. Network traffic is only the trigger mechanism for CPU and memory behavior.
- Pod placement uses NO affinity/pinning — scheduler spreads based on requests, k6 load creates actual-usage imbalance organically.
