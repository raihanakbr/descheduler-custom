#!/usr/bin/env python3
import argparse
import json
import re


def names(path):
    return [line.strip() for line in open(path) if line.strip()]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--system", choices=("H0", "H1"), required=True)
    parser.add_argument("--pods", required=True)
    parser.add_argument("--log", required=True)
    parser.add_argument("--sources", required=True)
    parser.add_argument("--destinations", required=True)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--api-uid", required=True)
    args = parser.parse_args()

    sources = names(args.sources)
    destinations = set(names(args.destinations))
    pods = json.load(open(args.pods))
    log = open(args.log, errors="replace").read()
    experiment = [
        pod for pod in pods["items"]
        if pod["metadata"]["namespace"] == args.namespace
        and pod.get("status", {}).get("phase") not in ("Succeeded", "Failed")
        and pod["metadata"].get("labels", {}).get("experiment") == "actual-usage-evictor"
    ]
    by_node = {}
    for pod in experiment:
        by_node.setdefault(pod.get("spec", {}).get("nodeName", ""), []).append(pod)

    active_nodes = len(by_node)
    evictions = len(re.findall(r'"Evicted pod"', log))
    blocks = len(re.findall(r"blocking eviction", log, re.IGNORECASE))
    api_pods = [
        pod for pod in experiment
        if pod["metadata"].get("labels", {}).get("app") == "workload-api"
    ]
    if len(api_pods) != 1:
        raise SystemExit(f"ERROR: expected one API pod after run, found {len(api_pods)}")
    api = api_pods[0]
    original_api_remains = api["metadata"]["uid"] == args.api_uid

    if args.system == "H0":
        if evictions != 3:
            raise SystemExit(f"ERROR: H0 expected 3 evictions, found {evictions}")
        if blocks != 0:
            raise SystemExit(f"ERROR: H0 expected 0 ActualUsageEvictor blocks, found {blocks}")
        if active_nodes != 3:
            raise SystemExit(f"ERROR: H0 expected 3 active nodes, found {active_nodes}")
        if any(source in by_node for source in sources):
            raise SystemExit("ERROR: H0 expected all source nodes to be empty")
        if original_api_remains:
            raise SystemExit("ERROR: H0 expected the original API pod to be evicted")
        if api.get("spec", {}).get("nodeName") not in destinations:
            raise SystemExit("ERROR: H0 API replacement did not land on a destination")
    else:
        if evictions != 2:
            raise SystemExit(f"ERROR: H1 expected 2 evictions, found {evictions}")
        if blocks < 1:
            raise SystemExit("ERROR: H1 expected at least one ActualUsageEvictor block")
        if active_nodes != 4:
            raise SystemExit(f"ERROR: H1 expected 4 active nodes, found {active_nodes}")
        if any(source in by_node for source in sources[:2]):
            raise SystemExit("ERROR: H1 expected both idle source nodes to be empty")
        if api.get("spec", {}).get("nodeName") != sources[2] or not original_api_remains:
            raise SystemExit("ERROR: H1 expected the original API pod to remain on the busy source")

    unexpected = set(by_node) - destinations - ({sources[2]} if args.system == "H1" else set())
    if unexpected:
        raise SystemExit(f"ERROR: unexpected active nodes after run: {sorted(unexpected)}")

    print(json.dumps({
        "system": args.system,
        "evictions": evictions,
        "actual_usage_blocks": blocks,
        "active_nodes_after": active_nodes,
        "api_pod_after": api["metadata"]["name"],
        "api_node_after": api.get("spec", {}).get("nodeName"),
        "original_api_remains": original_api_remains,
        "active_node_names": sorted(by_node),
    }, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
