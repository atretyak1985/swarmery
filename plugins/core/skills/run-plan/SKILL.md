---
name: run-plan
description: "EXECUTES an existing @planner plan -- parses the phase DAG, dispatches per-phase implement+review loops, preserving ASK gates. NOT for creating plans; NOT usable from inside a subagent."
version: "1.2.0"
owner: "swarmery-core"
docs:
  status: reviewed
  source_sha: 6ab5e18010a1
  updated: 2026-08-06
---

# Purpose

Turn a finished plan directory into shipped code with one command. The main
session acts as **controller**: it parses the phase DAG, picks a route, dispatches
executors with the plan's own prompts, reviews each result, and keeps durable
progress. The plan is the spec; this skill is the runner.

**Main session only.** Subagents cannot spawn subagents, so this playbook can
never be delegated.

# Rules (never violate)

1. **Read the ledger first** — phases marked reviewed-clean in
   `<task-dir>/logs/run-ledger.md` are DONE; never re-dispatch them.
2. **Tick as you verify** — flip each satisfied criterion `- [ ]` → `- [x]`
   immediately, never in a batch; unsatisfied criteria stay unticked.
3. **Write the `## Completion Report`** into the phase doc itself — the only
   per-phase summary the platform surfaces; a `reports/` file is no substitute.
   Its **Blocked calls** field is mandatory: which tool calls were refused or
   failed and how you got around them, one line each, `none` when there were
   none — omitting it is not the same as having nothing to report.
4. **Executors never commit** — commits, pushes, MRs, migrations, and deploys are
   ASK-gated controller business.
5. **Never skip the re-review** after a fix dispatch; review the reconciled tree,
   never the agent's own worktree.
6. One worktree per concurrent implementer on route P — parallel edits to one
   checkout corrupt each other.

# Resources

- Read `resources/execution-routes.md` at the start of a run: locating and
  parsing the plan, the pre-flight review, the route triage table and hand-off
  alternative, worktree isolation and reconciliation, and the S / P / W routes.
- Read `resources/progress-contracts.md` before accepting a phase: ledger format,
  the progress and summary hard gates, invariants, and failure modes.

# How to use

## What it does

Executes a plan directory, keeping a durable ledger so a compacted or interrupted
session resumes without redoing finished work.

## When to use it

- A plan directory with phase docs is ready to execute.
- The plan has parallel groups, several repositories, or a real dependency graph.
- A previous run stopped partway and should resume from the ledger.
- The plan needs worktree isolation so implementers cannot collide.

Not for: writing the plan (`@planner`); running inside a subagent; or a flat
sequential step list, which goes whole to `@implementation-agent` in its
plan-execution mode.

## How to invoke

```
Skill(skill: "core:run-plan") ~/workspace/working/2026/08/06/orders-line-items/plan/
```

The plan directory is optional — without one it finds the newest under the project
workspace, asking when several look plausible. Branch creation, pulls, commits,
pushes, migrations, and deploys come back to you as gated questions.

## Worked example

For the plan above it derives the phase graph and sees phases 1 and 2 are
independent: a worktree per phase, both implementers dispatched at once, each diff
reviewed against that phase's criteria, then merged in dependency order with the
plan's verification commands run after each merge. Phase 3 follows sequentially.
You end with a reviewed branch, ticked checkboxes, a Completion Report in every
phase doc, reports under `reports/`, a `SUMMARY.md`, and cleaned-up worktrees.

## Related

- `@planner` — produces the plan this skill consumes.
- `@implementation-agent` — for a simple sequential step list.
- `@verification-agent` — dispatched automatically for quality-gate phases.
