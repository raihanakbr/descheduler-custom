#!/usr/bin/env python3
"""Tool-agnostic cluster metrics for the consolidation experiments.

Computed purely from `kubectl get nodes/pods -o json` (passed as files or read
live), NEVER from any descheduler's logs, so the same numbers apply to B0/B1/B2
and the SUT.

Metrics (per the test plan):
  N_active   workers carrying >=1 experiment pod                      (O1, lower better)
  N_empty    schedulable workers carrying 0 experiment pods           (O1, higher better)
  S          stranding = sum over workers of |cpuFrac - memFrac|      (O2, lower better)
  H_balanced extra balanced reference pods that still fit by requests (O3, higher better)
  H_skewed   extra cpu-skewed reference pods that still fit           (O3, higher better)

Resource fractions use the REQUESTS of *all* scheduled pods on a node (daemonsets
included), matching how the ResourceDefragmentation plugin accounts node state in
requests mode.
"""

import argparse
import json
import subprocess
import sys

# Reference probe pods for schedulability headroom (O3), scaled to THIS cluster's
# 2000m / ~811Mi workers. balanced ~10% of both dims; skewed is cpu-heavy.
H_SHAPES = {
    "balanced": (200, 80 * 1024 * 1024),    # 200m / 80Mi
    "skewed":   (700, 20 * 1024 * 1024),    # 700m / 20Mi (cpu-skewed)
}

MEM_SUFFIXES = [
    ("Ki", 1024), ("Mi", 1024**2), ("Gi", 1024**3), ("Ti", 1024**4), ("Pi", 1024**5),
    ("k", 1000), ("M", 1000**2), ("G", 1000**3), ("T", 1000**4), ("P", 1000**5),
    ("m", 0.001),
]


def parse_cpu(v):
    """Return millicores from a Kubernetes cpu quantity."""
    if v is None:
        return 0
    v = str(v)
    if v.endswith("m"):
        return int(float(v[:-1]))
    if v.endswith("n"):
        return int(float(v[:-1]) / 1e6)
    if v.endswith("u"):
        return int(float(v[:-1]) / 1e3)
    return int(float(v) * 1000)


def parse_mem(v):
    """Return bytes from a Kubernetes memory quantity."""
    if v is None:
        return 0
    v = str(v)
    for suf, mult in MEM_SUFFIXES:
        if v.endswith(suf):
            return int(float(v[:-len(suf)]) * mult)
    return int(float(v))


def load(path, kubectl_args):
    if path:
        with open(path) as f:
            return json.load(f)
    return json.loads(subprocess.check_output(["kubectl"] + kubectl_args))


def is_control_plane(node):
    labels = node.get("metadata", {}).get("labels", {})
    return ("node-role.kubernetes.io/control-plane" in labels
            or "node-role.kubernetes.io/master" in labels)


def pod_requests(pod):
    cpu = mem = 0
    for c in pod.get("spec", {}).get("containers", []):
        req = c.get("resources", {}).get("requests", {})
        cpu += parse_cpu(req.get("cpu"))
        mem += parse_mem(req.get("memory"))
    return cpu, mem


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--namespace", default="defrag-exp", help="experiment namespace")
    ap.add_argument("--nodes-json", default=None, help="file from 'kubectl get nodes -o json'")
    ap.add_argument("--pods-json", default=None, help="file from 'kubectl get pods -A -o json'")
    ap.add_argument("--label", default="", help="snapshot label (before/after)")
    args = ap.parse_args()

    nodes = load(args.nodes_json, ["get", "nodes", "-o", "json"])
    pods = load(args.pods_json, ["get", "pods", "-A", "-o", "json"])

    workers = [n for n in nodes["items"] if not is_control_plane(n)]
    state = {}
    for n in workers:
        name = n["metadata"]["name"]
        alloc = n["status"]["allocatable"]
        state[name] = {
            "alloc_cpu": parse_cpu(alloc["cpu"]),
            "alloc_mem": parse_mem(alloc["memory"]),
            "req_cpu": 0,
            "req_mem": 0,
            "exp_pods": 0,
            "schedulable": not n.get("spec", {}).get("unschedulable", False),
        }

    for p in pods["items"]:
        node = p.get("spec", {}).get("nodeName")
        if node not in state:
            continue
        if p.get("status", {}).get("phase") in ("Succeeded", "Failed"):
            continue
        cpu, mem = pod_requests(p)
        state[node]["req_cpu"] += cpu
        state[node]["req_mem"] += mem
        if p["metadata"]["namespace"] == args.namespace:
            state[node]["exp_pods"] += 1

    n_active = sum(1 for s in state.values() if s["exp_pods"] > 0)
    n_empty = sum(1 for s in state.values() if s["exp_pods"] == 0 and s["schedulable"])

    stranding = 0.0
    for s in state.values():
        cf = s["req_cpu"] / s["alloc_cpu"] if s["alloc_cpu"] else 0
        mf = s["req_mem"] / s["alloc_mem"] if s["alloc_mem"] else 0
        s["cpu_frac"], s["mem_frac"] = cf, mf
        stranding += abs(cf - mf)

    def headroom(pod_cpu, pod_mem):
        total = 0
        for s in state.values():
            if not s["schedulable"]:
                continue
            free_cpu = s["alloc_cpu"] - s["req_cpu"]
            free_mem = s["alloc_mem"] - s["req_mem"]
            if free_cpu <= 0 or free_mem <= 0:
                continue
            total += min(free_cpu // pod_cpu, free_mem // pod_mem)
        return int(total)

    h = {name: headroom(c, m) for name, (c, m) in H_SHAPES.items()}

    # ---- human-readable ----
    tag = f" ({args.label})" if args.label else ""
    print(f"== METRICS{tag} ==")
    print(f"workers={len(workers)}  N_active={n_active}  N_empty={n_empty}")
    print(f"S (stranding, sum|cpuFrac-memFrac|) = {stranding:.3f}")
    print(f"H_balanced(200m/80Mi)={h['balanced']}   H_skewed(700m/20Mi)={h['skewed']}")
    print(f"{'node':<20}{'cpuReq':>8}{'/alloc':>8}{'memReqMi':>10}{'/allocMi':>10}"
          f"{'cpuF':>7}{'memF':>7}{'exp':>5}")
    for name in sorted(state):
        s = state[name]
        print(f"{name:<20}{s['req_cpu']:>8}{s['alloc_cpu']:>8}"
              f"{s['req_mem']//1048576:>10}{s['alloc_mem']//1048576:>10}"
              f"{s['cpu_frac']:>7.2f}{s['mem_frac']:>7.2f}{s['exp_pods']:>5}")

    summary = {
        "label": args.label,
        "workers": len(workers),
        "N_active": n_active,
        "N_empty": n_empty,
        "S": round(stranding, 4),
        "H_balanced": h["balanced"],
        "H_skewed": h["skewed"],
    }
    print("METRICS_JSON " + json.dumps(summary))


if __name__ == "__main__":
    main()
