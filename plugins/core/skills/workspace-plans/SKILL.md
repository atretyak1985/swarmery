---
name: workspace-plans
version: "1.0.0"
owner: "swarmery-core"
description: "Use this skill when writing, revising, or executing a workspace plan — the plan/README.md + phase-N-<slug>.md format the dashboard ingests. Don't use it for ad-hoc todo lists or in-repo design docs."
color: cyan
docs:
  status: draft
  updated: 2026-09-01
---

# Purpose

The private workspace plan format is a contract: the control plane parses it
to render plan progress, phase summaries, and spec coverage. A plan that
deviates from the format ships work the operator cannot see.

# The contract (never violate)

- Plans live at `<workspace>/<project>/workspace/working/{YYYY}/{MM}/{DD}/{slug}/plan/`;
  task-id `yyyy-mm-dd-slug` is derived from the path — never encode the date
  in the folder name.
- `plan/README.md` — objective, architecture decisions with real file paths,
  the sequencing table with exactly these header cells:
  `| # | Phase | Doc | Depends on |`, risks, Definition of Done.
- `plan/phase-N-<slug>.md` per phase (flat; `step-NN-*.md` is legacy
  read-compat only) — each with a self-contained copy-paste executor prompt,
  `- [ ]` acceptance criteria checkable by command or boolean inspection, and
  an empty `## Completion Report` section as the LAST section.
- `plan/manifest.json` — machine-readable phase DAG (must pass
  `python3 -m json.tool`).
- Optional `plan/spec.md` with `- [ ] **SC-n** — <criterion>` lines; then
  every phase doc carries a `**Covers:** SC-…` header line (coverage is
  linted).

# Executor duties

Tick each satisfied criterion `- [ ]` → `- [x]` immediately after verifying
it — progress is derived only from these checkboxes. When a phase's last
criterion is ticked, fill that doc's `## Completion Report` (what shipped,
files/commits, verification output, deviations, and a mandatory
**Blocked calls** list — which tool calls were refused or failed and how you
got around them, written as "none" when there were none; ≤50 lines) — the dashboard
renders exactly that heading. Never tick unsatisfied criteria; never archive
with unmet criteria or a missing SUMMARY.md.

Full field-by-field format, examples, and the manifest schema:
`resources/plan-format.md`.

# How to use

## What it does

Carries the exact workspace-plan format the dashboard ingests: README sequencing table, phase docs with executor prompts and checkbox criteria, manifest DAG, optional spec with SC coverage, and the tick/Completion Report duties for executors.

## When to use it

Load it whenever you write a new plan, revise one, or execute one — any time plan files under a task dir's `plan/` are created or updated.

## How to invoke

The `@core:planner` and `@core:implementation-agent` agents list it in their `skills:`; load it manually with `/workspace-plans` context or by reading `resources/plan-format.md` when editing plan files by hand.

## Worked example

A planner writing `plan/phase-2-api.md` includes: `**Covers:** SC-3`, a prompt an executor can run cold, three `- [ ]` criteria each with a check command, and ends the doc with an empty `## Completion Report` section. The executor later ticks the boxes one by one and fills the report — the dashboard's phase Summary tab shows exactly that section.
