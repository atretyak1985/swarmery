---
description: Execute an existing plan (implementation-planner / task-planner output) — triage the phase DAG, dispatch isolated executor subagents, review each phase, keep durable progress
color: green
---

# Run Plan

Execute a finished plan directory end-to-end: parse its phase DAG, route sequential
phases through a per-phase implementer + review loop and parallelizable groups
through concurrent isolated dispatches, preserve ASK gates (commits/pushes/deploys
stay with the user), and track durable progress in the task ledger.

Follow the playbook in `skills/run-plan/SKILL.md` (auto-loaded skill `run-plan`).
Plan directory = $ARGUMENTS if provided, otherwise the newest
`working/**/{slug}/plan/` in the project workspace.

This command runs in the **main session** (the controller). It cannot be delegated
to an agent — subagents cannot spawn the executor subagents the playbook dispatches.

Related: `@implementation-planner` / `@task-planner` produce what this runs;
`@tech-lead` is the alternative when you want the full 9-phase workflow including
re-planning, pre-mortem, and the complete quality-gate panel rather than executing
an already-approved plan as written; `@implementation-agent` in Plan-execution mode
(`task_dir` input) is the single-agent alternative for a strictly-sequential
step-NN plan — the skill's triage section says when to hand off to it.
