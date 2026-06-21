#!/usr/bin/env python3
import datetime as dt
import json
import pathlib
import re
import sys


def parse_time(value):
    return dt.datetime.fromisoformat(value.strip().replace("Z", "+00:00"))


def load_run_env(path):
    values = {}
    if not path.exists():
        return values
    for line in path.read_text().splitlines():
        if "=" in line:
            key, value = line.split("=", 1)
            values[key] = value
    return values


def load_k6(path, event_time, pre_seconds, post_seconds):
    before = []
    after = []
    failures = {"before": 0, "after": 0}
    counts = {"before": 0, "after": 0}
    statuses = {"before": {}, "after": {}}
    dropped = {"before": 0.0, "after": 0.0}
    if not path.exists():
        return {}

    with path.open() as source:
        for line in source:
            try:
                item = json.loads(line)
            except json.JSONDecodeError:
                continue
            data = item.get("data", {})
            timestamp = data.get("time")
            if not timestamp:
                continue
            point_time = parse_time(timestamp)
            if event_time - dt.timedelta(seconds=pre_seconds) <= point_time < event_time:
                bucket = "before"
            elif event_time <= point_time <= event_time + dt.timedelta(seconds=post_seconds):
                bucket = "after"
            else:
                continue
            metric = item.get("metric")
            if metric == "http_req_duration":
                (before if bucket == "before" else after).append(float(data.get("value", 0)))
                status = str(data.get("tags", {}).get("status", "unknown"))
                statuses[bucket][status] = statuses[bucket].get(status, 0) + 1
            elif metric == "http_req_failed":
                counts[bucket] += 1
                failures[bucket] += int(float(data.get("value", 0)) > 0)
            elif metric == "dropped_iterations":
                dropped[bucket] += float(data.get("value", 0))

    def percentile(values, quantile):
        if not values:
            return None
        values = sorted(values)
        index = min(len(values) - 1, round((len(values) - 1) * quantile))
        return values[index]

    result = {}
    for name, values in (("before", before), ("after", after)):
        result[name] = {
            "requests": len(values),
            "p50_ms": percentile(values, 0.50),
            "p95_ms": percentile(values, 0.95),
            "p99_ms": percentile(values, 0.99),
            "failure_rate": failures[name] / counts[name] if counts[name] else None,
            "successful_rps": (
                sum(count for status, count in statuses[name].items() if status.startswith("2"))
                / (pre_seconds if name == "before" else post_seconds)
            ),
            "status_counts": statuses[name],
            "dropped_iterations": dropped[name],
        }
    return result


def load_lifecycle(path, api_pod, event_time):
    if not path.exists():
        return {}

    rows = []
    with path.open() as source:
        header = source.readline().rstrip("\n").split("\t")
        for line in source:
            values = line.rstrip("\n").split("\t")
            if len(values) == len(header):
                rows.append(dict(zip(header, values)))

    original = next((row for row in rows if row["pod"] == api_pod), None)
    if not original:
        return {}
    owner = original["owner"]
    uid = original["uid"]
    deletion = next(
        (
            parse_time(row["timestamp"])
            for row in rows
            if row["uid"] == uid and row["deletionTimestamp"]
        ),
        None,
    )
    replacements = [row for row in rows if row["owner"] == owner and row["uid"] != uid]
    scheduled = next(
        (parse_time(row["timestamp"]) for row in replacements if row["node"]),
        None,
    )
    ready = next(
        (parse_time(row["timestamp"]) for row in replacements if row["ready"] == "true"),
        None,
    )

    def seconds(value):
        return (value - event_time).total_seconds() if value else None

    return {
        "original_pod": api_pod,
        "original_uid": uid,
        "owner": owner,
        "deletion_observed_at": deletion.isoformat() if deletion else None,
        "replacement_scheduled_at": scheduled.isoformat() if scheduled else None,
        "replacement_ready_at": ready.isoformat() if ready else None,
        "event_to_scheduled_seconds": seconds(scheduled),
        "event_to_ready_seconds": seconds(ready),
    }


def load_json(path):
    if not path.exists():
        return {}
    try:
        return json.loads(path.read_text())
    except (json.JSONDecodeError, OSError):
        return {}


def main():
    directory = pathlib.Path(sys.argv[1])
    event_time = parse_time((directory / "event-time.txt").read_text())
    run_env = load_run_env(directory / "run.env")
    pre_seconds = int(run_env.get("PRE_EVENT_SECONDS", "60"))
    post_seconds = int(run_env.get("POST_EVENT_SECONDS", "120"))
    log = (directory / "descheduler.log").read_text(errors="replace")
    evictions = len(re.findall(r'"Evicted pod"', log))
    blocks = len(re.findall(r"blocking eviction", log, re.IGNORECASE))

    result = {
        "event_time": event_time.isoformat(),
        "evictions": evictions,
        "actual_usage_blocks": blocks,
        "api_load": load_k6(directory / "api-load.json", event_time, pre_seconds, post_seconds),
        "cluster": {
            "before": load_json(directory / "cluster-metrics-before.json"),
            "event": load_json(directory / "cluster-metrics-event.json"),
            "after": load_json(directory / "cluster-metrics-after.json"),
        },
        "lifecycle": load_lifecycle(
            directory / "pod-lifecycle.tsv",
            run_env.get("API_POD", ""),
            event_time,
        ),
    }
    print(json.dumps(result, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
