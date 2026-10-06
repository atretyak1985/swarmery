#!/usr/bin/env python3
"""Draw the article's charts as SVG from the measured results.

    python3 charts.py results-2026-10-06.json ../assets

Substack takes PNG, so each SVG is then rendered with headless Chrome:

    chrome --headless --screenshot=<name>.png --window-size=800,<height> \
           --force-device-scale-factor=2 --hide-scrollbars <name>.svg

The script prints each file with the height to pass.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

WIDTH = 800
SURFACE, INK, SECONDARY, MUTED, AXIS = "#fcfcfb", "#0b0b0b", "#52514e", "#898781", "#c3c2b7"
BLUE, ORANGE = "#2a78d6", "#eb6834"  # sessions, cost: the same meaning in every chart
BLUE_RAMP = ("#86b6ef", "#2a78d6", "#104281")  # an ordered count of sessions, light to dark
BAR = 22
FONT = 'system-ui, -apple-system, "Segoe UI", sans-serif'
PERIOD = "10 Aug to 4 Oct 2026"


def text(x: float, y: float, body: str, size: int = 13, fill: str = INK, weight: int = 400, anchor: str = "start") -> str:
    return f'<text x="{x:.1f}" y="{y:.1f}" font-size="{size}" font-weight="{weight}" fill="{fill}" text-anchor="{anchor}">{body}</text>'


def bar(x: float, y: float, width: float, fill: str, round_end: bool = True) -> str:
    """A horizontal bar: square at the baseline, rounded at the data end."""
    width = max(width, 2.0)
    r = min(4.0, width / 2) if round_end else 0.0
    return (
        f'<path fill="{fill}" d="M{x:.1f},{y:.1f} H{x + width - r:.1f} Q{x + width:.1f},{y:.1f} {x + width:.1f},{y + r:.1f} '
        f'V{y + BAR - r:.1f} Q{x + width:.1f},{y + BAR:.1f} {x + width - r:.1f},{y + BAR:.1f} H{x:.1f} Z"/>'
    )


def svg(height: int, title: str, subtitle: str, desc: str, body: list[str]) -> tuple[int, str]:
    return height, "\n".join(
        [
            f'<svg xmlns="http://www.w3.org/2000/svg" width="{WIDTH}" height="{height}" viewBox="0 0 {WIDTH} {height}" font-family=\'{FONT}\' role="img">',
            f"<title>{title}</title><desc>{desc}</desc>",
            f'<rect width="{WIDTH}" height="{height}" fill="{SURFACE}"/>',
            text(32, 44, title, size=20, weight=600),
            text(32, 68, subtitle, fill=SECONDARY),
            *body,
            "</svg>",
        ]
    )


def legend(y: float, items: list[tuple[str, str]]) -> list[str]:
    out, x = [], 32.0
    for fill, label in items:
        out += [f'<rect x="{x:.1f}" y="{y - 10:.1f}" width="12" height="12" rx="2" fill="{fill}"/>', text(x + 18, y, label, fill=SECONDARY)]
        x += 18 + 7.2 * len(label) + 24
    return out


def share_chart(r: dict) -> tuple[int, str]:
    groups = [("Under 150k", "under_150k"), ("150k to 300k", "150k_to_300k"), ("300k and over", "300k_and_over")]
    x0, plot = 170.0, 520.0
    body = legend(100, [(BLUE, "Share of sessions"), (ORANGE, "Share of cost")])
    y = 126.0
    for label, key in groups:
        row = r["working_sessions_by_peak_context"][key]
        body.append(text(x0 - 14, y + BAR + 5, label, anchor="end"))
        for i, (fill, value) in enumerate(((BLUE, row["pct_sessions"]), (ORANGE, row["pct_cost"]))):
            top = y + i * (BAR + 2)
            body += [bar(x0, top, plot * value / 100, fill), text(x0 + plot * value / 100 + 8, top + 16, f"{value}%")]
        y += 2 * BAR + 2 + 26
    body.append(f'<line x1="{x0}" y1="118" x2="{x0}" y2="{y - 18:.0f}" stroke="{AXIS}" stroke-width="1"/>')
    body.append(text(32, y + 4, "Context = the most a session had to read before one answer, in tokens. Cost at API list price.", size=12, fill=MUTED))
    big = r["working_sessions_by_peak_context"]["300k_and_over"]
    return svg(
        int(y + 24),
        f"{big['pct_sessions']:.0f}% of sessions, {big['pct_cost']:.0f}% of the cost",
        f"{r['working_sessions']['sessions']} working sessions grouped by the largest context they reached · {PERIOD}",
        "Grouped horizontal bars comparing each context-size group's share of sessions with its share of cost.",
        body,
    )


def july_chart(r: dict) -> tuple[int, str]:
    july = r["late_july_daily_rollups"]
    background, projects = july["2026-07-27 background"], july["2026-07-27 projects"]
    panels = [
        ("Sessions", BLUE, [("Background", background["sessions"]), ("My projects", projects["sessions"])], "{:,.0f}"),
        ("Cost, API list price", ORANGE, [("Background", background["cost"]), ("My projects", projects["cost"])], "${:,.2f}"),
    ]
    body = []
    for i, (name, fill, rows, fmt) in enumerate(panels):
        left = 32.0 + i * 384
        x0, plot = left + 96, 190.0
        top_value = max(v for _, v in rows)
        body.append(text(left, 108, name, weight=600))
        for j, (label, value) in enumerate(rows):
            y = 126.0 + j * (BAR + 14)
            body += [text(x0 - 12, y + 16, label, anchor="end", fill=SECONDARY), bar(x0, y, plot * value / top_value, fill), text(x0 + plot * value / top_value + 8, y + 16, fmt.format(value))]
        body.append(f'<line x1="{x0}" y1="120" x2="{x0}" y2="{126 + 2 * BAR + 20}" stroke="{AXIS}" stroke-width="1"/>')
    body.append(text(32, 232, "Background = sessions the daemon starts by itself: the judge, the handoff notes and the like.", size=12, fill=MUTED))
    return svg(
        254,
        "27 July: what I saw and what it cost",
        "One day, two ways to count it",
        "Two small bar charts for 27 July 2026: number of sessions and cost, for background sessions and project sessions.",
        body,
    )


def answer_chart(r: dict) -> tuple[int, str]:
    rows = [("Under 50k", "under_50k"), ("50k to 150k", "50k_to_150k"), ("150k to 300k", "150k_to_300k"), ("300k and over", "300k_and_over")]
    data = r["cost_per_answer_by_context"]
    x0, plot = 170.0, 480.0
    top_value = max(data[key]["cents_per_answer"] for _, key in rows)
    body = []
    for j, (label, key) in enumerate(rows):
        y = 96.0 + j * (BAR + 14)
        value = data[key]["cents_per_answer"]
        body += [text(x0 - 14, y + 16, label, anchor="end"), bar(x0, y, plot * value / top_value, ORANGE), text(x0 + plot * value / top_value + 8, y + 16, f"{value}¢")]
    bottom = 96 + len(rows) * (BAR + 14)
    body.append(f'<line x1="{x0}" y1="90" x2="{x0}" y2="{bottom - 8}" stroke="{AXIS}" stroke-width="1"/>')
    body.append(text(32, bottom + 18, "Average cost of one model answer, by the context that answer had to read. API list price.", size=12, fill=MUTED))
    return svg(
        bottom + 40,
        "The fuller the session, the dearer each answer",
        f"All model answers in {r['total']['sessions']:,} sessions · {PERIOD}",
        "Four horizontal bars: the average cost of one model answer rises with the size of the context it had to read.",
        body,
    )


def handoff_chart(r: dict) -> tuple[int, str]:
    h = r["handoff_notes"]
    parts = [
        (h["no_answer_after_the_note"], "no answer after the note"),
        (h["1_to_100_answers_after"], "up to 100 more answers"),
        (h["more_than_100_answers_after"], "more than 100 more answers"),
    ]
    x0, plot, total = 32.0, 736.0, h["sessions_with_a_note"]
    body, x = [], x0
    for i, ((count, label), fill) in enumerate(zip(parts, BLUE_RAMP)):
        width = plot * count / total
        last = i == len(parts) - 1
        body += [text(x, 106, f"{count} · {label}"), bar(x, 116, width - (0 if last else 2), fill, round_end=last)]
        x += width
    body.append(text(x0, 172, f"${h['cost_after_first_note']:,.0f} of the ${h['cost_total']:,.0f} these sessions cost came after the first note was ready.", fill=SECONDARY))
    body.append(text(x0, 198, "The daemon writes a note once a session's context passes 150k tokens. Cost at API list price.", size=12, fill=MUTED))
    return svg(
        220,
        "After the handoff note was ready",
        f"{total} sessions that received a note · {PERIOD}",
        "One horizontal bar splitting the sessions that received a handoff note by how many more answers they had afterwards.",
        body,
    )


def main() -> None:
    results = json.loads(Path(sys.argv[1]).read_text())
    out = Path(sys.argv[2])
    out.mkdir(parents=True, exist_ok=True)
    charts = (("sessions-vs-cost", share_chart), ("july-27", july_chart), ("cost-per-answer", answer_chart), ("after-the-handoff", handoff_chart))
    for name, chart in charts:
        height, body = chart(results)
        (out / f"{name}.svg").write_text(body + "\n", encoding="utf-8")
        print(f"{name}.svg {height}")


if __name__ == "__main__":
    main()
