#!/usr/bin/env python3
"""Summarise effort-sweep results: pass rate, output tokens and latency per
agent per pass, as a markdown table.

    python3 evals/sweep/summarise.py <out-dir>
"""
import json
import pathlib
import re
import statistics
import sys


def agent_of(result):
    system = ((result.get("vars") or {}).get("system")) or ""
    m = re.search(r"agents/([a-z0-9-]+)\.md", system)
    if m:
        return m.group(1)
    desc = (result.get("testCase") or {}).get("description", "")
    return desc.split(" ")[0] or "?"


def main(out):
    rows = {}
    for f in sorted(pathlib.Path(out).glob("*.json")):
        data = json.loads(f.read_text())
        results = (data.get("results") or {}).get("results") or []
        for r in results:
            key = (agent_of(r), f.stem)
            row = rows.setdefault(key, {"n": 0, "pass": 0, "err": 0, "tok": [], "lat": []})
            row["n"] += 1
            if r.get("error") and not r.get("gradingResult"):
                row["err"] += 1
            if r.get("success"):
                row["pass"] += 1
            resp = r.get("response") or {}
            tok = (resp.get("tokenUsage") or {}).get("completion")
            if tok:
                row["tok"].append(tok)
            if r.get("latencyMs"):
                row["lat"].append(r["latencyMs"])
    print("| Agent | Pass | Cases | Pass rate | Errors | Median output tokens | Median latency (s) |")
    print("|---|---|---|---|---|---|---|")
    for (agent, pas), row in sorted(rows.items()):
        med_tok = int(statistics.median(row["tok"])) if row["tok"] else "—"
        med_lat = f"{statistics.median(row['lat']) / 1000:.1f}" if row["lat"] else "—"
        rate = f"{100 * row['pass'] / row['n']:.0f}%" if row["n"] else "—"
        print(f"| {agent} | {pas} | {row['n']} | {rate} | {row['err']} | {med_tok} | {med_lat} |")


if __name__ == "__main__":
    main(sys.argv[1])
