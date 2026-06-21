#!/usr/bin/env python3
import argparse
import json


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


def names(path):
    with open(path) as stream:
        return [line.strip() for line in stream if line.strip()]


def pod_requests(pod):
    requested_cpu = 0
    requested_memory = 0
    for container in pod.get("spec", {}).get("containers", []):
        requests = container.get("resources", {}).get("requests", {})
        requested_cpu += cpu(requests.get("cpu"))
        requested_memory += memory(requests.get("memory"))
    return requested_cpu, requested_memory


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--nodes", required=True)
    parser.add_argument("--pods", required=True)
    parser.add_argument("--workers", required=True)
    parser.add_argument("--destinations", required=True)
    parser.add_argument("--sources", required=True)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--api-pod", required=True)
    parser.add_argument("--threshold", type=float, default=0.20)
    parser.add_argument("--target-threshold", type=float, default=0.50)
    args = parser.parse_args()

    workers = names(args.workers)
    destinations = names(args.destinations)
    sources = names(args.sources)
    if len(workers) != 6 or len(destinations) != 3 or len(sources) != 3:
        raise SystemExit("ERROR: expected 6 workers, 3 destinations, and 3 sources")

    nodes = json.load(open(args.nodes))
    pods = json.load(open(args.pods))
    states = {}
    for node in nodes["items"]:
        name = node["metadata"]["name"]
        if name not in workers:
            continue
        allocatable = node["status"]["allocatable"]
        states[name] = {
            "allocatable_cpu": cpu(allocatable["cpu"]),
            "allocatable_memory": memory(allocatable["memory"]),
            "requested_cpu": 0,
            "requested_memory": 0,
            "experiment_pods": [],
        }

    pod_by_name = {}
    for pod in pods["items"]:
        node_name = pod.get("spec", {}).get("nodeName")
        if node_name not in states or pod.get("status", {}).get("phase") in ("Succeeded", "Failed"):
            continue
        requested_cpu, requested_memory = pod_requests(pod)
        states[node_name]["requested_cpu"] += requested_cpu
        states[node_name]["requested_memory"] += requested_memory
        if pod["metadata"]["namespace"] == args.namespace:
            name = pod["metadata"]["name"]
            states[node_name]["experiment_pods"].append(name)
            pod_by_name[name] = pod

    experiment_pods = sum(len(state["experiment_pods"]) for state in states.values())
    if experiment_pods != 9:
        raise SystemExit(f"ERROR: expected 9 experiment pods, found {experiment_pods}")

    underutilized = []
    overutilized = []
    details = {}
    for name, state in states.items():
        cpu_fraction = state["requested_cpu"] / state["allocatable_cpu"]
        memory_fraction = state["requested_memory"] / state["allocatable_memory"]
        is_under = cpu_fraction < args.threshold and memory_fraction < args.threshold
        is_over = (
            cpu_fraction > args.target_threshold
            or memory_fraction > args.target_threshold
        )
        if is_under:
            underutilized.append(name)
        if is_over:
            overutilized.append(name)
        details[name] = {
            "cpu_fraction": round(cpu_fraction, 6),
            "memory_fraction": round(memory_fraction, 6),
            "experiment_pods": sorted(state["experiment_pods"]),
            "underutilized": is_under,
            "overutilized": is_over,
        }

    if set(underutilized) != set(destinations):
        raise SystemExit(
            f"ERROR: LNU destinations are {sorted(underutilized)}, "
            f"expected {sorted(destinations)}"
        )
    if set(overutilized) != set(sources):
        raise SystemExit(
            f"ERROR: LNU sources are {sorted(overutilized)}, expected {sorted(sources)}"
        )
    if any(len(states[name]["experiment_pods"]) != 1 for name in destinations):
        raise SystemExit("ERROR: each destination must contain exactly one experiment pod")
    if any(len(states[name]["experiment_pods"]) != 2 for name in sources):
        raise SystemExit("ERROR: each source must contain exactly two experiment pods")

    api_locations = [
        name for name, state in states.items()
        if args.api_pod in state["experiment_pods"]
    ]
    if api_locations != [sources[2]]:
        raise SystemExit(
            f"ERROR: API placement is {api_locations}, expected [{sources[2]}]"
        )

    api = pod_by_name[args.api_pod]
    if api.get("status", {}).get("qosClass") != "Burstable":
        raise SystemExit("ERROR: API pod must be Burstable so LNU evaluates it first")
    fallback = [
        pod_by_name[name]
        for name in states[sources[2]]["experiment_pods"]
        if name != args.api_pod
    ]
    if len(fallback) != 1 or fallback[0].get("status", {}).get("qosClass") != "Guaranteed":
        raise SystemExit("ERROR: busy source must have one Guaranteed idle fallback")

    # LNU caps destination capacity at targetThresholds. Each candidate must fit
    # that capped capacity, and removing it must bring its source below target.
    max_candidate_cpu = 0
    max_candidate_memory = 0
    for source in sources:
        source_pods = [pod_by_name[name] for name in states[source]["experiment_pods"]]
        for pod in source_pods:
            pod_cpu, pod_memory = pod_requests(pod)
            max_candidate_cpu = max(max_candidate_cpu, pod_cpu)
            max_candidate_memory = max(max_candidate_memory, pod_memory)
            remaining_cpu = states[source]["requested_cpu"] - pod_cpu
            remaining_memory = states[source]["requested_memory"] - pod_memory
            if (
                remaining_cpu / states[source]["allocatable_cpu"] > args.target_threshold
                or remaining_memory / states[source]["allocatable_memory"] > args.target_threshold
            ):
                raise SystemExit(
                    f"ERROR: removing {pod['metadata']['name']} does not bring "
                    f"{source} below target thresholds"
                )

    capped_free_cpu = sum(
        int(states[name]["allocatable_cpu"] * args.target_threshold)
        - states[name]["requested_cpu"]
        for name in destinations
    )
    capped_free_memory = sum(
        int(states[name]["allocatable_memory"] * args.target_threshold)
        - states[name]["requested_memory"]
        for name in destinations
    )
    if max_candidate_cpu * len(sources) > capped_free_cpu:
        raise SystemExit("ERROR: destinations lack capped CPU capacity for replacements")
    if max_candidate_memory * len(sources) > capped_free_memory:
        raise SystemExit("ERROR: destinations lack capped memory capacity for replacements")

    print(json.dumps({
        "api_pod": args.api_pod,
        "busy_source": sources[2],
        "destinations": destinations,
        "sources": sources,
        "underutilized_destinations": sorted(underutilized),
        "overutilized_sources": sorted(overutilized),
        "experiment_pods": experiment_pods,
        "node_details": details,
    }, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
