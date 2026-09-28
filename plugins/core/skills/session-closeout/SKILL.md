---
name: session-closeout
version: "1.1.0"
owner: "swarmery-core"
description: "Use this skill when closing a finished task — writing the SUMMARY.md final report, the phase-09 retrospective, and task documentation in the workspace task dir. Don't use it for mid-task status updates or commit messages."
color: yellow
docs:
  status: draft
  updated: 2026-09-01
---

# Purpose

Closing artifacts are how finished work becomes visible: the dashboard reads
`SUMMARY.md`, and the retro/advisor loop ingests `phases/09-retrospective.md`
and the delegation ledger. A task closed without them is invisible work.

# What to write

1. **`{task-dir}/SUMMARY.md`** (canonical final report, ≤300 lines) — exactly
   these seven sections: `## Результат`, `## Змінені файли`, `## Агенти`,
   `## Сесії`, `## Відхилення від плану`, `## Скріншоти`, `## Follow-ups`.
   Deviations compare actual agents/loops against ORCHESTRATION.md, not only
   the plan; the screenshots section lists `{task-dir}/screenshots/` files
   with captions or states "None captured". Format details:
   `resources/summary-format.md`.
2. **`{task-dir}/phases/09-retrospective.md`** (≤150 lines) — for non-trivial
   tasks. MUST use the machine-parsed headings: `## Lessons Learned` with
   `### Lesson N: <title>` subsections each carrying a `**Action**: …` line,
   and `## Process Improvements` (that exact heading — the ingester regexes
   match it, not synonyms). Read `{task-dir}/logs/agents.md` (7-cell ledger)
   first — its quality/mistakes cells are the evidence base. Format:
   `resources/retrospective-format.md`.
3. **`{task-dir}/plan/SUMMARY.md`** (≤120 lines) — for plan-driven tasks: the
   plain-language overview the dashboard shows at the top of the plan's
   Summary tab, above the per-phase reports. Written for the operator, not
   for an engineer: what the task was in plain words, what was built and how
   it works, where to see it, the headline numbers, what is left and what the
   operator has to do. No commit hashes or file paths in the body. Format:
   `resources/plan-summary-format.md`.
4. **Archive** when every criterion is met:
   `bash "${CLAUDE_PLUGIN_ROOT}/bin/agent-work.sh" complete {task-id}`. Never
   archive with unmet criteria or a missing SUMMARY.md — escalate instead.

Honesty rule: outcome is `Success | Partial | Failed` as it actually was;
lessons cite specific phases/files; a flattering retro poisons the
self-improvement loop.

# How to use

## What it does

Carries the closing-artifact contract for a finished task: the seven-section `SUMMARY.md`, the plain-language `plan/SUMMARY.md` overview, the machine-parseable retrospective, and the archive step — the formats the dashboard and the retro/advisor loop actually ingest.

## When to use it

At the end of any orchestrated or plan-driven task: the last checkbox is ticked, the work is verified, and the task dir needs its final report and retrospective before archiving.

## How to invoke

`@core:tech-lead` and `@core:implementation-agent` load it via `skills:` at close-out; load it manually when finishing a task by hand and follow `resources/summary-format.md`, `resources/plan-summary-format.md` and `resources/retrospective-format.md`.

## Worked example

After a 3-phase plan finishes, the orchestrator writes `SUMMARY.md` (result, changed files, agents used with loop counts, deviations: "phase 2 needed one correction loop — planned 0"), then `phases/09-retrospective.md` with `### Lesson 1: verify fixture DBs after migrations` and its `**Action**:` line under `## Lessons Learned`, one entry under `## Process Improvements`, and runs `agent-work.sh complete 2026-09-01-rate-limiting`.
