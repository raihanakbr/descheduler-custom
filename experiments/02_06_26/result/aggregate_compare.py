#!/usr/bin/env python3
"""Aggregate the S1..S5 x {hnu,rd,rdc2} comparison into per-cell mean +/- 95% CI.

Scans results/<scenario>/<ts>-<strategy>-k<seed>/ produced by run_s15_compare.sh,
reads metrics_before/after.txt (METRICS_JSON line) and summary.txt (total E),
and prints one table per metric plus a compact headline table. Pure stdlib.
"""
import json
import math
import os
import re
from collections import defaultdict

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "results")
SCENARIOS = ["s1", "s2", "s3", "s4", "s5"]
STRATS = ["hnu", "rd", "rdc2"]
STRAT_LABEL = {"hnu": "HighNodeUtilization", "rd": "ResourceDefrag", "rdc2": "ResourceDefragC2"}
# Restrict to this comparison run (RUN_DATE prefix) so legacy same-named runs
# from earlier experiments are not mixed in. Override with RUN_DATE env.
RUN_DATE = os.environ.get("RUN_DATE", "20260607")
DIR_RE = re.compile(rf"^{RUN_DATE}-\d{{6}}-(?P<strat>hnu|rd|rdc2)-k(?P<seed>\d+)$")


def metrics(d, label):
    p = os.path.join(d, f"metrics_{label}.txt")
    if not os.path.exists(p):
        return None
    with open(p) as f:
        for line in f:
            if line.startswith("METRICS_JSON "):
                return json.loads(line[len("METRICS_JSON "):])
    return None


def total_E(d):
    p = os.path.join(d, "summary.txt")
    if not os.path.exists(p):
        return None
    with open(p) as f:
        for line in f:
            if line.startswith("total_evictions(E):"):
                return int(line.split(":")[1].strip())
    return None


def passes(d):
    p = os.path.join(d, "summary.txt")
    if not os.path.exists(p):
        return None
    with open(p) as f:
        for line in f:
            if line.startswith("per_pass_evictions:"):
                return line.split(":", 1)[1].strip()
    return None


def mean(xs):
    xs = [x for x in xs if x is not None]
    return sum(xs) / len(xs) if xs else float("nan")


def ci95(xs):
    xs = [x for x in xs if x is not None]
    n = len(xs)
    if n < 2:
        return 0.0
    m = sum(xs) / n
    sd = math.sqrt(sum((x - m) ** 2 for x in xs) / (n - 1))
    return 1.96 * sd / math.sqrt(n)


def collect():
    # data[scenario][strat] = list of dicts {before,after,E,n,passes}
    data = defaultdict(lambda: defaultdict(list))
    for sc in SCENARIOS:
        sdir = os.path.join(ROOT, sc)
        if not os.path.isdir(sdir):
            continue
        for name in sorted(os.listdir(sdir)):
            m = DIR_RE.match(name)
            if not m:
                continue
            d = os.path.join(sdir, name)
            b, a = metrics(d, "before"), metrics(d, "after")
            if not b or not a:
                continue
            data[sc][m.group("strat")].append({
                "before": b, "after": a, "E": total_E(d), "passes": passes(d),
            })
    return data


def fmt(m, c):
    if math.isnan(m):
        return "    --    "
    return f"{m:6.3f}±{c:0.3f}" if c else f"{m:6.3f}     "


def main():
    data = collect()

    print("# S1-S5 x {HighNodeUtilization, ResourceDefrag, ResourceDefragC2}")
    print("# mean +/- 95% CI across seeds.  requests signal, threshold 0.40.\n")

    # counts
    print("runs collected (scenario x strategy = n seeds):")
    for sc in SCENARIOS:
        cells = "  ".join(f"{st}={len(data[sc].get(st, []))}" for st in STRATS)
        print(f"  {sc}: {cells}")
    print()

    def cell_vals(sc, st, path, key):
        return [r[path][key] for r in data[sc].get(st, []) if r.get(path)]

    metrics_tbl = [
        ("N_active_before", "before", "N_active"),
        ("N_active_after",  "after",  "N_active"),
        ("N_empty_after",   "after",  "N_empty"),
        ("S_before",        "before", "S"),
        ("S_after",         "after",  "S"),
        ("H_balanced_after","after",  "H_balanced"),
        ("H_skewed_after",  "after",  "H_skewed"),
    ]

    for title, path, key in metrics_tbl:
        print(f"== {title} ==")
        hdr = f"{'scenario':<10}" + "".join(f"{STRAT_LABEL[st]:>22}" for st in STRATS)
        print(hdr)
        for sc in SCENARIOS:
            if not data.get(sc):
                continue
            row = f"{sc:<10}"
            for st in STRATS:
                vs = cell_vals(sc, st, path, key)
                row += f"{fmt(mean(vs), ci95(vs)):>22}"
            print(row)
        print()

    # evictions E + efficiency dN_active/E
    print("== total_evictions E  (and dN_active reclaimed) ==")
    hdr = f"{'scenario':<10}" + "".join(f"{STRAT_LABEL[st]:>22}" for st in STRATS)
    print(hdr)
    for sc in SCENARIOS:
        if not data.get(sc):
            continue
        row = f"{sc:<10}"
        for st in STRATS:
            Es = [r["E"] for r in data[sc].get(st, []) if r["E"] is not None]
            dN = [r["before"]["N_active"] - r["after"]["N_active"]
                  for r in data[sc].get(st, [])]
            cell = f"E={mean(Es):.1f} dN={mean(dN):.1f}"
            row += f"{cell:>22}"
        print(row)
    print()

    # headline: nodes reclaimed and S reduction
    print("== HEADLINE: nodes reclaimed (dN_active up=better) | S_after (down=better) ==")
    hdr = f"{'scenario':<10}" + "".join(f"{STRAT_LABEL[st]:>22}" for st in STRATS)
    print(hdr)
    for sc in SCENARIOS:
        if not data.get(sc):
            continue
        row = f"{sc:<10}"
        for st in STRATS:
            dN = [r["before"]["N_active"] - r["after"]["N_active"]
                  for r in data[sc].get(st, [])]
            Sa = cell_vals(sc, st, "after", "S")
            cell = f"+{mean(dN):.1f}n S={mean(Sa):.3f}"
            row += f"{cell:>22}"
        print(row)
    print()

    # machine-readable dump
    out = {}
    for sc in SCENARIOS:
        out[sc] = {}
        for st in STRATS:
            rs = data[sc].get(st, [])
            if not rs:
                continue
            out[sc][st] = {
                "n": len(rs),
                "N_active_before": mean([r["before"]["N_active"] for r in rs]),
                "N_active_after": mean([r["after"]["N_active"] for r in rs]),
                "N_empty_after": mean([r["after"]["N_empty"] for r in rs]),
                "S_before": mean([r["before"]["S"] for r in rs]),
                "S_after": mean([r["after"]["S"] for r in rs]),
                "S_after_ci": ci95([r["after"]["S"] for r in rs]),
                "H_balanced_after": mean([r["after"]["H_balanced"] for r in rs]),
                "H_skewed_after": mean([r["after"]["H_skewed"] for r in rs]),
                "E": mean([r["E"] for r in rs if r["E"] is not None]),
                "dN_active": mean([r["before"]["N_active"] - r["after"]["N_active"] for r in rs]),
            }
    print("AGG_JSON " + json.dumps(out))


if __name__ == "__main__":
    main()
