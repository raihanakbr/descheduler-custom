#!/usr/bin/env python3
import argparse
import json
import math


def cpu(value):
    value = str(value or "0")
    if value.endswith("m"):
        return int(float(value[:-1]))
    if value.endswith("n"):
        return int(float(value[:-1]) / 1_000_000)
    return int(float(value) * 1000)


def memory(value):
    value = str(value or "0")
    for suffix, multiplier in (
        ("Ki", 1024),
        ("Mi", 1024**2),
        ("Gi", 1024**3),
        ("k", 1000),
        ("M", 1000**2),
        ("G", 1000**3),
    ):
        if value.endswith(suffix):
            return int(float(value[:-len(suffix)]) * multiplier)
    return int(float(value))


def pod_requests(pod):
    requested_cpu = 0
    requested_memory = 0
    for container in pod.get("spec", {}).get("containers", []):
        requests = container.get("resources", {}).get("requests", {})
        requested_cpu += cpu(requests.get("cpu"))
        requested_memory += memory(requests.get("memory"))
    return requested_cpu, requested_memory


def fractions(state):
    return (
        state["requested_cpu"] / state["allocatable_cpu"],
        state["requested_memory"] / state["allocatable_memory"],
    )


def bin_score(requested_cpu, requested_memory, allocatable_cpu, allocatable_memory):
    cpu_fraction = requested_cpu / allocatable_cpu
    memory_fraction = requested_memory / allocatable_memory
    density = (cpu_fraction + memory_fraction) / 2
    balance = 1 - abs(cpu_fraction - memory_fraction)
    return (density + balance) / 2


def priority(state):
    cpu_fraction, memory_fraction = fractions(state)
    imbalance = cpu_fraction - memory_fraction
    free_space = (1 - cpu_fraction) * (1 - memory_fraction)
    return 0.5 * abs(imbalance) + 0.5 * (1 / (free_space + 1e-10))


def best_target(pod, source_name, states, ceiling):
    pod_cpu, pod_memory = pod_requests(pod)
    source_min = min(fractions(states[source_name]))
    selected = None
    selected_score = -math.inf
    for name, state in states.items():
        if name == source_name:
            continue
        projected_cpu = state["requested_cpu"] + pod_cpu
        projected_memory = state["requested_memory"] + pod_memory
        if projected_cpu > state["allocatable_cpu"] or projected_memory > state["allocatable_memory"]:
            continue
        if max(
            projected_cpu / state["allocatable_cpu"],
            projected_memory / state["allocatable_memory"],
        ) > ceiling:
            continue
        if min(fractions(state)) < source_min:
            continue
        score = bin_score(
            projected_cpu,
            projected_memory,
            state["allocatable_cpu"],
            state["allocatable_memory"],
        )
        if score > selected_score:
            selected = name
            selected_score = score
    return selected, selected_score


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--nodes", required=True)
    parser.add_argument("--pods", required=True)
    parser.add_argument("--workers", required=True)
    parser.add_argument("--namespace", default="actual-usage-exp")
    parser.add_argument("--hotspot-pod", required=True)
    parser.add_argument("--threshold", type=float, default=0.40)
    parser.add_argument("--ceiling", type=float, default=0.90)
    args = parser.parse_args()

    nodes = json.load(open(args.nodes))
    pods = json.load(open(args.pods))
    worker_names = {
        line.strip()
        for line in open(args.workers)
        if line.strip()
    }

    states = {}
    for node in nodes["items"]:
        name = node["metadata"]["name"]
        if name not in worker_names:
            continue
        allocatable = node["status"]["allocatable"]
        states[name] = {
            "allocatable_cpu": cpu(allocatable["cpu"]),
            "allocatable_memory": memory(allocatable["memory"]),
            "requested_cpu": 0,
            "requested_memory": 0,
            "evictable": [],
        }

    for pod in pods["items"]:
        node_name = pod.get("spec", {}).get("nodeName")
        if node_name not in states or pod.get("status", {}).get("phase") in ("Succeeded", "Failed"):
            continue
        requested_cpu, requested_memory = pod_requests(pod)
        states[node_name]["requested_cpu"] += requested_cpu
        states[node_name]["requested_memory"] += requested_memory
        if pod["metadata"]["namespace"] == args.namespace:
            states[node_name]["evictable"].append(pod)

    candidates = []
    for name, state in states.items():
        cpu_fraction, memory_fraction = fractions(state)
        average = (cpu_fraction + memory_fraction) / 2
        score = bin_score(
            state["requested_cpu"],
            state["requested_memory"],
            state["allocatable_cpu"],
            state["allocatable_memory"],
        )
        if state["evictable"] and (average < args.threshold or score < args.threshold):
            candidates.append((priority(state), name))

    selected_pod = None
    selected_source = None
    selected_target = None
    for _, source_name in sorted(candidates, reverse=True):
        pod_choice = None
        target_choice = None
        score_choice = -math.inf
        for pod in states[source_name]["evictable"]:
            target, score = best_target(pod, source_name, states, args.ceiling)
            if target and score > score_choice:
                pod_choice = pod
                target_choice = target
                score_choice = score
        if pod_choice:
            selected_pod = pod_choice["metadata"]["name"]
            selected_source = source_name
            selected_target = target_choice
            break

    if selected_pod != args.hotspot_pod:
        raise SystemExit(
            "ERROR: RDC2 layout validation selected "
            f"{selected_pod or 'no pod'} from {selected_source or 'no source'}, "
            f"expected hotspot {args.hotspot_pod}"
        )

    hnu_sources = []
    for name, state in states.items():
        cpu_fraction, memory_fraction = fractions(state)
        if (
            state["evictable"]
            and cpu_fraction < args.threshold
            and memory_fraction < args.threshold
        ):
            hnu_sources.append(name)
    if hnu_sources:
        raise SystemExit(
            "ERROR: expected HNU negative baseline, but these workers are below "
            f"both thresholds: {', '.join(sorted(hnu_sources))}"
        )

    print(json.dumps({
        "rdc2_first_pod": selected_pod,
        "rdc2_source": selected_source,
        "rdc2_predicted_target": selected_target,
        "hnu_underutilized_sources": hnu_sources,
    }, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
