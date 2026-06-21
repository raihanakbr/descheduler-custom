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
    return [line.strip() for line in open(path) if line.strip()]


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
    parser.add_argument("--threshold", type=float, default=0.40)
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

    for pod in pods["items"]:
        node_name = pod.get("spec", {}).get("nodeName")
        if node_name not in states or pod.get("status", {}).get("phase") in ("Succeeded", "Failed"):
            continue
        requested_cpu, requested_memory = pod_requests(pod)
        states[node_name]["requested_cpu"] += requested_cpu
        states[node_name]["requested_memory"] += requested_memory
        if pod["metadata"]["namespace"] == args.namespace:
            states[node_name]["experiment_pods"].append(pod["metadata"]["name"])

    experiment_pods = sum(len(state["experiment_pods"]) for state in states.values())
    if experiment_pods != 9:
        raise SystemExit(f"ERROR: expected 9 experiment pods, found {experiment_pods}")

    underutilized = []
    details = {}
    for name, state in states.items():
        cpu_fraction = state["requested_cpu"] / state["allocatable_cpu"]
        memory_fraction = state["requested_memory"] / state["allocatable_memory"]
        is_source = cpu_fraction < args.threshold and memory_fraction < args.threshold
        if is_source and state["experiment_pods"]:
            underutilized.append(name)
        details[name] = {
            "cpu_fraction": round(cpu_fraction, 6),
            "memory_fraction": round(memory_fraction, 6),
            "experiment_pods": state["experiment_pods"],
            "underutilized": is_source,
        }

    if set(underutilized) != set(sources):
        raise SystemExit(
            f"ERROR: HNU sources are {sorted(underutilized)}, expected {sorted(sources)}"
        )
    if any(details[name]["underutilized"] for name in destinations):
        raise SystemExit("ERROR: a destination node is below both HNU thresholds")

    api_locations = [
        name for name, state in states.items()
        if args.api_pod in state["experiment_pods"]
    ]
    if api_locations != [sources[2]]:
        raise SystemExit(
            f"ERROR: API placement is {api_locations}, expected [{sources[2]}]"
        )
    if len(states[sources[2]]["experiment_pods"]) != 1:
        raise SystemExit("ERROR: busy source must contain only the API pod")
    if any(len(states[name]["experiment_pods"]) != 1 for name in sources[:2]):
        raise SystemExit("ERROR: each idle source must contain exactly one pod")

    source_cpu = sum(
        pod_requests(pod)[0]
        for pod in pods["items"]
        if pod["metadata"]["namespace"] == args.namespace
        and pod.get("spec", {}).get("nodeName") in sources
    )
    source_memory = sum(
        pod_requests(pod)[1]
        for pod in pods["items"]
        if pod["metadata"]["namespace"] == args.namespace
        and pod.get("spec", {}).get("nodeName") in sources
    )
    free_cpu = sum(
        states[name]["allocatable_cpu"] - states[name]["requested_cpu"]
        for name in destinations
    )
    free_memory = sum(
        states[name]["allocatable_memory"] - states[name]["requested_memory"]
        for name in destinations
    )
    if source_cpu > free_cpu or source_memory > free_memory:
        raise SystemExit("ERROR: destination nodes lack aggregate replacement capacity")

    print(json.dumps({
        "api_pod": args.api_pod,
        "busy_source": sources[2],
        "destinations": destinations,
        "sources": sources,
        "underutilized_sources": sorted(underutilized),
        "experiment_pods": experiment_pods,
        "node_details": details,
    }, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
