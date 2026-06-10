#!/usr/bin/env python3
import argparse
import datetime as dt
import json
import subprocess
import time


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--interval", type=float, default=1.0)
    args = parser.parse_args()

    seen = set()
    with open(args.output, "w", buffering=1) as output:
        output.write("timestamp\tpod\tuid\towner\tnode\tphase\tready\tdeletionTimestamp\n")
        while True:
            try:
                data = json.loads(subprocess.check_output(
                    ["kubectl", "-n", args.namespace, "get", "pods",
                     "-l", "experiment=actual-usage-evictor", "-o", "json"],
                    stderr=subprocess.DEVNULL,
                ))
                timestamp = dt.datetime.now(dt.timezone.utc).isoformat()
                current = set()
                for pod in data.get("items", []):
                    meta = pod["metadata"]
                    status = pod.get("status", {})
                    ready = any(
                        condition.get("type") == "Ready" and condition.get("status") == "True"
                        for condition in status.get("conditions", [])
                    )
                    owner = next(
                        (
                            reference.get("name", "")
                            for reference in meta.get("ownerReferences", [])
                            if reference.get("controller")
                        ),
                        "",
                    )
                    row = (
                        meta["name"], meta["uid"], owner, pod.get("spec", {}).get("nodeName", ""),
                        status.get("phase", ""), str(ready).lower(), meta.get("deletionTimestamp", ""),
                    )
                    current.add(row)
                    if row not in seen:
                        output.write(timestamp + "\t" + "\t".join(row) + "\n")
                seen = current
            except (subprocess.CalledProcessError, json.JSONDecodeError):
                pass
            time.sleep(args.interval)


if __name__ == "__main__":
    main()
