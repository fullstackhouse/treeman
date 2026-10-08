#!/usr/bin/env python3
"""Baseline and compare osv-scanner reports.

    osv.py baseline report.json            > .fsh/upstream-osv.json
    osv.py compare  baseline.json now.json >> $GITHUB_STEP_SUMMARY

Upstream ships 40 known advisories today (37 of them against one old indirect
`golang.org/x/crypto`). Failing on any advisory would mean a permanently red
check, which is a check we would learn to ignore — so we fail only on advisories
that are NEW since the baseline.
"""
import json
import sys


def findings(report):
    """The stable set of (package, advisory) pairs in an osv-scanner JSON report.

    The version is reported but kept out of the key: a dependency bump that
    carries the same advisory forward is not news, a new advisory ID is.
    """
    out = {}
    for result in report.get("results", []):
        for pkg in result.get("packages", []):
            name = pkg["package"]["name"]
            version = pkg["package"].get("version", "?")
            for vuln in pkg.get("vulnerabilities", []):
                out[f"{name}:{vuln['id']}"] = {
                    "package": name,
                    "version": version,
                    "id": vuln["id"],
                    "summary": (vuln.get("summary") or "").strip()[:120],
                }
    return out


def baseline(path):
    found = findings(json.load(open(path)))
    print(json.dumps({
        "_comment": "Known-vulnerable dependencies at the time of baselining. fsh-vet.yml "
                    "fails only on advisories NOT listed here. Re-baseline deliberately, in "
                    "the PR that accepts the new advisory, and say why in the body.",
        "count": len(found),
        "findings": dict(sorted(found.items())),
    }, indent=2))


def compare(base_path, now_path):
    base = json.load(open(base_path))["findings"]
    now = findings(json.load(open(now_path)))
    new = {k: v for k, v in now.items() if k not in base}
    gone = sorted(set(base) - set(now))

    print("## Dependency advisories\n")
    print(f"**{len(now)}** advisory/advisories against `go.mod`, "
          f"**{len(new)}** new since the baseline of {len(base)}.\n")
    if new:
        print("### 🔻 New since the baseline\n")
        print("| package | version | advisory | summary |\n|---|---|---|---|")
        for v in sorted(new.values(), key=lambda v: (v["package"], v["id"])):
            print(f"| `{v['package']}` | {v['version']} | {v['id']} | {v['summary']} |")
        print()
    if gone:
        print("### 🔼 Resolved — re-baseline when convenient\n")
        for k in gone:
            print(f"- {k}")
        print()
    if not new and not gone:
        print("No change against the baseline.\n")
    if new:
        print("Fix the dependency, or re-baseline `.fsh/upstream-osv.json` **in the PR that "
              "accepts it**, with the reason in the body.\n")
        print(f"::error::new dependency advisories: {', '.join(sorted(new))}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    if sys.argv[1:2] == ["baseline"]:
        baseline(sys.argv[2])
    elif sys.argv[1:2] == ["compare"]:
        compare(sys.argv[2], sys.argv[3])
    else:
        sys.exit(__doc__)
