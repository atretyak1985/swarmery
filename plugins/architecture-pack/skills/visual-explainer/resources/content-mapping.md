# Content mapping — from a source to a page plan

Do this before any markup. The output is a short plan you can check against the source.

## 1. Extract

Read the whole source and list, with the section it came from:

- **Thesis** — the one sentence a reader must leave with.
- **Mechanisms** — what flows where, who calls whom, what state a thing moves through.
- **Comparisons** — options with criteria, before/after, A vs B.
- **Quantities** — counts, shares, scores, thresholds, durations.
- **Sequences** — ordered steps, message exchanges, phases with dependencies.
- **Rules and lists** — invariants, "never do", checklists, requirements.
- **Risks, decisions, open questions** — usually with an owner, a stage or a recommendation.
- **Vocabulary** — every term a newcomer would stop at.

## 2. Pick the form by the job

| Content | Form (shell component) | Never |
|---|---|---|
| A mechanism: components and what passes between them | inline-SVG flow diagram (`.d` vocabulary), clickable boxes (`data-d`) | a bullet list of component names |
| A message exchange in order (protocol, approval, request path) | sequence diagram + `[data-stepper]` player; or `.hops` for a linear path | prose paragraphs per step |
| The same system under different conditions (failures, modes) | `[data-scenarios]`: one diagram, buttons switch `down`/`off` states | one diagram per case |
| Parallel tracks with the same anatomy (layers, channels) | lanes in one SVG + `[data-tabs]` panels | separate disconnected figures |
| Options scored on one scale | `.bars` (score rows), best option in `--accent`, rows open details | a dense table only |
| Options compared on several criteria | table with `.yes`/`.no` cells and verdict chips | colored text without labels |
| Parts of a whole | `.sbar` 100 % stacked bar, labels inside only when they fit | pie charts |
| Thresholds, time windows | `.scale-bar` / `.marks`, drawn to scale (flex = real units) | a decorative timeline |
| Two dimensions (probability × impact, effort × value) | `.matrix` with items as buttons | a ranked list that hides the second axis |
| Phases with dependencies | DAG in SVG + `.cards` with a gate line each | a numbered list when order is not linear |
| A directory or config tree | `.tree` with a comment column, colored by role | a code block with comments |
| Hard rules, forbidden things | `.xlist` (✕ items) or `.alerts` | bold paragraphs |
| A procedure | `.steps` (numbered) — only when order is real | numbered markers on unordered content |
| Acceptance criteria / goals | `.checks` | — |
| Stability or maturity levels | `.meters` (3 levels, with a word label) | color alone |
| The few numbers the page is about | `.facts` row or one `.stat` | big-number tiles for numbers nobody acts on |
| Q&A from the owner, decisions to make | `.cards` (+ `[data-filter]` when > 6) | — |

Budgets: split a diagram above ~12 nodes; at most one `--accent` focus per diagram; three
categorical series (`--c1..--c3`) — fold the rest or use small multiples.

## 3. Write the section plan

One row per section, in reading order:

| # | Nav name | Claim (plain, one sentence) | Visual | Drawer / tooltips |
|---|---|---|---|---|

Rules for the plan:
- The first section states the thesis and how to read the page (terms, drawer, ← → keys).
- Each claim is a sentence a newcomer understands without the drawer.
- The visual column is never empty. "Table" alone is allowed only for genuinely tabular data.
- Order follows the reader's questions: what is it → how it works today → what changes →
  how → what can go wrong → what happens next → what needs deciding.

## 4. Write the two layers

**On the page** — short, active sentences; name things the way the reader knows them; one idea
per paragraph; numbers with units; the lede of each section answers "why should I care".

**In the drawer** (`<template id="d-key">` or `#details`) — protocols, payloads, ports, file:line
references, configs, exact thresholds, edge cases, source quotes. End with `<div class="src">`
naming where the facts come from. Use `<dl>` for property lists, `<pre>` for config, small tables.

**In tooltips** (`#glossary`) — one plain sentence per term, no jargon inside the definition.

## 5. Honesty rules

- Every number, name and claim traces to the source. Do not fill gaps with plausible detail.
- Keep the source's uncertainty markers (e.g. "unverified", "[from memory]") next to the claim.
- Models you invent to explain (a simplified simulator, an example payload) are labelled
  "illustrative" on the page.
- No time-relative content: no "today" markers, countdowns or "N days left". Dates stay dates.
- When the source is a conversation rather than a file, list in the footer what it was based on.
