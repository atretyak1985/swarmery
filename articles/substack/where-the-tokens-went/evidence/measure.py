#!/usr/bin/env python3
"""Reproduce every number in the article from the swarmery daemon's database.

    python3 measure.py [--db ~/.swarmery/swarmery.db] [--from 2026-08-10] [--to 2026-10-05]

Opens the database read-only and prints one JSON document. Dollar figures are
the daemon's API list-price computation, not a subscription bill. The window is
half-open: turns from --from up to, not including, --to; the default is eight
whole weeks. The daemon prunes old turns on a daily schedule, so a run made
later than the saved results will see less of the window.
"""

from __future__ import annotations

import argparse
import json
import sqlite3
from collections import Counter, defaultdict
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[4]
PRICING = REPO_ROOT / "tools/swarmery/config/pricing.json"
WARN_CTX, FAT_CTX = 150_000, 300_000  # the dashboard's amber and red badge thresholds
CONTEXT = "COALESCE(tokens_in, 0) + COALESCE(tokens_cache_read, 0) + COALESCE(tokens_cache_write, 0)"


def pct(part: float, whole: float) -> float:
    return round(100.0 * part / whole, 1) if whole else 0.0


def sessions(db: sqlite3.Connection, start: str, end: str, system_dir: str) -> list[dict]:
    """One row per session with turns in the window."""
    rows = db.execute(
        f"""
        SELECT s.id, s.cwd,
               COALESCE(SUM(t.cost_usd), 0),
               COALESCE(SUM(CASE WHEN COALESCE(t.agent_name, '') <> '' THEN t.cost_usd ELSE 0 END), 0),
               SUM(CASE WHEN t.role = 'assistant' THEN 1 ELSE 0 END),
               MAX({CONTEXT}),
               (julianday(MAX(t.started_at)) - julianday(MIN(t.started_at))) * 24
        FROM turns t JOIN sessions s ON s.id = t.session_id
        WHERE t.started_at >= ? AND t.started_at < ?
        GROUP BY s.id
        """,
        (start, end),
    ).fetchall()
    return [
        {"id": sid, "background": cwd in (system_dir, "/"), "cost": cost, "subagent_cost": sub, "answers": answers or 0, "peak_ctx": peak or 0, "hours": hours or 0.0}
        for sid, cwd, cost, sub, answers, peak, hours in rows
    ]


def group(rows: list[dict], of: list[dict]) -> dict:
    n, cost, total = len(rows), sum(r["cost"] for r in rows), sum(r["cost"] for r in of)
    return {
        "sessions": n,
        "pct_sessions": pct(n, len(of)),
        "cost": round(cost, 2),
        "pct_cost": pct(cost, total),
        "avg_answers": round(sum(r["answers"] for r in rows) / n) if n else 0,
        "avg_hours_first_to_last_turn": round(sum(r["hours"] for r in rows) / n, 1) if n else 0,
        "cost_per_session": round(cost / n, 2) if n else 0,
    }


def by_peak_context(rows: list[dict]) -> dict:
    return {
        "under_150k": group([r for r in rows if r["peak_ctx"] < WARN_CTX], rows),
        "150k_to_300k": group([r for r in rows if WARN_CTX <= r["peak_ctx"] < FAT_CTX], rows),
        "300k_and_over": group([r for r in rows if r["peak_ctx"] >= FAT_CTX], rows),
    }


def price_for(table: dict, model: str, speed: str | None) -> dict | None:
    """The price row for a model, the way the daemon resolves it: an exact name,
    else the longest matching prefix; a fast-mode turn uses the `-fast` row."""
    models, prefixes = table["models"], table["fallback_prefixes"]
    name = models.get(model) and model
    if not name:
        matches = [p for p in prefixes if model.startswith(p)]
        name = prefixes[max(matches, key=len)] if matches else None
    if name and speed == "fast" and f"{name}-fast" in models:
        name = f"{name}-fast"
    return models.get(name) if name else None


def token_split(db: sqlite3.Connection, start: str, end: str) -> dict:
    """Cost by token type, re-priced from today's price table. An estimate: the
    stored cost was computed turn by turn with the prices of its day, so the
    re-priced total is compared with the stored one to show how close it lands."""
    table = json.loads(PRICING.read_text())
    parts: Counter[str] = Counter()
    tokens: Counter[str] = Counter()
    stored = 0.0
    for model, speed, tin, tout, read, w5, w1, wflat, cost in db.execute(
        """
        SELECT model, speed, SUM(tokens_in), SUM(tokens_out), SUM(tokens_cache_read),
               SUM(COALESCE(cache_write_5m_tokens, 0)), SUM(COALESCE(cache_write_1h_tokens, 0)),
               SUM(COALESCE(tokens_cache_write, 0)), SUM(cost_usd)
        FROM turns WHERE started_at >= ? AND started_at < ? AND cost_usd > 0 GROUP BY model, speed
        """,
        (start, end),
    ):
        stored += cost
        price = price_for(table, model or "", speed)
        if not price:
            continue
        if w5 + w1 == 0:
            w5 = wflat
        tokens.update({"new_input": tin or 0, "output": tout or 0, "cache_read": read or 0, "cache_write": w5 + w1})
        parts["new_input"] += (tin or 0) / 1e6 * price["input"]
        parts["output"] += (tout or 0) / 1e6 * price["output"]
        parts["cache_read"] += (read or 0) / 1e6 * price["cache_read"]
        parts["cache_write"] += w5 / 1e6 * price["cache_write"] + w1 / 1e6 * price.get("cache_write_1h", price["cache_write"])
    total, all_tokens = sum(parts.values()), sum(tokens.values())
    return {
        "repriced_total": round(total, 2),
        "stored_total": round(stored, 2),
        "repriced_pct_of_stored": pct(total, stored),
        **{k: {"cost": round(v, 2), "pct_cost": pct(v, total), "pct_of_raw_tokens": pct(tokens[k], all_tokens)} for k, v in parts.items()},
    }


def cost_per_answer(db: sqlite3.Connection, start: str, end: str) -> dict:
    """What one model answer cost, by how much context that answer had to read."""
    rows = db.execute(
        f"""
        SELECT CASE WHEN c < 50000 THEN 'under_50k' WHEN c < 150000 THEN '50k_to_150k'
                    WHEN c < 300000 THEN '150k_to_300k' ELSE '300k_and_over' END, COUNT(*), SUM(cost)
        FROM (SELECT {CONTEXT} AS c, cost_usd AS cost FROM turns
              WHERE started_at >= ? AND started_at < ? AND role = 'assistant' AND cost_usd > 0)
        GROUP BY 1
        """,
        (start, end),
    ).fetchall()
    total = sum(cost for _, _, cost in rows)
    out = {name: {"answers": n, "cost": round(cost, 2), "pct_cost": pct(cost, total), "cents_per_answer": round(100 * cost / n, 1)} for name, n, cost in rows}
    above = [out[k] for k in ("150k_to_300k", "300k_and_over") if k in out]
    fresh_rate = out["50k_to_150k"]["cents_per_answer"] / 100
    paid = sum(b["cost"] for b in above)
    at_fresh_rate = sum(b["answers"] for b in above) * fresh_rate
    out["answers_given_above_150k"] = {
        "answers": sum(b["answers"] for b in above),
        "cost": round(paid, 2),
        "pct_cost": pct(paid, total),
        "cost_at_the_50k_to_150k_rate": round(at_fresh_rate, 2),
        "difference": round(paid - at_fresh_rate, 2),
        "difference_pct_of_all_cost": pct(paid - at_fresh_rate, total),
    }
    return out


def compactions(db: sqlite3.Connection, start: str, end: str, big_ids: set[int]) -> dict:
    """A compaction shows as the main conversation's context falling by more
    than half from one answer to the next, from 150k or more."""
    last: dict[int, int] = {}
    drops: defaultdict[int, int] = defaultdict(int)
    for sid, ctx in db.execute(
        f"""
        SELECT session_id, {CONTEXT} FROM turns
        WHERE started_at >= ? AND started_at < ? AND role = 'assistant' AND COALESCE(agent_name, '') = '' AND cost_usd > 0
        ORDER BY session_id, seq
        """,
        (start, end),
    ):
        if sid in big_ids:
            if last.get(sid, 0) >= WARN_CTX and ctx < last[sid] / 2:
                drops[sid] += 1
            last[sid] = ctx
    return {"big_sessions": len(big_ids), "big_sessions_compacted_at_least_once": len(drops), "compactions": sum(drops.values())}


def handoffs(db: sqlite3.Connection, start: str, end: str) -> dict:
    """What happened to a session after the daemon wrote it a handoff note."""
    notes, with_note = db.execute("SELECT COUNT(*), COUNT(DISTINCT session_id) FROM handoffs WHERE created_at >= ? AND created_at < ?", (start, end)).fetchone()
    rows = db.execute(
        """
        WITH first AS (SELECT session_id, MIN(created_at) AS at FROM handoffs WHERE created_at >= ? AND created_at < ? GROUP BY 1)
        SELECT (SELECT COUNT(*) FROM turns t WHERE t.session_id = f.session_id AND t.role = 'assistant' AND t.started_at > f.at AND t.started_at < ?),
               (SELECT COALESCE(SUM(t.cost_usd), 0) FROM turns t WHERE t.session_id = f.session_id AND t.started_at > f.at AND t.started_at < ?),
               (SELECT COALESCE(SUM(t.cost_usd), 0) FROM turns t WHERE t.session_id = f.session_id AND t.started_at >= ? AND t.started_at < ?)
        FROM first f
        """,
        (start, end, end, end, start, end),
    ).fetchall()
    after, total = sum(r[1] for r in rows), sum(r[2] for r in rows)
    return {
        "notes_written": notes,
        "sessions_with_a_note": with_note,
        "no_answer_after_the_note": sum(1 for r in rows if r[0] == 0),
        "1_to_100_answers_after": sum(1 for r in rows if 1 <= r[0] <= 100),
        "more_than_100_answers_after": sum(1 for r in rows if r[0] > 100),
        "cost_total": round(total, 2),
        "cost_after_first_note": round(after, 2),
        "pct_cost_after_first_note": pct(after, total),
    }


def main() -> None:
    parser = argparse.ArgumentParser(description="Reproduce the article's numbers from the swarmery database.")
    parser.add_argument("--db", type=Path, default=Path.home() / ".swarmery/swarmery.db")
    parser.add_argument("--from", dest="start", default="2026-08-10")
    parser.add_argument("--to", dest="end", default="2026-10-05")
    args = parser.parse_args()

    db = sqlite3.connect(f"file:{args.db}?mode=ro", uri=True)
    system_dir = str(Path.home() / ".swarmery")
    rows = sessions(db, args.start, args.end, system_dir)
    working = [r for r in rows if not r["background"]]
    background = [r for r in rows if r["background"]]
    total = sum(r["cost"] for r in rows)
    days = db.execute("SELECT COUNT(DISTINCT date(started_at)) FROM turns WHERE started_at >= ? AND started_at < ?", (args.start, args.end)).fetchone()[0]
    ranked = sorted(rows, key=lambda r: r["cost"], reverse=True)
    big = [r for r in rows if r["peak_ctx"] >= FAT_CTX]
    orchestrated = [r for r in big if r["subagent_cost"] >= 0.3 * r["cost"]]
    solo = [r for r in big if r["subagent_cost"] < 0.3 * r["cost"]]
    subagent_cost = sum(r["subagent_cost"] for r in rows)

    july = db.execute(
        """
        SELECT r.day, CASE WHEN p.path IN (?, '/') THEN 'background' ELSE 'projects' END, SUM(r.sessions), SUM(r.cost_usd)
        FROM daily_rollups r JOIN projects p ON p.id = r.project_id WHERE r.day BETWEEN '2026-07-26' AND '2026-07-28' GROUP BY 1, 2
        """,
        (system_dir,),
    ).fetchall()
    weekly = [
        {"week_from": first, "days_with_turns": n, "cost_per_day": round(cost / n)}
        for first, n, cost in db.execute(
            """
            SELECT MIN(date(started_at)), COUNT(DISTINCT date(started_at)), SUM(cost_usd)
            FROM turns WHERE started_at >= ? AND started_at < ? GROUP BY strftime('%Y-%W', started_at) ORDER BY 1
            """,
            (args.start, args.end),
        )
    ]

    result = {
        "window": {"from": args.start, "to_exclusive": args.end, "days_with_turns": days},
        "total": {"sessions": len(rows), "cost": round(total, 2)},
        "background_sessions": {"sessions": len(background), "cost": round(sum(r["cost"] for r in background), 2), "with_context_over_300k": sum(1 for r in background if r["peak_ctx"] >= FAT_CTX)},
        "working_sessions": {"sessions": len(working), "cost": round(sum(r["cost"] for r in working), 2)},
        "working_sessions_by_peak_context": by_peak_context(working),
        "all_sessions_by_peak_context": by_peak_context(rows),
        "concentration": {
            "top_10_sessions_pct_cost": pct(sum(r["cost"] for r in ranked[:10]), total),
            "cheaper_half_sessions": len(ranked) - len(ranked) // 2,
            "cheaper_half_cost": round(sum(r["cost"] for r in ranked[len(ranked) // 2 :]), 2),
        },
        "threads": {"main_thread_pct": pct(total - subagent_cost, total), "subagent_pct": pct(subagent_cost, total)},
        "big_sessions_by_shape": {
            "orchestrations": {
                "note": "subagents are 30% or more of the session's cost",
                "sessions": len(orchestrated),
                "cost": round(sum(r["cost"] for r in orchestrated), 2),
                "cost_per_session": round(sum(r["cost"] for r in orchestrated) / len(orchestrated)),
                "subagent_pct_of_their_cost": pct(sum(r["subagent_cost"] for r in orchestrated), sum(r["cost"] for r in orchestrated)),
                "highest_subagent_pct_in_one_session": max(pct(r["subagent_cost"], r["cost"]) for r in orchestrated),
            },
            "mostly_main_conversation": {
                "sessions": len(solo),
                "cost": round(sum(r["cost"] for r in solo), 2),
                "cost_per_session": round(sum(r["cost"] for r in solo) / len(solo)),
            },
        },
        "cost_per_answer_by_context": cost_per_answer(db, args.start, args.end),
        "cost_by_token_type_estimate": token_split(db, args.start, args.end),
        "compactions_in_big_sessions": compactions(db, args.start, args.end, {r["id"] for r in big}),
        "handoff_notes": handoffs(db, args.start, args.end),
        "late_july_daily_rollups": {f"{day} {kind}": {"sessions": n, "cost": round(cost, 2)} for day, kind, n, cost in july},
        "weekly": weekly,
    }
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
