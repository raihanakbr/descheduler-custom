#!/usr/bin/env python3
import argparse
import json
import re


def names(path):
    with open(path) as stream:
        return [line.strip() for line in stream if line.strip()]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--system", choices=("L0", "L1"), required=True)
    parser.add_argument("--pods", required=True)
    parser.add_argument("--log", required=True)
    parser.add_argument("--sources", required=True)
    parser.add_argument("--destinations", required=True)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--api-uid", required=True)
    args = parser.parse_args()

    sources = names(args.sources)
    destinations = names(args.destinations)
    pods = json.load(open(args.pods))
    with open(args.log, errors="replace") as stream:
        log = stream.read()

    experiment = [
        pod for pod in pods["items"]
        if pod["metadata"]["namespace"] == args.namespace
        and pod.get("status", {}).get("phase") not in ("Succeeded", "Failed")
        and pod["metadata"].get("labels", {}).get("experiment") == "actual-usage-evictor"
    ]
    by_node = {}
    for pod in experiment:
        by_node.setdefault(pod.get("spec", {}).get("nodeName", ""), []).append(pod)

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

    if evictions != 3:
        raise SystemExit(f"ERROR: {args.system} expected 3 evictions, found {evictions}")
    if args.system == "L0":
        if blocks != 0:
            raise SystemExit(f"ERROR: L0 expected 0 ActualUsageEvictor blocks, found {blocks}")
        if original_api_remains:
            raise SystemExit("ERROR: L0 expected the original API pod to be evicted")
        if api.get("spec", {}).get("nodeName") not in destinations:
            raise SystemExit("ERROR: L0 API replacement did not land on a destination")
    else:
        if blocks < 1:
            raise SystemExit("ERROR: L1 expected at least one ActualUsageEvictor block")
        if api.get("spec", {}).get("nodeName") != sources[2] or not original_api_remains:
            raise SystemExit("ERROR: L1 expected the original API to remain on the busy source")
        fallback = [
            pod for pod in experiment
            if pod["metadata"].get("labels", {}).get("app") == "workload-api-fallback"
        ]
        if len(fallback) != 1 or fallback[0].get("spec", {}).get("nodeName") not in destinations:
            raise SystemExit("ERROR: L1 expected the idle API fallback replacement on a destination")

    for source in sources:
        if len(by_node.get(source, [])) != 1:
            raise SystemExit(
                f"ERROR: expected one experiment pod on source {source}, "
                f"found {len(by_node.get(source, []))}"
            )
    for destination in destinations:
        if len(by_node.get(destination, [])) != 2:
            raise SystemExit(
                f"ERROR: expected two experiment pods on destination {destination}, "
                f"found {len(by_node.get(destination, []))}"
            )

    expected_nodes = set(sources) | set(destinations)
    unexpected = set(by_node) - expected_nodes
    if unexpected:
        raise SystemExit(f"ERROR: unexpected active nodes after run: {sorted(unexpected)}")

    print(json.dumps({
        "system": args.system,
        "evictions": evictions,
        "actual_usage_blocks": blocks,
        "active_nodes_after": len(by_node),
        "api_pod_after": api["metadata"]["name"],
        "api_node_after": api.get("spec", {}).get("nodeName"),
        "original_api_remains": original_api_remains,
        "active_node_names": sorted(by_node),
    }, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
