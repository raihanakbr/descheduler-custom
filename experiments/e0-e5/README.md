# E0-E4 thesis experiment matrix

This folder contains a local-cluster runner skeleton for the final real-usage descheduler matrix.
It is intentionally Kubernetes-only: it does not create or destroy cloud resources.

## Groups

- E0: scheduler only / no descheduler
- E1: official/base descheduler `LowNodeUtilization`
- E2: Ian request-based `ResourceDefragmentation` RII+TOPSIS (`usageMode: requests`)
- E3: actual-usage RII+TOPSIS raw/no smoothing (`usageMode: actual-raw`)
- E4: actual-usage RII+TOPSIS + EWMA tight CronJob with persisted state (`usageMode: actual-ewma-persisted`)

## Workload requirements

Each group uses staged ramps: `low`, `medium`, `high-safe`.
Every stage deploys CPU, memory, mixed, and bursty burners plus probe pods to measure schedulability.
The runner captures pods deployed/evicted, probe scheduled/pending/latency, node/pod metrics, events, and policy output.

## E4 note

E4 intentionally uses persisted EWMA state because a CronJob-only in-memory EWMA would lose history after each run. The weak CronJob/in-memory variant is not a final experiment group. A long-running descheduler Deployment with in-memory EWMA can be tested separately as optional E4b.

## Optional E4b ablation

E4b runs the tight EWMA path as a long-running descheduler Deployment using `usageMode: actual-ewma`.
Unlike the final E4 CronJob, it does not persist EWMA state because the descheduler process itself is expected to stay alive.
If the Deployment pod restarts, the in-memory EWMA state resets.
