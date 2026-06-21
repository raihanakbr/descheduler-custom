#!/usr/bin/env python3
import argparse
import json

H_SHAPES = {"balanced": (200, 80 * 1024 * 1024), "skewed": (700, 20 * 1024 * 1024)}


def cpu(value):
    value = str(value or "0")
    if value.endswith("m"):
        return int(float(value[:-1]))
    if value.endswith("n"):
        return int(float(value[:-1]) / 1_000_000)
    return int(float(value) * 1000)


def memory(value):
    value = str(value or "0")
    for suffix, multiplier in (("Ki", 1024), ("Mi", 1024**2), ("Gi", 1024**3), ("k", 1000), ("M", 1000**2), ("G", 1000**3)):
        if value.endswith(suffix):
            return int(float(value[:-len(suffix)]) * multiplier)
    return int(float(value))


def is_worker(node):
    labels = node["metadata"].get("labels", {})
    return "node-role.kubernetes.io/control-plane" not in labels and "node-role.kubernetes.io/master" not in labels


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--nodes", required=True)
    parser.add_argument("--pods", required=True)
    parser.add_argument("--namespace", default="actual-usage-exp")
    parser.add_argument("--label", required=True)
    args = parser.parse_args()

    nodes = json.load(open(args.nodes))
    pods = json.load(open(args.pods))
    state = {}
    for node in nodes["items"]:
        if not is_worker(node):
            continue
        allocatable = node["status"]["allocatable"]
        state[node["metadata"]["name"]] = {
            "cpu": cpu(allocatable["cpu"]), "memory": memory(allocatable["memory"]),
            "used_cpu": 0, "used_memory": 0, "experiment_pods": 0,
            "schedulable": not node.get("spec", {}).get("unschedulable", False),
        }

    for pod in pods["items"]:
        node = pod.get("spec", {}).get("nodeName")
        if node not in state or pod.get("status", {}).get("phase") in ("Succeeded", "Failed"):
            continue
        for container in pod.get("spec", {}).get("containers", []):
            requests = container.get("resources", {}).get("requests", {})
            state[node]["used_cpu"] += cpu(requests.get("cpu"))
            state[node]["used_memory"] += memory(requests.get("memory"))
        if pod["metadata"]["namespace"] == args.namespace:
            state[node]["experiment_pods"] += 1

    active = sum(item["experiment_pods"] > 0 for item in state.values())
    empty = sum(item["experiment_pods"] == 0 and item["schedulable"] for item in state.values())
    stranding = sum(
        abs(item["used_cpu"] / item["cpu"] - item["used_memory"] / item["memory"])
        for item in state.values()
    )

    def headroom(shape):
        shape_cpu, shape_memory = H_SHAPES[shape]
        return sum(
            min(
                max(0, item["cpu"] - item["used_cpu"]) // shape_cpu,
                max(0, item["memory"] - item["used_memory"]) // shape_memory,
            )
            for item in state.values() if item["schedulable"]
        )

    result = {
        "label": args.label,
        "N_active": active,
        "N_empty": empty,
        "S": round(stranding, 6),
        "H_balanced": int(headroom("balanced")),
        "H_skewed": int(headroom("skewed")),
    }
    print(json.dumps(result, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
