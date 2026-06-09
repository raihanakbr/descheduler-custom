#!/usr/bin/env python3
"""Aggregate the S6 necessity suite into a regret matrix + N1-N4 verdict.

Scans results/<scenario>/<ts>-<policy>-k<seed>/ for the suite scenarios, groups
S_after by (scenario, policy), and evaluates the PRE-REGISTERED necessity
thresholds. Pure stdlib. Penalty-sweep runs (policy tag contains 'lambda') are
excluded from the gate.
"""
import json
import math
import os
import re
import sys
from collections import defaultdict

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "results")
SCENARIOS = ["s5", "s6-c1", "s6-c3", "s6-c4", "s6-mix"]
SCEN_LABEL = {"s5": "C2", "s6-c1": "C1", "s6-c3": "C3", "s6-c4": "C4", "s6-mix": "MIX"}
DIR_RE = re.compile(r"^\d{8}-\d{6}-(?P<policy>.+)-k(?P<seed>\d+)$")

POLICY_ORDER = ["topsis", "just-c1", "just-c2", "just-c3", "just-c4",
                "no-c1", "no-c2", "no-c3", "no-c4",
                "random", "largest", "lowest-priority"]

# pre-registered thresholds (see plan / REPORT)
DELTA_BOMB = 0.10
DELTA_CONTRIB = 0.05
EPS = 1e-6


def metric_after(d, key="S"):
    p = os.path.join(d, "metrics_after.txt")
    if not os.path.exists(p):
        return None
    with open(p) as f:
        for line in f:
            if line.startswith("METRICS_JSON "):
                return json.loads(line[len("METRICS_JSON "):]).get(key)
    return None


def mean(xs):
    xs = [x for x in xs if x is not None]
    return sum(xs) / len(xs) if xs else float("nan")


def collect():
    # data[scenario][policy] = list of S_after
    data = defaultdict(lambda: defaultdict(list))
    for scen in SCENARIOS:
        sdir = os.path.join(ROOT, scen)
        if not os.path.isdir(sdir):
            continue
        for name in sorted(os.listdir(sdir)):
            m = DIR_RE.match(name)
            if not m:
                continue
            policy = m.group("policy")
            if "lambda" in policy:        # penalty-sweep run, not a gate policy
                continue
            s = metric_after(os.path.join(sdir, name), "S")
            if s is not None:
                data[scen][policy].append(s)
    return data


def main():
    data = collect()
    present_scen = [s for s in SCENARIOS if data.get(s)]
    if not present_scen:
        sys.exit(f"no suite runs found under {ROOT}")
    policies = [p for p in POLICY_ORDER
                if any(p in data[s] for s in present_scen)]

    meanS = {s: {p: mean(data[s][p]) for p in policies if data[s].get(p)}
             for s in present_scen}
    bestS = {s: min(v.values()) for s, v in meanS.items()}

    # ---- S_after matrix ----
    print("== S_after (mean) — policy x scenario.  lower = better ==")
    hdr = f"{'policy':<16}" + "".join(f"{SCEN_LABEL[s]+'/'+s:>12}" for s in present_scen)
    print(hdr); print("-" * len(hdr))
    for p in policies:
        row = f"{p:<16}"
        for s in present_scen:
            v = meanS[s].get(p)
            row += f"{'--':>12}" if v is None else f"{v:>12.3f}"
        print(row)
    print(f"\n{'best_S(scen)':<16}" + "".join(f"{bestS[s]:>12.3f}" for s in present_scen))

    # ---- regret matrix ----
    print("\n== regret = S_after - best_S(scenario).  0 = best in that scenario ==")
    print(hdr); print("-" * len(hdr))
    regret = defaultdict(dict)
    for p in policies:
        row = f"{p:<16}"
        for s in present_scen:
            v = meanS[s].get(p)
            if v is None:
                row += f"{'--':>12}"
            else:
                r = v - bestS[s]
                regret[p][s] = r
                row += f"{r:>12.3f}"
        print(row)
    worst = {p: (max(regret[p].values()) if regret[p] else float("nan")) for p in policies}
    print(f"\n{'worstRegret':<16}" + "  " + ", ".join(f"{p}={worst[p]:.3f}" for p in policies))

    # ---- pre-registered verdict ----
    print("\n== NECESSITY VERDICT (pre-registered; δ_bomb=%.2f δ_contrib=%.2f) =="
          % (DELTA_BOMB, DELTA_CONTRIB))

    # N1: >=3 scenarios with spread > δ_bomb
    spreads = {s: (max(meanS[s].values()) - min(meanS[s].values())) for s in present_scen}
    n1_scen = [s for s, sp in spreads.items() if sp > DELTA_BOMB]
    n1 = len(n1_scen) >= 3
    print(f"N1 selector causal: {'PASS' if n1 else 'FAIL'} — "
          f"{len(n1_scen)} scenarios spread>δ_bomb "
          + ", ".join(f"{SCEN_LABEL[s]}={spreads[s]:.3f}" for s in present_scen))

    # N2: each single-criterion + random + largest bombs somewhere
    n2_targets = ["just-c1", "just-c2", "just-c3", "just-c4", "random", "largest"]
    n2_targets = [p for p in n2_targets if p in worst and not math.isnan(worst[p])]
    n2_detail = {p: worst[p] > DELTA_BOMB for p in n2_targets}
    n2 = all(n2_detail.values())
    print(f"N2 no single criterion sufficient: {'PASS' if n2 else 'FAIL'} — "
          + ", ".join(f"{p}:{'bomb' if ok else 'OK?!'}({worst[p]:.3f})" for p, ok in n2_detail.items()))

    # N3: each no-cX degrades vs topsis in some scenario
    n3_detail = {}
    for p in ["no-c1", "no-c2", "no-c3", "no-c4"]:
        if p not in regret:
            continue
        gains = [(s, regret[p][s] - regret["topsis"].get(s, 0.0))
                 for s in present_scen if s in regret[p]]
        worst_gain = max((g for _, g in gains), default=float("nan"))
        n3_detail[p] = (worst_gain > DELTA_CONTRIB, worst_gain,
                        max(gains, key=lambda x: x[1])[0] if gains else None)
    n3 = all(v[0] for v in n3_detail.values()) if n3_detail else False
    print(f"N3 every criterion contributes: {'PASS' if n3 else 'FAIL'} — "
          + ", ".join(f"{p}:{'contrib' if ok else 'redundant'}"
                      f"(+{g:.3f}@{SCEN_LABEL.get(sc,sc)})" for p, (ok, g, sc) in n3_detail.items()))

    # N4: topsis worst-case regret minimal + best-or-tied on MIX
    tw = worst.get("topsis", float("nan"))
    min_other = min((worst[p] for p in policies if p != "topsis" and not math.isnan(worst[p])),
                    default=float("nan"))
    n4a = (not math.isnan(tw)) and tw <= min_other + EPS
    mix_regret = regret.get("topsis", {}).get("s6-mix")
    n4b = (mix_regret is not None) and (mix_regret <= DELTA_CONTRIB)
    n4 = n4a and n4b
    print(f"N4 TOPSIS robust: {'PASS' if n4 else 'FAIL'} — "
          f"worstRegret(topsis)={tw:.3f} vs min-other={min_other:.3f} "
          f"[{'min' if n4a else 'NOT min'}]; "
          f"MIX regret={'n/a' if mix_regret is None else f'{mix_regret:.3f}'} "
          f"[{'≤δ' if n4b else '>δ or n/a'}]")

    overall = n1 and n2 and n3 and n4
    print(f"\nOVERALL necessity claim: {'PASS' if overall else 'FAIL (report honestly + diagnose)'}")

    print("\nAGG_JSON " + json.dumps({
        "meanS": meanS, "bestS": bestS, "regret": {p: regret[p] for p in policies},
        "worstRegret": worst, "spreads": spreads,
        "verdict": {"N1": n1, "N2": n2, "N3": n3, "N4": n4, "overall": overall},
    }))


if __name__ == "__main__":
    main()
