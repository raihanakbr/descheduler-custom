# Image build walkthrough

Image build is separate from experiment execution. Rebuild only when workload
or plugin code changes, and use immutable tags in reported runs.

The custom descheduler image intentionally contains only:

- `ResourceDefragmentationC2`
- `ActualUsageEvictor`
- `NetworkCostEvictor`
- `DefaultEvictor`
- `HighNodeUtilization`
- `LowNodeUtilization`

`DefaultEvictor` and the utilization strategies are included because the
experiment policies execute them in the same descheduler process.

## 1. Enter the repository

```bash
cd /home/matthewhjt/TA/descheduler-custom-real-usage-fixed
export EXP_DIR="$PWD/experiments/actual-usage-evictor"
docker login
```

## 2. Build and push the HTTP workload

```bash
export WORKLOAD_IMAGE="docker.io/matthewhjt/workload-http:actual-usage-v1"
IMAGE="$WORKLOAD_IMAGE" "$EXP_DIR/scripts/build-push-workload.sh"
```

The image is built from
`experiments/actual-usage-evictor/cmd/workload-http`. Use a new tag when that
workload changes.

## 3. Build and push the custom descheduler

```bash
export DESCHEDULER_IMAGE="docker.io/matthewhjt/descheduler-custom:actual-usage-v1"
IMAGE="$DESCHEDULER_IMAGE" "$EXP_DIR/scripts/build-push-descheduler.sh"
```

This uses `Dockerfile.experiment` and the `experiment_plugins` build tag. It
does not include the removed monitoring agent or unrelated descheduler plugins.

## 4. Record the exact inputs

```bash
git rev-parse HEAD
printf 'WORKLOAD_IMAGE=%s\nDESCHEDULER_IMAGE=%s\n' \
  "$WORKLOAD_IMAGE" "$DESCHEDULER_IMAGE"
```

Keep the commit and both immutable image tags with the experiment results.
