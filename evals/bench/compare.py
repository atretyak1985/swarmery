#!/usr/bin/env python3
"""Compare two frozen-bench result files task by task.

    python3 evals/bench/compare.py <candidate.json> <baseline.json>

Prints a markdown table of per-task verdicts (candidate / baseline) with turns
and cost, then the totals. Exit codes:
    0  candidate passes at least as many tasks as the baseline
    1  candidate passes fewer tasks than the baseline (regression)
    2  not comparable: different or duplicate task ids, or a task on either
       side is error:true (the run broke — infra, not the model); re-run it
    3  unreadable input (missing file, bad JSON, wrong shape)
"""
import json
import sys


class BadInput(Exception):
    pass


def load(path):
    try:
        with open(path) as f:
            data = json.load(f)
        tasks = data["tasks"]
        ids = [t["id"] for t in tasks]
    except (OSError, ValueError, KeyError, TypeError) as e:
        raise BadInput(f"cannot read {path}: {e}") from e
    dups = sorted({i for i in ids if ids.count(i) > 1})
    return data, {t["id"]: t for t in tasks}, dups


def verdict(task):
    if task.get("error"):
        return "ERROR"
    if task.get("timedOut"):
        return "TIMEOUT"
    return "PASS" if task.get("pass") else "FAIL"


def num(value, fmt):
    return "—" if value is None else fmt.format(value)


def total(tasks, key):
    return sum(t.get(key) or 0 for t in tasks.values())


def has_null(tasks, key):
    return any(t.get(key) is None for t in tasks.values())


def label(data):
    return f"{data.get('model', '?')}/{data.get('effort', '?')}"


def passes(tasks):
    return sum(1 for t in tasks.values() if t.get("pass") and not t.get("error"))


def compare(cand_path, base_path):
    cand_data, cand, cand_dups = load(cand_path)
    base_data, base, base_dups = load(base_path)

    if cand_dups or base_dups:
        print(f"not comparable: duplicate task ids — candidate {cand_dups}, baseline {base_dups}")
        return 2
    if set(cand) != set(base):
        only_c = sorted(set(cand) - set(base))
        only_b = sorted(set(base) - set(cand))
        print(f"not comparable: candidate-only {only_c}, baseline-only {only_b}")
        return 2

    print(f"candidate: {label(cand_data)} ({cand_path})")
    print(f"baseline:  {label(base_data)} ({base_path})")
    print()
    print("| Task | Candidate | Baseline | Turns (c/b) | Cost USD (c/b) |")
    print("|---|---|---|---|---|")
    for tid in sorted(cand):
        c, b = cand[tid], base[tid]
        flip = not (c.get("error") or b.get("error")) and bool(c.get("pass")) != bool(b.get("pass"))
        print(
            f"| {tid} | {verdict(c)}{' ←' if flip else ''} | {verdict(b)} "
            f"| {num(c.get('turns'), '{}')}/{num(b.get('turns'), '{}')} "
            f"| {num(c.get('costUsd'), '{:.2f}')}/{num(b.get('costUsd'), '{:.2f}')} |"
        )

    c_pass, b_pass = passes(cand), passes(base)
    nulls = has_null(cand, "turns") or has_null(base, "turns") or has_null(cand, "costUsd") or has_null(base, "costUsd")
    mark = "¹" if nulls else ""
    print(
        f"| **total** | **{c_pass}/{len(cand)}** | **{b_pass}/{len(base)}** "
        f"| {total(cand, 'turns')}/{total(base, 'turns')}{mark} "
        f"| {total(cand, 'costUsd'):.2f}/{total(base, 'costUsd'):.2f}{mark} |"
    )
    if nulls:
        print()
        print("¹ tasks with no result (timeout or error) count as 0 turns and $0.00 in these totals.")
    print()

    errored = sorted(tid for tid in cand if cand[tid].get("error") or base[tid].get("error"))
    if errored:
        print(f"NOT COMPARABLE: run error (not a model result) on {errored}; re-run those tasks")
        return 2
    if c_pass < b_pass:
        print(f"REGRESSION: candidate passes {c_pass}, baseline {b_pass}")
        return 1
    print(f"OK: candidate passes {c_pass}, baseline {b_pass}")
    return 0


def main(argv):
    if len(argv) != 3:
        print(__doc__.strip(), file=sys.stderr)
        return 3
    try:
        return compare(argv[1], argv[2])
    except BadInput as e:
        print(f"compare.py: {e}", file=sys.stderr)
        return 3
    except Exception as e:  # noqa: BLE001 — any other surprise is bad input, not a regression
        print(f"compare.py: unexpected input shape: {e!r}", file=sys.stderr)
        return 3


if __name__ == "__main__":
    sys.exit(main(sys.argv))
