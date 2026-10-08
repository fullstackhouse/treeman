#!/usr/bin/env python3
"""Fail if upstream's OpenSSF Scorecard checks have regressed against our baseline.

A rise is reported and ignored — we are not here to police his project, only to
notice when the ground we build on shifts. Scorecard scores -1 for "could not
determine"; a check moving to or from -1 is reported as a change, not a failure,
because it usually means the check could not run rather than that anything moved.
"""
import json
import sys


def load(path):
    d = json.load(open(path))
    if "checks" in d and isinstance(d["checks"], dict):  # our baseline
        return d["aggregate"], d["checks"], d.get("commit", "?")
    checks = {c["name"]: (c["score"] if c["score"] is not None else -1) for c in d["checks"]}
    return d["score"], checks, d["repo"]["commit"]


base_agg, base, base_commit = load(sys.argv[1])
now_agg, now, now_commit = load(sys.argv[2])

regressions, improvements, undetermined, new = [], [], [], []
for name in sorted(set(base) | set(now)):
    b, n = base.get(name), now.get(name)
    if b is None:
        new.append((name, n))
    elif n is None or n == -1 or b == -1:
        if b != n:
            undetermined.append((name, b, n))
    elif n < b:
        regressions.append((name, b, n))
    elif n > b:
        improvements.append((name, b, n))

print("## Upstream hygiene — `stubbedev/treeman`\n")
print(f"Aggregate **{now_agg}/10** (baseline {base_agg}/10) at `{now_commit[:12]}`, "
      f"baseline taken at `{base_commit[:12]}`.\n")

for title, rows in (("🔻 Regressions", regressions), ("🔼 Improvements", improvements)):
    if rows:
        print(f"### {title}\n")
        print("| check | baseline | now |\n|---|---|---|")
        for name, b, n in rows:
            print(f"| {name} | {b} | {n} |")
        print()
for title, rows in (("Could not determine (not a failure)", undetermined),):
    if rows:
        print(f"### {title}\n")
        for name, b, n in rows:
            print(f"- {name}: {b} → {n}")
        print()
if new:
    print("### New checks in this Scorecard version\n")
    for name, n in new:
        print(f"- {name}: {n}")
    print()
if not (regressions or improvements or undetermined or new):
    print("No change against the baseline.\n")

if regressions:
    names = ", ".join(f"{n} ({b}→{c})" for n, b, c in regressions)
    print("Re-baseline `.fsh/upstream-scorecard.json` **in the PR that accepts this**, "
          "and say in the body why the regression is acceptable.\n")
    print(f"::error::upstream Scorecard regressed: {names}", file=sys.stderr)
    sys.exit(1)
