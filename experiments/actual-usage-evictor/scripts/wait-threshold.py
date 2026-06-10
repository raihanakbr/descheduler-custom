#!/usr/bin/env python3
import argparse
import datetime as dt
import json
import subprocess
import time


def kubectl_json(*args):
    return json.loads(subprocess.check_output(
        ["kubectl", "--request-timeout=10s", *args],
        timeout=15,
    ))


def parse_cpu(value):
    value = str(value)
    if value.endswith("n"):
        return float(value[:-1]) / 1_000_000
    if value.endswith("u"):
        return float(value[:-1]) / 1_000
    if value.endswith("m"):
        return float(value[:-1])
    return float(value) * 1000


def parse_mem(value):
    value = str(value)
    suffixes = {"Ki": 1024, "Mi": 1024**2, "Gi": 1024**3, "k": 1000, "M": 1000**2, "G": 1000**3}
    for suffix, multiplier in suffixes.items():
        if value.endswith(suffix):
            return float(value[:-len(suffix)]) * multiplier
    return float(value)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--pod", required=True)
    parser.add_argument("--resource", choices=("cpu", "memory"), required=True)
    parser.add_argument("--threshold", type=float, required=True)
    parser.add_argument("--consecutive", type=int, default=2)
    parser.add_argument("--interval", type=int, default=15)
    parser.add_argument("--timeout", type=int, default=240)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()

    pod = kubectl_json("-n", args.namespace, "get", "pod", args.pod, "-o", "json")
    requests = pod["spec"]["containers"][0]["resources"]["requests"]
    request = parse_cpu(requests["cpu"]) if args.resource == "cpu" else parse_mem(requests["memory"])

    deadline = time.time() + args.timeout
    consecutive = 0
    with open(args.output, "w", buffering=1) as output:
        output.write("timestamp\tpod\tresource\tusage\trequest\tratio\tthreshold\tabove\n")
        while time.time() < deadline:
            timestamp = dt.datetime.now(dt.timezone.utc).isoformat()
            try:
                metrics = kubectl_json(
                    "get", "--raw",
                    f"/apis/metrics.k8s.io/v1beta1/namespaces/{args.namespace}/pods/{args.pod}",
                )
                usage_value = metrics["containers"][0]["usage"][args.resource]
                usage = parse_cpu(usage_value) if args.resource == "cpu" else parse_mem(usage_value)
                ratio = usage / request if request else float("inf")
                above = ratio >= args.threshold
                consecutive = consecutive + 1 if above else 0
                output.write(
                    f"{timestamp}\t{args.pod}\t{args.resource}\t{usage_value}\t"
                    f"{requests[args.resource]}\t{ratio:.6f}\t{args.threshold:.2f}\t{str(above).lower()}\n"
                )
                if consecutive >= args.consecutive:
                    return
            except (
                subprocess.CalledProcessError,
                subprocess.TimeoutExpired,
                KeyError,
                IndexError,
            ) as error:
                output.write(f"{timestamp}\t{args.pod}\t{args.resource}\tERROR:{error}\t-\t-\t{args.threshold:.2f}\tfalse\n")
                consecutive = 0
            time.sleep(args.interval)

    raise SystemExit(f"threshold not reached within {args.timeout}s")


if __name__ == "__main__":
    main()
