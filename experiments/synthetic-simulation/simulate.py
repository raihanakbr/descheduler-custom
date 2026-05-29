#!/usr/bin/env python3
"""
Deterministic synthetic simulation of the ResourceDefragmentation descheduler.

Mirrors:
  /root/.openclaw/workspace/skripsi/03-implementation/descheduler-custom-pr2/
  pkg/framework/plugins/resourcedefragmentation/resourcedefragmentation.go

Function-by-function map (Go -> Python):
    computeRII             -> compute_rii
    computeFSI             -> compute_fsi
    computePriorityIndex   -> compute_priority
    computeC1              -> compute_c1
    evaluateFeasibleTargets-> evaluate_feasible_targets
    computeC2              -> compute_c2
    computeC3              -> compute_c3
    computeC4              -> compute_c4
    topsis                 -> topsis
    Balance                -> balance

Usage mode used in this synthetic run: requests (so pod_usage == pod_requests),
which matches the recap MD's calculations.
"""

from __future__ import annotations

import argparse
import copy
import math
from dataclasses import dataclass, field
from typing import Dict, List, Optional, Tuple

# ----------------------------- Domain ------------------------------------ #

EPSILON = 1e-10
NO_FEASIBLE_TARGET_SCORE = -999.9


@dataclass
class Pod:
    name: str
    cpu: int     # milliCPU
    mem: int     # Mi (we keep memory in Mi; ratios are unit-free)
    priority: int = 0

    def __repr__(self) -> str:
        return f"{self.name}({self.cpu}m/{self.mem}Mi)"


@dataclass
class Node:
    name: str
    alloc_cpu: int
    alloc_mem: int
    pods: List[Pod] = field(default_factory=list)
    is_master: bool = False


@dataclass
class NodeState:
    """Mirror of NodeResourceState in Go. UsedCPU/UsedMem == requests in
    UsageModeRequests, which is what the synthetic recap uses."""
    alloc_cpu: int
    alloc_mem: int
    requested_cpu: int
    requested_mem: int
    used_cpu: int
    used_mem: int


# ----------------------- Math helpers (mirror Go) ------------------------ #

def compute_rii(alloc_cpu: int, alloc_mem: int, used_cpu: int, used_mem: int) -> float:
    return used_cpu / alloc_cpu - used_mem / alloc_mem


def compute_fsi(alloc_cpu: int, alloc_mem: int, used_cpu: int, used_mem: int) -> float:
    c = (alloc_cpu - used_cpu) / alloc_cpu
    m = (alloc_mem - used_mem) / alloc_mem
    return c * m


def compute_priority(rii: float, fsi: float, w_p: float = 0.5) -> float:
    return w_p * abs(rii) + (1 - w_p) * (1 / (fsi + EPSILON))


def get_pod_requests(pod: Pod) -> Tuple[int, int]:
    return pod.cpu, pod.mem


def compute_c1(node_rii: float, pod: Pod, alloc_cpu: int, alloc_mem: int) -> float:
    pod_cpu, pod_mem = get_pod_requests(pod)
    pod_cpu_ratio = pod_cpu / alloc_cpu
    pod_mem_ratio = pod_mem / alloc_mem
    pod_rii = pod_cpu_ratio - pod_mem_ratio
    max_share = max(pod_cpu_ratio, pod_mem_ratio)
    return node_rii * pod_rii * max_share


@dataclass
class FeasibilityDecision:
    can_evict: bool
    reason: str
    target_names: List[str] = field(default_factory=list)
    best_improvement: float = -1.0
    per_target: Dict[str, float] = field(default_factory=dict)


def evaluate_feasible_targets(
    pod: Pod, origin_name: str, node_states: Dict[str, NodeState]
) -> FeasibilityDecision:
    decision = FeasibilityDecision(False, "no-non-origin-request-feasible-target")

    pod_cpu, pod_mem = get_pod_requests(pod)
    # Requests mode: pod_used == pod_requests
    pod_used_cpu, pod_used_mem = pod_cpu, pod_mem

    origin = node_states[origin_name]
    origin_before = abs(compute_rii(origin.alloc_cpu, origin.alloc_mem,
                                    origin.used_cpu, origin.used_mem))
    origin_after = abs(compute_rii(origin.alloc_cpu, origin.alloc_mem,
                                   origin.used_cpu - pod_used_cpu,
                                   origin.used_mem - pod_used_mem))

    for name, state in node_states.items():
        if name == origin_name:
            continue
        free_cpu = state.alloc_cpu - state.requested_cpu
        free_mem = state.alloc_mem - state.requested_mem
        if free_cpu < pod_cpu or free_mem < pod_mem:
            continue

        decision.target_names.append(name)
        target_before = abs(compute_rii(state.alloc_cpu, state.alloc_mem,
                                        state.used_cpu, state.used_mem))
        target_after = abs(compute_rii(state.alloc_cpu, state.alloc_mem,
                                       state.used_cpu + pod_used_cpu,
                                       state.used_mem + pod_used_mem))
        improvement = (origin_before + target_before) - (origin_after + target_after)
        decision.per_target[name] = improvement
        if improvement > decision.best_improvement:
            decision.best_improvement = improvement

    if not decision.target_names:
        return decision
    decision.reason = "no-positive-projected-score-improvement"
    if decision.best_improvement > 0:
        decision.can_evict = True
        decision.reason = "request-feasible-and-score-improves"
    return decision


def compute_c2(pod: Pod, origin_name: str,
               node_states: Dict[str, NodeState]) -> float:
    decision = evaluate_feasible_targets(pod, origin_name, node_states)
    if not decision.can_evict:
        return NO_FEASIBLE_TARGET_SCORE
    return decision.best_improvement


def compute_c3(pod: Pod, state: NodeState) -> float:
    c = (state.alloc_cpu - state.used_cpu) / state.alloc_cpu
    m = (state.alloc_mem - state.used_mem) / state.alloc_mem
    pod_cpu, pod_mem = get_pod_requests(pod)
    p_c = pod_cpu / state.alloc_cpu
    p_m = pod_mem / state.alloc_mem
    return (c * p_m) + (m * p_c) + (p_c * p_m)


def compute_c4(pod: Pod) -> float:
    return float(pod.priority)


# --------------------------- Pretty-printing ----------------------------- #

def hr(ch: str = "-", n: int = 78) -> str:
    return ch * n


def fmt_state(state: NodeState) -> str:
    rii = compute_rii(state.alloc_cpu, state.alloc_mem,
                      state.used_cpu, state.used_mem)
    fsi = compute_fsi(state.alloc_cpu, state.alloc_mem,
                      state.used_cpu, state.used_mem)
    free_cpu = state.alloc_cpu - state.used_cpu
    free_mem = state.alloc_mem - state.used_mem
    return (f"req={state.requested_cpu}m/{state.requested_mem}Mi  "
            f"free={free_cpu}m/{free_mem}Mi  "
            f"RII={rii:+.4f}  FSI={fsi:.4f}")


# -------------------------- TOPSIS (with prints) ------------------------- #

def topsis(node: Node, pods: List[Pod],
           node_states: Dict[str, NodeState],
           verbose: bool = True) -> Optional[Pod]:
    if not pods:
        return None

    weights = [0.30, 0.30, 0.25, 0.15]
    is_benefit = [True, True, True, False]
    n_pods = len(pods)
    n_crit = len(weights)

    state = node_states[node.name]
    node_rii = compute_rii(state.alloc_cpu, state.alloc_mem,
                           state.used_cpu, state.used_mem)

    # 1. Raw decision matrix
    matrix = []
    for pod in pods:
        row = [
            compute_c1(node_rii, pod, state.alloc_cpu, state.alloc_mem),
            compute_c2(pod, node.name, node_states),
            compute_c3(pod, state),
            compute_c4(pod),
        ]
        matrix.append(row)

    if verbose:
        print(f"  TOPSIS on {node.name} (nodeRII={node_rii:+.4f})")
        print(f"  weights = C1:0.30  C2:0.30  C3:0.25  C4:0.15")
        print(f"  benefit = C1:T     C2:T     C3:T     C4:F (cost)")
        print(f"  Raw matrix")
        print(f"    {'pod':<10} {'C1':>10} {'C2':>10} {'C3':>10} {'C4':>6}")
        for pod, row in zip(pods, matrix):
            print(f"    {pod.name:<10} "
                  f"{row[0]:>10.5f} {row[1]:>10.5f} "
                  f"{row[2]:>10.5f} {row[3]:>6.2f}")

    # 2. Vector normalization per column
    normalized = [[0.0] * n_crit for _ in range(n_pods)]
    for j in range(n_crit):
        sq = sum(matrix[i][j] ** 2 for i in range(n_pods))
        norm = math.sqrt(sq)
        for i in range(n_pods):
            normalized[i][j] = 0.0 if norm == 0 else matrix[i][j] / norm

    if verbose:
        print(f"  Normalized matrix")
        print(f"    {'pod':<10} {'C1':>10} {'C2':>10} {'C3':>10} {'C4':>10}")
        for pod, row in zip(pods, normalized):
            print(f"    {pod.name:<10} "
                  f"{row[0]:>10.5f} {row[1]:>10.5f} "
                  f"{row[2]:>10.5f} {row[3]:>10.5f}")

    # 3. Weighted matrix
    weighted = [[normalized[i][j] * weights[j] for j in range(n_crit)]
                for i in range(n_pods)]

    if verbose:
        print(f"  Weighted matrix")
        print(f"    {'pod':<10} {'C1':>10} {'C2':>10} {'C3':>10} {'C4':>10}")
        for pod, row in zip(pods, weighted):
            print(f"    {pod.name:<10} "
                  f"{row[0]:>10.5f} {row[1]:>10.5f} "
                  f"{row[2]:>10.5f} {row[3]:>10.5f}")

    # 4. Ideal best / worst per criterion
    ideal_best = [0.0] * n_crit
    ideal_worst = [0.0] * n_crit
    for j in range(n_crit):
        col = [weighted[i][j] for i in range(n_pods)]
        if is_benefit[j]:
            ideal_best[j] = max(col)
            ideal_worst[j] = min(col)
        else:
            ideal_best[j] = min(col)
            ideal_worst[j] = max(col)

    if verbose:
        print(f"  Ideal best  : "
              f"C1={ideal_best[0]:+.5f}  C2={ideal_best[1]:+.5f}  "
              f"C3={ideal_best[2]:+.5f}  C4={ideal_best[3]:+.5f}")
        print(f"  Ideal worst : "
              f"C1={ideal_worst[0]:+.5f}  C2={ideal_worst[1]:+.5f}  "
              f"C3={ideal_worst[2]:+.5f}  C4={ideal_worst[3]:+.5f}")

    # 5. Separation measures
    d_plus = [0.0] * n_pods
    d_minus = [0.0] * n_pods
    for i in range(n_pods):
        for j in range(n_crit):
            d_plus[i] += (weighted[i][j] - ideal_best[j]) ** 2
            d_minus[i] += (weighted[i][j] - ideal_worst[j]) ** 2
        d_plus[i] = math.sqrt(d_plus[i])
        d_minus[i] = math.sqrt(d_minus[i])

    # 6. Relative closeness
    cc = []
    for i in range(n_pods):
        denom = d_plus[i] + d_minus[i]
        cc.append(0.5 if denom == 0 else d_minus[i] / denom)

    if verbose:
        print(f"  Closeness")
        print(f"    {'pod':<10} {'d+':>10} {'d-':>10} {'cc':>10}")
        for pod, dp, dm, c in zip(pods, d_plus, d_minus, cc):
            print(f"    {pod.name:<10} {dp:>10.5f} {dm:>10.5f} {c:>10.5f}")

    # Strict '>': first occurrence wins on a tie (matches Go)
    best_cc = -1.0
    best_idx = -1
    for i, c in enumerate(cc):
        if c > best_cc:
            best_cc = c
            best_idx = i

    if best_idx == -1:
        if verbose:
            print("  TOPSIS: no candidate selected")
        return None

    chosen = pods[best_idx]
    if verbose:
        print(f"  TOPSIS pick : {chosen.name} (cc={best_cc:.5f})")
    return chosen


# ------------------------------ Balance ---------------------------------- #

def balance(nodes: List[Node], threshold: float, max_evictions: int,
            verbose: bool = True) -> List[Tuple[str, Pod]]:
    """Returns list of (origin_node, pod) evictions in order."""
    evictions: List[Tuple[str, Pod]] = []

    # Step 1: build cluster state cache (skip masters)
    node_states: Dict[str, NodeState] = {}
    fragmented: List[Dict] = []  # mimic fragmentedNode struct

    print(hr("="))
    print("STEP 1: build cluster state cache")
    print(hr("="))
    for node in nodes:
        if node.is_master:
            print(f"  skip master node {node.name}")
            continue
        req_cpu = sum(p.cpu for p in node.pods)
        req_mem = sum(p.mem for p in node.pods)
        # UsageModeRequests => used == requests
        used_cpu, used_mem = req_cpu, req_mem
        state = NodeState(node.alloc_cpu, node.alloc_mem,
                          req_cpu, req_mem, used_cpu, used_mem)
        node_states[node.name] = state

        rii = compute_rii(state.alloc_cpu, state.alloc_mem,
                          state.used_cpu, state.used_mem)
        fsi = compute_fsi(state.alloc_cpu, state.alloc_mem,
                          state.used_cpu, state.used_mem)
        is_frag = abs(rii) > threshold
        prio = compute_priority(rii, fsi) if is_frag else 0.0
        print(f"  Node {node.name}  "
              f"alloc={state.alloc_cpu}m/{state.alloc_mem}Mi  "
              f"{fmt_state(state)}  "
              f"fragmented={'YES' if is_frag else 'no'}"
              + (f"  priority={prio:.4f}" if is_frag else ""))
        if is_frag:
            fragmented.append({
                "node": node, "pods": list(node.pods),
                "rii": rii, "fsi": fsi, "priority": prio,
            })

    # Sort by priority desc
    fragmented.sort(key=lambda x: x["priority"], reverse=True)

    print()
    print(hr("="))
    print("STEP 2: fragmented node priority order")
    print(hr("="))
    for i, fn in enumerate(fragmented):
        print(f"  #{i+1}  {fn['node'].name}  "
              f"RII={fn['rii']:+.4f}  FSI={fn['fsi']:.4f}  "
              f"priority={fn['priority']:.4f}")

    # Step 3: eviction loop (mirror of Go)
    print()
    print(hr("="))
    print("STEP 3: eviction loop  "
          f"(threshold={threshold}, maxEvictions={max_evictions})")
    print(hr("="))

    iteration = 0
    idx = 0
    while idx < len(fragmented) and iteration < max_evictions:
        fn = fragmented[idx]
        node = fn["node"]
        print()
        print(hr("-"))
        print(f"Iteration {iteration+1}  origin={node.name}  "
              f"RII={fn['rii']:+.4f}")
        print(hr("-"))

        chosen = topsis(node, fn["pods"], node_states, verbose=verbose)
        if chosen is None:
            print("  no candidate, advance")
            idx += 1
            continue

        # Pre-eviction feasibility recheck
        recheck = evaluate_feasible_targets(chosen, node.name, node_states)
        print(f"  pre-eviction recheck: pod={chosen.name}  "
              f"feasible_targets={recheck.target_names}  "
              f"per_target_improvement={ {k: round(v, 5) for k, v in recheck.per_target.items()} }  "
              f"best={recheck.best_improvement:+.5f}  "
              f"can_evict={recheck.can_evict}  reason={recheck.reason}")
        if not recheck.can_evict:
            print("  guard blocked eviction, advance")
            idx += 1
            continue

        # Evict
        evictions.append((node.name, chosen))
        iteration += 1
        print(f"  EVICT #{iteration}: {chosen.name} from {node.name}")

        # Update origin state cache
        ev_cpu, ev_mem = chosen.cpu, chosen.mem
        s = node_states[node.name]
        s.requested_cpu -= ev_cpu
        s.requested_mem -= ev_mem
        s.used_cpu -= ev_cpu  # requests mode
        s.used_mem -= ev_mem
        fn["pods"] = [p for p in fn["pods"] if p.name != chosen.name]

        new_rii = compute_rii(s.alloc_cpu, s.alloc_mem, s.used_cpu, s.used_mem)
        print(f"  origin updated -> {fmt_state(s)}")

        # Decide whether origin remains in fragmented list
        if abs(new_rii) <= threshold:
            print(f"  {node.name} drops below threshold, removed from list")
            fragmented.pop(idx)
            # do not increment idx
        else:
            fn["rii"] = new_rii
            idx += 1
            if idx >= len(fragmented):
                idx = 0
                print("  wrap idx -> 0 (still-fragmented nodes remain)")

    return evictions


# ----------------------- Default kube-scheduler -------------------------- #
#
# Mirrors default kube-scheduler v1.x behavior used here:
#   1. Filter:  NodeResourcesFit -> node must have enough free CPU and memory
#   2. Score:   LeastAllocated -> per resource: (cap - (req+pod_req)) / cap * 100
#               final score = mean across CPU and memory
#   3. Higher score wins; on tie, lexicographic node-name order (deterministic)

def schedule_pod(pod: Pod, nodes: List[Node],
                 node_states: Dict[str, NodeState]) -> Optional[str]:
    feasible: List[str] = []
    for n in sorted(nodes, key=lambda x: x.name):
        if n.is_master or n.name not in node_states:
            continue
        s = node_states[n.name]
        free_cpu = s.alloc_cpu - s.requested_cpu
        free_mem = s.alloc_mem - s.requested_mem
        if free_cpu >= pod.cpu and free_mem >= pod.mem:
            feasible.append(n.name)
    if not feasible:
        return None

    best_score = -1.0
    best_node: Optional[str] = None
    for name in feasible:
        s = node_states[name]
        cpu_score = (s.alloc_cpu - (s.requested_cpu + pod.cpu)) / s.alloc_cpu * 100
        mem_score = (s.alloc_mem - (s.requested_mem + pod.mem)) / s.alloc_mem * 100
        score = (cpu_score + mem_score) / 2
        if score > best_score:
            best_score = score
            best_node = name
    return best_node


def simulate_scheduling(
    nodes: List[Node], node_states: Dict[str, NodeState],
    queue: List[Pod], label: str,
) -> List[Tuple[str, Optional[str]]]:
    """Place pods in queue order. Mutates node_states. Returns placements."""
    print(f"  Order: {label}")
    placements: List[Tuple[str, Optional[str]]] = []
    for pod in queue:
        target = schedule_pod(pod, nodes, node_states)
        if target is None:
            placements.append((pod.name, None))
            print(f"    {pod.name:<12} ({pod.cpu:>4}m/{pod.mem:>4}Mi)  -> Pending")
        else:
            s = node_states[target]
            s.requested_cpu += pod.cpu
            s.requested_mem += pod.mem
            s.used_cpu += pod.cpu
            s.used_mem += pod.mem
            placements.append((pod.name, target))
            print(f"    {pod.name:<12} ({pod.cpu:>4}m/{pod.mem:>4}Mi)  -> Running on {target}")
    return placements


def print_scheduler_snapshot(nodes: List[Node],
                             node_states: Dict[str, NodeState],
                             placements: List[Tuple[str, Optional[str]]]) -> None:
    by_node: Dict[str, List[str]] = {n.name: [] for n in nodes if not n.is_master}
    pending: List[str] = []
    for pod_name, target in placements:
        if target is None:
            pending.append(pod_name)
        elif target in by_node:
            by_node[target].append(pod_name)
    print("  Final node states (after this scheduling order):")
    for n in nodes:
        if n.is_master:
            continue
        s = node_states[n.name]
        added = ", ".join(by_node[n.name]) if by_node[n.name] else "(none)"
        print(f"    {n.name}  {fmt_state(s)}")
        print(f"        added by scheduler: {added}")
    print(f"  Pending: {', '.join(pending) if pending else '(none)'}")

# ----------------------------- Probe check ------------------------------- #

def probe_fits(probe: Pod, nodes: List[Node],
               node_states: Dict[str, NodeState]) -> List[str]:
    fitting = []
    for n in nodes:
        if n.is_master or n.name not in node_states:
            continue
        s = node_states[n.name]
        free_cpu = s.alloc_cpu - s.requested_cpu
        free_mem = s.alloc_mem - s.requested_mem
        if free_cpu >= probe.cpu and free_mem >= probe.mem:
            fitting.append(n.name)
    return fitting


def rebuild_states(nodes: List[Node]) -> Dict[str, NodeState]:
    states: Dict[str, NodeState] = {}
    for node in nodes:
        if node.is_master:
            continue
        rc = sum(p.cpu for p in node.pods)
        rm = sum(p.mem for p in node.pods)
        states[node.name] = NodeState(node.alloc_cpu, node.alloc_mem,
                                      rc, rm, rc, rm)
    return states


# ------------------------------- main ------------------------------------ #

def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(
        description="Deterministic ResourceDefragmentation simulation."
    )
    p.add_argument("--threshold", type=float, default=0.2,
                   help="ImbalanceThreshold (default: 0.2)")
    p.add_argument("--max-evictions", type=int, default=2,
                   help="MaxEvictions (default: 2; Go zero-value=0 means no-op)")
    p.add_argument("--probe-cpu", type=int, default=900,
                   help="Probe CPU request in m (default: 900)")
    p.add_argument("--probe-mem", type=int, default=300,
                   help="Probe memory request in Mi (default: 300)")
    p.add_argument("--quiet", action="store_true",
                   help="Suppress TOPSIS internals; print only summary")
    p.add_argument("--no-scheduler", action="store_true",
                   help="Skip post-eviction scheduler simulation")
    return p.parse_args()


def main() -> None:
    args = parse_args()
    ALLOC_CPU = 2000   # m
    ALLOC_MEM = 2000   # Mi (unit-free in ratios; matches recap)
    THRESHOLD = args.threshold
    MAX_EVICTIONS = args.max_evictions

    # Pod templates
    def C(i: int) -> Pod:
        return Pod(name=f"C-{i}", cpu=400, mem=100)

    def M(i: int) -> Pod:
        return Pod(name=f"M-{i}", cpu=100, mem=400)

    # Initial cluster
    node_a = Node("A", ALLOC_CPU, ALLOC_MEM,
                  pods=[C(1), C(2), C(3), C(4)])
    node_b = Node("B", ALLOC_CPU, ALLOC_MEM,
                  pods=[C(5), C(6), M(1), M(2), M(3), M(4)])
    node_c = Node("C", ALLOC_CPU, ALLOC_MEM,
                  pods=[C(7), C(8), C(9), M(5), M(6)])

    nodes = [node_a, node_b, node_c]
    probe = Pod("Probe", cpu=args.probe_cpu, mem=args.probe_mem)

    print(hr("#"))
    print(f"CONFIG  threshold={THRESHOLD}  maxEvictions={MAX_EVICTIONS}  "
          f"probe={probe.cpu}m/{probe.mem}Mi")
    print(hr("#"))

    # Probe before
    print(hr("#"))
    print("INITIAL CLUSTER")
    print(hr("#"))
    for n in nodes:
        names = ", ".join(p.name for p in n.pods)
        print(f"  {n.name}: pods=[{names}]  "
              f"alloc={n.alloc_cpu}m/{n.alloc_mem}Mi")

    print()
    print(hr("#"))
    print(f"PROBE BEFORE  (probe needs {probe.cpu}m / {probe.mem}Mi)")
    print(hr("#"))
    states_before = rebuild_states(nodes)
    fits = probe_fits(probe, nodes, states_before)
    for n in nodes:
        if n.is_master:
            continue
        s = states_before[n.name]
        free_cpu = s.alloc_cpu - s.requested_cpu
        free_mem = s.alloc_mem - s.requested_mem
        verdict = "FIT" if (n.name in fits) else "no-fit"
        print(f"  {n.name}  free={free_cpu}m/{free_mem}Mi  -> {verdict}")
    print(f"  Result: probe={'Schedulable on ' + ','.join(fits) if fits else 'Pending (no node fits)'}")

    # Run defrag
    print()
    print(hr("#"))
    print("DESCHEDULER: ResourceDefragmentation")
    print(hr("#"))
    evictions = balance(nodes, THRESHOLD, MAX_EVICTIONS, verbose=not args.quiet)

    # Apply deletions to cluster
    for origin, pod in evictions:
        target = next(n for n in nodes if n.name == origin)
        target.pods = [p for p in target.pods if p.name != pod.name]

    # Final state immediately after deletions (before scheduler reschedules)
    print()
    print(hr("#"))
    print("STATE AFTER DESCHEDULER DELETIONS  (before any replacement)")
    print(hr("#"))
    states_after = rebuild_states(nodes)
    for n in nodes:
        if n.is_master:
            continue
        names = ", ".join(p.name for p in n.pods)
        print(f"  {n.name}: pods=[{names}]  {fmt_state(states_after[n.name])}")

    # Probe after
    print()
    print(hr("#"))
    print(f"PROBE AFTER  (probe needs {probe.cpu}m / {probe.mem}Mi)")
    print(hr("#"))
    fits_after = probe_fits(probe, nodes, states_after)
    for n in nodes:
        if n.is_master:
            continue
        s = states_after[n.name]
        free_cpu = s.alloc_cpu - s.requested_cpu
        free_mem = s.alloc_mem - s.requested_mem
        verdict = "FIT" if (n.name in fits_after) else "no-fit"
        print(f"  {n.name}  free={free_cpu}m/{free_mem}Mi  -> {verdict}")
    print(f"  Result: probe={'Schedulable on ' + ','.join(fits_after) if fits_after else 'Pending (no node fits)'}")

    # ----------------- Scheduler simulation (post-eviction) ------------- #
    scheduling_results: Dict[str, List[Tuple[str, Optional[str]]]] = {}
    if not args.no_scheduler and evictions:
        print()
        print(hr("#"))
        print("SCHEDULER SIMULATION  (default kube-scheduler-style)")
        print(hr("#"))
        print("  filter: NodeResourcesFit (free CPU/mem >= request)")
        print("  score:  LeastAllocated mean(CPU, mem); higher score = node more free")
        print("  tiebreak: lexicographic node name")

        # Replacement pods: same spec as evicted, name prefixed with R_
        replacements = [
            Pod(name=f"R_{p.name}", cpu=p.cpu, mem=p.mem, priority=p.priority)
            for _, p in evictions
        ]

        scenarios = [
            ("probe-first",       [probe] + replacements),
            ("replacement-first", replacements + [probe]),
        ]
        for label, queue in scenarios:
            print()
            print(hr("-"))
            print(f"Scenario: {label}")
            print(hr("-"))
            local_states = copy.deepcopy(states_after)
            placements = simulate_scheduling(nodes, local_states, queue, label)
            scheduling_results[label] = placements
            print()
            print_scheduler_snapshot(nodes, local_states, placements)

    # Compact summary
    print()
    print(hr("#"))
    print("COMPACT SUMMARY")
    print(hr("#"))
    print(f"  Evictions ({len(evictions)}):")
    for i, (origin, pod) in enumerate(evictions, 1):
        print(f"    #{i}  {pod.name} from {origin}")
    print(f"  Probe feasibility before -> "
          f"{'fit on ' + ','.join(fits) if fits else 'no node fits'}")
    print(f"  Probe feasibility after  -> "
          f"{'fit on ' + ','.join(fits_after) if fits_after else 'no node fits'}")
    if scheduling_results:
        print("  Scheduler simulation:")
        for label, placements in scheduling_results.items():
            probe_target = next((t for n, t in placements if n == probe.name), None)
            pending_pods = [n for n, t in placements if t is None]
            probe_str = f"Running on {probe_target}" if probe_target else "Pending"
            pending_str = ','.join(pending_pods) if pending_pods else '(none)'
            print(f"    [{label:<18}] probe={probe_str:<18}  pending={pending_str}")


if __name__ == "__main__":
    main()
