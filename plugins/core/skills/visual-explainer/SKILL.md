---
name: visual-explainer
version: "1.0.0"
owner: "swarmery-core"
description: "Use when a markdown document, spec, plan, investigation or verbal explanation must become a page people read to understand how something works and explain it to others — an interactive self-contained HTML explainer with diagrams, charts, tables, glossary tooltips and a side panel for technical depth. NOT for agent summary/audit reports in the house style (html-reporting), viewing an existing .mmd (mermaid-viewer), or slide decks."
allowed-tools: Read, Write, Edit, Bash
color: teal
docs:
  status: draft
  updated: 2026-10-01
---

# Purpose

Turn a source (a markdown file, several files, or an explanation from the conversation) into one
self-contained HTML page that a newcomer can read top to bottom and a specialist can drill into.
Two layers, always: plain language and visuals on the page; protocols, file paths, numbers and
caveats behind a click (side drawer) or a hover (glossary tooltip).

The page is built on `templates/shell.html`, which already owns navigation, theming, the drawer,
tooltips, tabs, a step-through player, scenario switches and filters. You write content and
diagrams, not interaction code. `scripts/check.mjs` and `scripts/probe.js` then measure the result.

# Procedure

1. **Read the whole source.** Note the thesis, the mechanisms (what flows where), comparisons,
   numbers, sequences, decisions, risks and open questions. Every fact on the page must come from
   the source; keep its "unverified" markers.
2. **Map content to forms** before writing markup — read `resources/content-mapping.md`. Produce a
   short plan: one row per section with its plain-language claim, the visual that carries it, and
   what goes to the drawer. A section whose only visual is a table of prose is not done.
3. **Load the companion skills you have** (table below). They shape quality; the shell does not
   replace them.
4. **Build from the shell.** Copy `templates/shell.html` to the output path, rewrite the token
   block for this subject (palette, fonts that cover the page language), then add sections,
   glossary entries (`#glossary` JSON) and drawer content (`<template id="d-…">` or `#details` JSON).
   Markup for every component: `resources/components.md`. Write the page in parts of at most
   ~25 KB each (head + tokens, the shell's style block untouched, sections in two or three files,
   glossary/details islands, drawer templates, the shell script) and join them with `cat` — one
   huge Write stalls and loses everything when the stream breaks.
5. **Verify — measured, bounded.** Read `resources/verification.md`. Run `check.mjs`, then the
   browser matrix (desktop and 400 px, light and dark) with `probe.js`. Fix everything in one
   batch, re-run once. Do not report success on a pass you did not run after the last edit.
6. **Deliver.** The local file is the deliverable. If the page is meant for other people and an
   artifact/publishing tool is available, convert with `scripts/to-artifact.mjs` and publish.
   Tell the user what you checked and what you could not.

## Companion skills — load what is available

| Load | Available when | What it adds |
|---|---|---|
| The host's page/artifact design guidance (e.g. an `artifact-design` skill, or the publishing tool's quickstart) | the session offers it | page contract, theming, CDN allowlist, design effort |
| `frontend-design` | that plugin is enabled | aesthetic direction, token plan, list of generic looks to avoid |
| A diagramming skill (e.g. `artifact-diagramming`) | offered | inline-SVG discipline: draw the mechanism, label arrows |
| A data-viz skill with a palette validator (e.g. `dataviz`) | offered | chart form choice; run its validator on `--c1..--c3` in both themes |
| `core:browser-verification` | always (core) | the browser loop when no simpler browser tool is at hand |
| `core:mermaid-viewer` | always (core) | only when the user also wants a standalone viewer for a `.mmd` |

None is required; when one is missing, follow this skill's resources alone.

# Rules

1. Facts come from the source. Label models and examples you invent as illustrative; never add
   time-relative content (countdowns, "today" markers) — the page must stay true after today.
2. Every chapter has at least one visual that shows a mechanism, a comparison or a quantity.
   Draw the mechanism, not boxes with names; label every arrow.
3. Plain text first, depth on demand: technical detail goes to the drawer or a tooltip, never into
   the lede. Jargon on the page gets a `data-t` glossary entry.
4. Keep the shell's mechanics; change tokens, not component rules. Colors only via tokens, both
   themes designed, every font covering the page language, monospace ligatures off.
5. Ids unique across the page (a section and an SVG must not share one); `aria-label` on every
   diagram stating its claim; charts drawn to scale.
6. Success means: `check.mjs` exits 0 and `probe.js` reports `ok: true` in all four browser runs
   after the last edit. Anything not run is reported as unverified.

# Resources

- Read `resources/content-mapping.md` at step 2 — which visual fits which content, section recipe,
  writing the two layers, honesty rules.
- Read `resources/components.md` at step 4 — copy-paste markup for every shell component and the
  SVG recipes (flow, sequence with stepper, zones, lanes, DAG, mesh), with coordinate discipline.
- Read `resources/verification.md` at step 5 — static check, browser matrix, `probe.js`, tool
  pitfalls (headless CLI widths and color schemes), publishing.
- `templates/shell.html` — the page skeleton; `scripts/check.mjs` (static), `scripts/probe.js`
  (runs inside the page), `scripts/to-artifact.mjs` (strip the document wrapper for publishing),
  `scripts/test.sh` (self-test of the shell and scripts).

# How to use

## What it does

Builds one self-contained HTML page that explains a document or a topic: a thesis up front,
chapters each carried by a diagram, chart or table, glossary tooltips for jargon, and a side
drawer with the technical depth — then measures the page in two widths and two themes.

## When to use it

- "Make this markdown into a page I can show the team", "visualize this investigation/spec/plan".
- An explanation needs diagrams and a simple narrative, with details available on click.
- A long design doc must be skimmable by people who will not read all of it.

Not for: agent status reports and audits (`html-reporting`), rendering an existing `.mmd`
(`mermaid-viewer`), slide decks or exported documents (use a slides/docx skill).

## How to invoke

```
Skill(skill: "core:visual-explainer")

source: docs/design/investigation.md      # file(s), or "the explanation above"
out: docs/explainer.html                  # optional; default <source-stem>.explainer.html next to the source
language: uk                              # optional; default = the source's language
publish: ask                              # optional: ask | yes | no
```

Or the thin command: `/visualize docs/design/investigation.md`.

## Worked example

A 1,800-line investigation about running a daemon on many hosts became a 13-chapter page:
a one-host data-flow diagram, a stacked bar of table classes, a full-mesh architecture diagram
with clickable components, three sync lanes with tabs, a 9-step approval sequence player, a
failure-scenario switcher, a risk matrix and a phase DAG. 46 drawer templates and 47 glossary
terms carried the depth. Verification found a duplicate id (section and SVG), monospace
ligatures turning `-<` into arrows, an axis label overflowing at 100 %, and an arrowhead hidden
under a box — all fixed before delivery.

## Related

- `html-reporting` — agent reports in one canonical dark shell.
- `mermaid-viewer` — interactive viewer for an existing Mermaid file.
- `browser-verification` — the generic browser loop this skill's step 5 specialises.
