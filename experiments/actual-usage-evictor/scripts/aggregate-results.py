#!/usr/bin/env python3
import argparse
import json
import math
import pathlib
import statistics


T_975 = {2: 12.706, 3: 4.303, 4: 3.182, 5: 2.776, 6: 2.571, 7: 2.447, 8: 2.365, 9: 2.306, 10: 2.262}


def describe(values):
    values = [value for value in values if value is not None]
    if not values:
        return None
    mean = statistics.mean(values)
    if len(values) == 1:
        return {"n": 1, "mean": mean, "median": mean, "ci95_low": None, "ci95_high": None}
    critical = T_975.get(len(values), 1.96)
    margin = critical * statistics.stdev(values) / math.sqrt(len(values))
    return {
        "n": len(values),
        "mean": mean,
        "median": statistics.median(values),
        "ci95_low": mean - margin,
        "ci95_high": mean + margin,
    }


def nested(data, *keys):
    for key in keys:
        if not isinstance(data, dict):
            return None
        data = data.get(key)
    return data


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("resource", choices=("cpu", "memory"))
    parser.add_argument("--pattern", default="sustained", choices=("sustained", "idle", "transient"))
    parser.add_argument("--root", default=None)
    args = parser.parse_args()

    experiment_root = pathlib.Path(args.root) if args.root else pathlib.Path(__file__).resolve().parents[1]
    result_root = experiment_root / "results" / args.pattern / args.resource
    output = {"pattern": args.pattern, "resource": args.resource, "systems": {}}

    for system in ("N0", "R0", "R1", "H0", "H1"):
        runs = []
        for path in sorted((result_root / system).glob("repeat-*/*/summary.txt")):
            try:
                runs.append(json.loads(path.read_text()))
            except (json.JSONDecodeError, OSError):
                continue

        p95_before = [nested(run, "foreground", "before", "p95_ms") for run in runs]
        p95_after = [nested(run, "foreground", "after", "p95_ms") for run in runs]
        p95_delta = [
            after - before
            for before, after in zip(p95_before, p95_after)
            if before is not None and after is not None
        ]
        output["systems"][system] = {
            "runs": len(runs),
            "p95_before_ms": describe(p95_before),
            "p95_after_ms": describe(p95_after),
            "p95_delta_ms": describe(p95_delta),
            "failure_rate_after": describe([
                nested(run, "foreground", "after", "failure_rate") for run in runs
            ]),
            "event_to_ready_seconds": describe([
                nested(run, "lifecycle", "event_to_ready_seconds") for run in runs
            ]),
            "evictions": describe([run.get("evictions") for run in runs]),
            "actual_usage_blocks": describe([run.get("actual_usage_blocks") for run in runs]),
        }

    destination = result_root / "aggregate.json"
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(json.dumps(output, indent=2, sort_keys=True) + "\n")
    print(json.dumps(output, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
