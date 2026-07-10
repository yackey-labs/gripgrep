#!/usr/bin/env python3
"""Render the CI benchmark comparison as GitHub step-summary markdown.

Reads the per-leg artifact dirs produced by ci-run.sh (one dir per
platform+tool, named bench-<platform>-<tool>) and pairs gg with rg per
platform. Emits one table per platform: mean +/- sigma for each tool,
the ratio, and a verdict. A platform whose gg leg is missing (build
failed / not yet ported) gets an honest note instead of numbers.

usage: ci-report.py <artifacts-root>
"""
import json
import os
import sys

ROWS = [
    ("tree_literal", "Linux tree, literal (`-n PM_RESUME`)"),
    ("files_walk", "Linux tree, `--files` (walk only)"),
    ("big_literal", "830MB single file, literal"),
    ("multi_literal", "830MB single file, `Sherlock|Watson`"),
]

def load_leg(root, platform, tool):
    d = os.path.join(root, f"bench-{platform}-{tool}")
    if not os.path.isdir(d):
        return None
    leg = {"version": "?"}
    vf = os.path.join(d, "version.txt")
    if os.path.exists(vf):
        leg["version"] = open(vf).read().strip()
    for key, _ in ROWS:
        jf = os.path.join(d, f"{key}.json")
        cf = os.path.join(d, f"{key}.count.json")
        if os.path.exists(jf):
            res = json.load(open(jf))["results"][0]
            leg[key] = {"mean": res["mean"], "stddev": res.get("stddev") or 0.0}
        if os.path.exists(cf):
            leg.setdefault(key, {})["lines"] = json.load(open(cf))["lines"]
    return leg

def fmt_ms(sec, sig):
    return f"{sec * 1000:.0f} ± {sig * 1000:.0f} ms"

def main():
    root = sys.argv[1]
    if not os.path.isdir(root):
        print("# gg vs rg — headline benchmarks\n\n"
              "> ⚠️ No benchmark artifacts were produced — every leg failed "
              "before uploading results; check the job logs.")
        return
    platforms = sorted(
        {d[len("bench-"):].rsplit("-", 1)[0]
         for d in os.listdir(root)
         if d.startswith("bench-") and "-" in d[len("bench-"):]}
    )
    out = ["# gg vs rg — headline benchmarks", ""]
    for plat in platforms:
        gg, rg = load_leg(root, plat, "gg"), load_leg(root, plat, "rg")
        out.append(f"## {plat}")
        if not gg or not any(k in gg for k, _ in ROWS):
            out.append("")
            out.append("> ⚠️ **gg leg produced no results on this platform** — "
                       "gg is not yet ported here (build fails); see the leg's job log.")
            out.append("")
            continue
        if not rg or not any(k in rg for k, _ in ROWS):
            out.append("")
            out.append("> ⚠️ **rg leg produced no results on this platform** — see its job log.")
            out.append("")
            continue
        out.append(f"`{gg['version']}` vs `{rg['version']}` — "
                   "each tool on its own fresh runner")
        out.append("")
        out.append("| Benchmark | gg | rg | gg vs rg | |")
        out.append("|---|---|---|---|---|")
        for key, label in ROWS:
            g, r = gg.get(key), rg.get(key)
            if (not g or "mean" not in g) and (not r or "mean" not in r):
                out.append(f"| {label} | — | — | not available on this platform | ➖ |")
                continue
            if not g or "mean" not in g or not r or "mean" not in r:
                out.append(f"| {label} | — | — | missing data | ⚠️ |")
                continue
            ratio = r["mean"] / g["mean"]
            if ratio >= 1.05:
                verdict, mark = f"**{ratio:.2f}× faster**", "🟢"
            elif ratio >= 0.95:
                verdict, mark = f"~parity ({ratio:.2f}×)", "⚪"
            else:
                verdict, mark = f"{1 / ratio:.2f}× slower", "🔴"
            if g.get("lines") is not None and g.get("lines") != r.get("lines"):
                verdict += f" — ⚠️ line counts differ (gg {g['lines']} vs rg {r['lines']})"
                mark = "⚠️"
            out.append(f"| {label} | {fmt_ms(g['mean'], g['stddev'])} | "
                       f"{fmt_ms(r['mean'], r['stddev'])} | {verdict} | {mark} |")
        out.append("")
    out.append("---")
    out.append("*Each tool ran `hyperfine --warmup 3 -m 15 -N` on its own fresh "
               "hosted runner against an identically-built corpus. Hosted-runner "
               "hardware varies run to run — treat ratios as indicative, not "
               "authoritative; the README numbers come from a controlled box.*")
    print("\n".join(out))

if __name__ == "__main__":
    main()
