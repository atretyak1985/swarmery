---
name: html-reporting
version: "1.0.0"
owner: "agentry-core"
description: "Use this skill when an agent must render a self-contained HTML report or dashboard — task summaries (Phase 8), code/operational audits (Phase 5), or any shareable artifact viewed outside the terminal. It provides one canonical dark-terminal shell so every report looks consistent. Don't use it for markdown-only output, for measuring metrics (it only formats numbers you supply), or for the mermaid viewer (use mermaid-viewer)."
disable-model-invocation: true
color: teal
---

# Purpose

Provide a single, reusable, **self-contained** HTML shell (inline CSS, no external assets, no JS required) so that every report an agent emits — `@summary-generator`'s Phase 8 dashboard and `@code-auditor`'s Phase 5 audit — shares one visual language: a dark "terminal" theme with severity-coded cards, metric tables, and collapsible sections.

This skill produces presentation only. It does **not** gather data, measure metrics, or run analysis — the calling agent supplies the content and this skill wraps it.

# When to use this skill

- An agent has completed analysis and needs to emit a report as HTML (saved to a `phases/*.html` or `SUMMARY`-adjacent path)
- `@summary-generator` renders the optional Phase 8 HTML dashboard
- `@code-auditor` renders the Phase 5 audit report (health score + P0–P3 backlog)
- A report has >3 sections or will be shared outside the terminal (per `summary-templates` Step 4)

# When NOT to use this skill

- **Markdown-only output** — the canonical `SUMMARY.md` / `05-audit.md` stay markdown; HTML is the optional mirror
- **Measuring or computing metrics** — this skill formats numbers the caller already has; it never fabricates them
- **Rendering mermaid diagrams** — use `mermaid-viewer`
- **Writing code, tests, or configuration** — output is a single `.html` document only
- **Content authoring** — section text comes from `summary-templates` (summaries) or the audit agent's findings; this skill only wraps it

# Required environment

No tooling required. Output is a single self-contained `.html` file (inline `<style>`, no CDN, no build step). It opens directly in any browser and survives being copied between machines.

# Inputs

| Input | Required | Description |
|-------|----------|-------------|
| Report kind | Yes | `summary` (Phase 8) or `audit` (Phase 5) — selects the section skeleton |
| Title | Yes | Report `<h1>` text |
| Sections | Yes | The already-authored content blocks (metrics, findings, backlog, etc.) |
| Output path | Yes | Where to write, e.g. `…/{slug}/phases/05-audit.html` or `phases/08-summary.html` |
| Health score | Audit only | Integer 1–10 for the audit header badge |

# Outputs

**Format:** one self-contained HTML file at the caller's output path.

**Length budget:** body content ≤ 300 lines (summary) / ≤ 500 lines (audit). Consolidate similar findings to stay within budget; never pad. The shell CSS does not count against the content budget.

# The shell (canonical)

Copy this shell verbatim, then fill `{{TITLE}}` and the `<main>` body. Severity classes: `.sev-p0`/`.sev-p1`/`.sev-p2`/`.sev-p3` for audit tiers; `.ok`/`.warn`/`.crit` badges for status.

```html
<!DOCTYPE html><html lang="uk"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{TITLE}}</title>
<style>
  :root{--bg:#0d1117;--panel:#161b22;--border:#30363d;--text:#e6edf3;
        --muted:#8b949e;--accent:#58a6ff;--mono:"SF Mono","JetBrains Mono",Menlo,monospace;
        --p0:#f85149;--p1:#d29922;--p2:#58a6ff;--p3:#8b949e;
        --ok:#3fb950;--warn:#d29922;--crit:#f85149;}
  *{box-sizing:border-box}
  body{margin:0;background:var(--bg);color:var(--text);line-height:1.6;
       font-family:var(--mono);-webkit-font-smoothing:antialiased}
  .wrap{max-width:920px;margin:0 auto;padding:2rem 1.5rem 4rem}
  header{border-bottom:1px solid var(--border);padding-bottom:1rem;margin-bottom:1.5rem}
  h1{font-size:1.5rem;margin:0 0 .4rem} h2{font-size:1.15rem;margin:2rem 0 .8rem}
  .meta{color:var(--muted);font-size:.85rem}
  .score{display:inline-block;font-size:2rem;font-weight:700}
  .card{background:var(--panel);border:1px solid var(--border);border-radius:8px;
        padding:1rem 1.2rem;margin:.8rem 0}
  .card.sev-p0{border-left:4px solid var(--p0)} .card.sev-p1{border-left:4px solid var(--p1)}
  .card.sev-p2{border-left:4px solid var(--p2)} .card.sev-p3{border-left:4px solid var(--p3)}
  table{width:100%;border-collapse:collapse;margin:1rem 0;font-size:.88rem}
  th,td{text-align:left;padding:.5rem .6rem;border-bottom:1px solid var(--border)}
  th{color:var(--muted);text-transform:uppercase;font-size:.72rem;letter-spacing:.05em}
  code{background:#1f2630;padding:.1em .4em;border-radius:4px;font-size:.9em}
  .badge{display:inline-block;padding:.1em .55em;border-radius:999px;font-size:.72rem;font-weight:600}
  .badge.ok{background:rgba(63,185,80,.15);color:var(--ok)}
  .badge.warn{background:rgba(210,153,34,.15);color:var(--warn)}
  .badge.crit{background:rgba(248,81,73,.15);color:var(--crit)}
  details{background:var(--panel);border:1px solid var(--border);border-radius:8px;padding:.6rem 1rem;margin:.6rem 0}
  summary{cursor:pointer;font-weight:600}
  a{color:var(--accent)}
</style></head>
<body><div class="wrap">
<header><h1>{{TITLE}}</h1><p class="meta">{{META}}</p></header>
<main>{{BODY}}</main>
</div></body></html>
```

# Procedure

<procedure>

### 1. Pick the skeleton
- `summary` → sections: Status header → Metrics table → per-role `<details>` "How to Use" → Next steps `<ul>` → Known issues `.card.crit`. Mirror the markdown produced by `summary-templates`.
- `audit` → sections: Executive summary (health `.score` /10) → Metrics table (current vs target) → Dimension Coverage table → P0–P3 backlog as `.card.sev-pN` blocks, each with What → Risk/Cost → Fix → How-to-verify → Engineering Standards.

Checkpoint: skeleton chosen for the report kind.

### 2. Fill the shell
Paste the shell, set `{{TITLE}}`/`{{META}}`, drop authored content into `<main>`. Use the severity classes; never invent inline colors.

Checkpoint: all placeholders replaced; no `{{…}}` left.

### 3. Map content to components
Status header → `<h1>` + `.badge`; metrics → `<table>`; per-role guidance → `<details>`; findings → `.card.sev-pN`; positives → `.badge.ok`.

Checkpoint: every section uses a shell component, not ad-hoc HTML.

### 4. Verify self-containment + budget
No external `src`/`href` to CDNs; no `<script>` unless explicitly needed. Count `<main>` lines against the budget (300 summary / 500 audit) and trim lowest-priority sections first.

Checkpoint: file opens offline; within budget.

### 5. Write and confirm
Write to the caller's output path. Confirm the file exists and is non-empty (`test -s`).

Checkpoint: artifact on disk.

</procedure>

# Self-check before returning

- [ ] Output is a single self-contained `.html` (no CDN/external assets)
- [ ] Correct skeleton used for the report kind (summary vs audit)
- [ ] All `{{…}}` placeholders replaced
- [ ] Severity uses `.sev-p0..p3` / `.badge ok|warn|crit` classes, not ad-hoc colors
- [ ] No metrics fabricated — every number came from the caller
- [ ] No secrets/PII in the output
- [ ] `<main>` within length budget (300 summary / 500 audit)
- [ ] File written and verified on disk (`test -s`)

# Common mistakes to avoid

- **Linking external CSS/JS** — reports must render offline; keep everything inline
- **Inventing metrics or health scores** — format only supplied numbers
- **Re-styling per report** — always use the one shell so reports stay consistent
- **Emitting HTML as the canonical artifact** — markdown stays canonical; HTML is the optional mirror

# Related skills

- **summary-templates** — authors the summary content this skill renders
- **mermaid-viewer** — for diagrams (do not hand-roll mermaid here)
- **code-standards** — review findings that feed an audit report

