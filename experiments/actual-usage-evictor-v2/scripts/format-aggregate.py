#!/usr/bin/env python3
import argparse
import json
import pathlib


SYSTEM_ORDER = ("N0", "R0", "R1", "H0", "H1")

COLUMNS = (
    ("system", "System"),
    ("runs", "Runs"),
    ("p95_before_ms", "p95 before ms"),
    ("p95_after_ms", "p95 after ms"),
    ("p95_delta_ms", "p95 delta ms"),
    ("failure_rate_after", "Failure after"),
    ("evictions", "Evictions"),
    ("actual_usage_blocks", "AU blocks"),
    ("event_to_ready_seconds", "Event->ready s"),
    ("stranding_after", "Stranding after"),
    ("active_nodes_after", "Active nodes"),
    ("balanced_headroom_after", "Balanced headroom"),
)


def metric_mean(system, key):
    value = system.get(key)
    if isinstance(value, dict):
        return value.get("mean")
    return value


def format_value(value):
    if value is None:
        return "-"
    if isinstance(value, int):
        return str(value)
    if isinstance(value, float):
        if abs(value) < 0.0005 and value != 0:
            return f"{value:.2e}"
        return f"{value:.3f}".rstrip("0").rstrip(".")
    return str(value)


def rows(data):
    systems = data.get("systems", {})
    for name in SYSTEM_ORDER:
        if name not in systems:
            continue
        system = systems[name]
        row = {"system": name, "runs": system.get("runs", 0)}
        for key, _ in COLUMNS:
            if key not in row:
                row[key] = metric_mean(system, key)
        yield row


def markdown_table(data):
    header = [label for _, label in COLUMNS]
    lines = [
        f"# Aggregate results: {data.get('pattern', '-')}/{data.get('resource', '-')}",
        "",
        "| " + " | ".join(header) + " |",
        "| " + " | ".join("---" for _ in header) + " |",
    ]
    for row in rows(data):
        lines.append("| " + " | ".join(format_value(row[key]) for key, _ in COLUMNS) + " |")
    lines.extend([
        "",
        "Notes:",
        "",
        "- Values are aggregate means from aggregate.json.",
        "- A dash means the metric was not available for that system.",
        "- The main ActualUsageEvictor comparison is R0 versus R1.",
    ])
    return "\n".join(lines) + "\n"


def tsv_table(data):
    lines = ["\t".join(label for _, label in COLUMNS)]
    for row in rows(data):
        lines.append("\t".join(format_value(row[key]) for key, _ in COLUMNS))
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("resource", choices=("cpu", "memory"))
    parser.add_argument("--pattern", default="sustained", choices=("sustained", "idle", "transient"))
    parser.add_argument("--root", default=None)
    parser.add_argument("--input", default=None)
    parser.add_argument("--no-write", action="store_true")
    args = parser.parse_args()

    experiment_root = pathlib.Path(args.root) if args.root else pathlib.Path(__file__).resolve().parents[1]
    result_root = experiment_root / "results" / args.pattern / args.resource
    source = pathlib.Path(args.input) if args.input else result_root / "aggregate.json"

    data = json.loads(source.read_text())
    markdown = markdown_table(data)
    tsv = tsv_table(data)

    if not args.no_write:
        (result_root / "aggregate.md").write_text(markdown)
        (result_root / "aggregate.tsv").write_text(tsv)
        print(f"wrote {result_root / 'aggregate.md'}")
        print(f"wrote {result_root / 'aggregate.tsv'}")

    print(markdown)


if __name__ == "__main__":
    main()
