#!/usr/bin/env python3
"""Aggregate the S5 selector-ablation runs into a per-policy S/H table.

Scans results/s5/<ts>-<policy>-k<seed>/ dirs, reads the METRICS_JSON lines from
metrics_before.txt / metrics_after.txt and total_evictions from summary.txt,
groups by policy, and prints mean +/- 95% CI for the headline metrics. Pure
stdlib so it runs anywhere the experiment does.
"""
import json
import math
import os
import re
import sys
from collections import defaultdict

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "results", "s5")
DIR_RE = re.compile(r"^\d{8}-\d{6}-(?P<policy>.+)-k(?P<seed>\d+)$")

ORDER = ["topsis", "just-c1", "just-c2", "just-c3", "just-c4",
         "random", "largest", "lowest-priority",
         "no-c1", "no-c2", "no-c3", "no-c4"]


def read_metrics_json(path):
    if not os.path.exists(path):
        return None
    with open(path) as f:
        for line in f:
            if line.startswith("METRICS_JSON "):
                return json.loads(line[len("METRICS_JSON "):])
    return None


def read_total_evictions(path):
    if not os.path.exists(path):
        return None
    with open(path) as f:
        for line in f:
            if line.startswith("total_evictions(E):"):
                return int(line.split(":")[1].strip())
    return None


def mean_ci(xs):
    n = len(xs)
    if n == 0:
        return (float("nan"), float("nan"), 0)
    m = sum(xs) / n
    if n < 2:
        return (m, 0.0, n)
    sd = math.sqrt(sum((x - m) ** 2 for x in xs) / (n - 1))
    ci = 1.96 * sd / math.sqrt(n)
    return (m, ci, n)


def main():
    runs = defaultdict(list)
    if not os.path.isdir(ROOT):
        sys.exit(f"no results dir: {ROOT}")
    for name in sorted(os.listdir(ROOT)):
        m = DIR_RE.match(name)
        if not m:
            continue
        d = os.path.join(ROOT, name)
        before = read_metrics_json(os.path.join(d, "metrics_before.txt"))
        after = read_metrics_json(os.path.join(d, "metrics_after.txt"))
        E = read_total_evictions(os.path.join(d, "summary.txt"))
        if after is None:
            continue
        runs[m.group("policy")].append({"before": before, "after": after, "E": E})

    if not runs:
        sys.exit(f"no parsable run dirs under {ROOT}")

    policies = [p for p in ORDER if p in runs] + \
               [p for p in sorted(runs) if p not in ORDER]

    # before state (scenario constant) from any run
    any_before = next((r["before"] for rs in runs.values() for r in rs if r["before"]), None)
    if any_before:
        print("== BEFORE (scenario constant) ==")
        print(f"  N_active={any_before['N_active']} N_empty={any_before['N_empty']} "
              f"S={any_before['S']:.3f} H_bal={any_before['H_balanced']} "
              f"H_skew={any_before['H_skewed']}")
        print()

    hdr = f"{'policy':<16}{'n':>3}  {'S_after':>16}  {'H_bal':>13}  {'H_skew':>13}  {'N_act':>11}  {'N_empty':>11}  {'E':>10}"
    print("== AFTER (mean +/- 95% CI), lower S better, higher H/N_empty better ==")
    print(hdr)
    print("-" * len(hdr))

    def col(xs, fmt="{:.3f}"):
        m, ci, n = mean_ci(xs)
        if n == 0:
            return " " * 11
        return f"{fmt.format(m)}+-{fmt.format(ci)}"

    rows = {}
    for p in policies:
        rs = runs[p]
        S = [r["after"]["S"] for r in rs]
        Hb = [r["after"]["H_balanced"] for r in rs]
        Hs = [r["after"]["H_skewed"] for r in rs]
        Na = [r["after"]["N_active"] for r in rs]
        Ne = [r["after"]["N_empty"] for r in rs]
        E = [r["E"] for r in rs if r["E"] is not None]
        rows[p] = (S, Hb, Hs, Na, Ne, E)
        print(f"{p:<16}{len(rs):>3}  {col(S):>16}  {col(Hb,'{:.1f}'):>13}  "
              f"{col(Hs,'{:.1f}'):>13}  {col(Na,'{:.1f}'):>11}  "
              f"{col(Ne,'{:.1f}'):>11}  {col(E,'{:.1f}'):>10}")

    # machine-readable
    out = {p: {"S": mean_ci(v[0]), "H_balanced": mean_ci(v[1]),
               "H_skewed": mean_ci(v[2]), "N_active": mean_ci(v[3]),
               "N_empty": mean_ci(v[4]), "E": mean_ci(v[5])}
           for p, v in rows.items()}
    print()
    print("AGG_JSON " + json.dumps(out))


if __name__ == "__main__":
    main()
