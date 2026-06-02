# E3 NodePort single-service workload

This experiment uses the existing `workload-http` app from
`experiments/e3-k6-http-workload/cmd/workload-http`, but deploys it as one
multi-replica Deployment and one NodePort Service.

The goal is to test the runtime behavior of a single Kubernetes Service whose
Pods all declare identical resource requests while traffic drives different
actual CPU and memory profiles.

## Image

The default manifests use:

```text
docker.io/matthewhjt/workload-http:manual-v1
```

Build and push it from this repository:

```bash
cd experiments/e3-nodeport-single-service
docker login
IMAGE=docker.io/matthewhjt/workload-http:manual-v1 scripts/build-push-dockerhub.sh
```

## Deploy default NodePort routing

This variant intentionally does not set `externalTrafficPolicy`.

```bash
kubectl apply -f k8s/workload-default-nodeport.yaml
kubectl rollout status deployment/workload-http -n e3-nodeport --timeout=180s
kubectl get pods -n e3-nodeport -o wide
kubectl get svc -n e3-nodeport workload-http -o wide
```

Get the NodePort:

```bash
PORT=$(kubectl get svc -n e3-nodeport workload-http -o jsonpath='{.spec.ports[0].nodePort}')
echo "$PORT"
```

From the control-plane node, drive traffic through worker private IPs:

```bash
# CPU-heavy traffic through worker-a entrypoint
for i in $(seq 1 100); do
  curl -s "http://172.31.18.162:${PORT}/cpu/work?cpu_ms=250&mem_mb=4&hold_ms=0" >/dev/null &
done
wait

# Memory-heavy traffic through worker-b entrypoint
for i in $(seq 1 100); do
  curl -s "http://172.31.18.78:${PORT}/memory/work?cpu_ms=20&mem_mb=64&hold_ms=1000" >/dev/null &
done
wait

# Balanced traffic through worker-c entrypoint
for i in $(seq 1 100); do
  curl -s "http://172.31.17.242:${PORT}/balanced/work?cpu_ms=100&mem_mb=16&hold_ms=200" >/dev/null &
done
wait
```

Capture metrics:

```bash
kubectl top nodes
kubectl top pods -n e3-nodeport
kubectl get pods -n e3-nodeport -o wide
```

Or run the same mixed traffic profile with k6:

```bash
PORT=$(kubectl get svc -n e3-nodeport workload-http -o jsonpath='{.spec.ports[0].nodePort}')
NODEPORT="$PORT" WORKER_IPS="172.31.18.162,172.31.18.78,172.31.17.242" k6 run k6/nodeport-mixed.js
```

The default k6 profile sends approximately 45% CPU-heavy traffic, 30%
memory-heavy traffic, and 25% balanced traffic. The rate can be adjusted with:

```bash
TARGET_RPS=40 PREALLOCATED_VUS=80 MAX_VUS=160 NODEPORT="$PORT" k6 run k6/nodeport-mixed.js
```

## Deploy local NodePort routing

This variant uses the same Deployment shape, but the Service sets
`externalTrafficPolicy: Local` so a node only serves local endpoints.

```bash
kubectl apply -f k8s/workload-local-nodeport.yaml
kubectl rollout status deployment/workload-http-local -n e3-nodeport-local --timeout=180s
kubectl get pods -n e3-nodeport-local -o wide
kubectl get svc -n e3-nodeport-local workload-http-local -o wide
```

## Cleanup

```bash
kubectl delete namespace e3-nodeport
kubectl delete namespace e3-nodeport-local
```

## Notes

- All Pods use the same image and the same declared requests.
- Runtime profile is controlled through HTTP parameters:
  - `/cpu/work?cpu_ms=250&mem_mb=4&hold_ms=0`
  - `/memory/work?cpu_ms=20&mem_mb=64&hold_ms=1000`
  - `/balanced/work?cpu_ms=100&mem_mb=16&hold_ms=200`
- The default NodePort variant may forward requests across nodes. This is the
  behavior being tested first.
