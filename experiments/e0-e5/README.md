# E0-E5 thesis experiment matrix

This folder contains a local-cluster runner skeleton for the final real-usage descheduler matrix.
It is intentionally Kubernetes-only: it does not create or destroy cloud resources.

## Groups

- E0: scheduler only / no descheduler
- E1: official/base descheduler `LowNodeUtilization`
- E2: Ian request-based `ResourceDefragmentation` RII+TOPSIS (`usageMode: requests`)
- E3: actual-usage RII+TOPSIS raw/no smoothing (`usageMode: actual-raw`)
- E4: actual-usage RII+TOPSIS + EWMA tight/direct (`usageMode: actual-ewma`)
- E5: loose monitoring-agent Deployment publishes EWMA annotations; descheduler CronJob consumes (`usageMode: published-ewma`)

## Workload requirements

Each group uses staged ramps: `low`, `medium`, `high-safe`.
Every stage deploys CPU, memory, mixed, and bursty burners plus probe pods to measure schedulability.
The runner captures pods deployed/evicted, probe scheduled/pending/latency, node/pod metrics, events, and policy output.

## Optional E4 ablations

- E4a: `actual-ewma-persisted` lets a descheduler CronJob calculate EWMA from raw metrics and persist the previous smoothed value in node annotations. This tests tight CronJob + persisted state without a separate monitoring agent.
- E4b: `actual-ewma` under a long-running descheduler Deployment keeps EWMA in process memory. This tests tight Deployment + in-memory state.

Both are optional ablations after E0-E5 are stable.
