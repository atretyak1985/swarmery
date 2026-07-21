---
name: run-plan
description: "EXECUTES an existing plan produced by @implementation-planner or @task-planner -- parses the phase DAG, routes sequential phases through a per-phase implementer+review loop and parallel groups through concurrent isolated dispatches, preserving ASK gates and durable progress. NOT for creating plans (use @task-planner / @implementation-planner), NOT for ad-hoc single-file fixes (no plan needed), NOT usable from inside a subagent (subagents cannot spawn subagents -- this playbook runs in the main session only)."
version: "1.0.0"
owner: "swarmery-core"
---

# Purpose

Turn a finished plan directory into shipped code with one command. The main-session
agent acts as **controller**: it parses the plan's phase DAG, picks an execution route
per the triage table, dispatches executor subagents with the plan's own copy-paste
prompts, reviews each result, and keeps durable progress. The plan is the spec; this
skill is the runner.

**Hard constraint — main session only.** Subagents cannot spawn subagents, so this
playbook can never be delegated to an agent. It is invoked via `/run-plan` (or when
the user says "run the plan") and executed by the main loop.

# Step 0 — Locate and load the plan

1. Plan dir = `$ARGUMENTS` if given; otherwise the newest `working/**/{slug}/plan/`
   under the project workspace. If more than one plausible candidate exists, ask.
2. Parse the DAG: run `${CLAUDE_PLUGIN_ROOT}/bin/plan-manifest.sh <plan-dir>`.
   It prints `manifest.json` if the planner emitted one (authoritative), else a
   best-effort derivation (`"source": "derived"`) from the phase docs + README
   sequencing table. On a derived manifest, sanity-check `depends_on` against the
   README's table before trusting parallelism.
3. Read the plan README fully (risks, Definition of Done). Do NOT bulk-read all
   phase docs into context — each phase's content travels to its executor as a
   file reference, not as pasted text.
4. **Pre-flight review** (from superpowers:subagent-driven-development, which this
   skill follows where the two overlap): scan once for phases that contradict each
   other or the plan's constraints. Present all conflicts as ONE batched question
   before execution; if clean, proceed without comment.

# Step 1 — Triage: pick the route

| Plan shape (from manifest) | Route |
|---|---|
| All phases sequential (every `parallel_group` null) | **S — sequential loop** (default; most plans) |
| 2–4 phases share a `parallel_group` | **P — parallel group dispatch** |
| ≥5 independent same-shaped items (audit/migration/sweep) | **W — Workflow script** |

**Hand-off alternative:** a strictly-sequential `step-NN` plan (task-planner output)
can instead be given whole to `@implementation-agent` in its Plan-execution mode
(`task_dir` input, direct user invocation — see its Mode selection): it orchestrates
per-step leaf dispatch + verification loops itself. Prefer that when the user asked
for that agent or the plan is a simple step list; prefer /run-plan when the plan has
phases, parallel groups, multiple repos, or needs the manifest DAG / worktree
reconciliation machinery below. Never both at once for the same plan.

Route modifiers, any route:
- `kind: quality-gate` phases dispatch **@verification-agent** (or the plan's named
  gate owner), never an implementer.
- `manual_legs: true` steps (browser checks, live-env probes) run in the **main
  session** — subagents and workflow agents cannot drive interactive environments
  or answer permission prompts. Execute them yourself after the phase's automated
  part returns, or explicitly defer them with a `DEFERRED` ledger note.
- Mixed plans mix routes per phase group: a plan with phases 1‖2 parallel then 3
  sequential runs P for the group, then S.

# Step 2 — Isolation and branching (before first dispatch)

- Branch per the phase Header (the planner names branch + base). Create it with the
  project's branch flow (e.g. `/new-feature-branch`) — base-branch updates (`git
  pull`) and pushes are ASK-gated; surface them, don't bury them in a subagent.
- **Controller creates worktrees, not the Agent tool.** In multi-repo workspaces the
  workspace root is often not a git repo, so Agent-level `isolation: "worktree"`
  has nothing to act on. Instead: `git -C <repo> worktree add <workspace-scratch>/wt-<task>-p<N> <branch>`
  and pass the worktree path in the dispatch prompt. One worktree per concurrent
  implementer is MANDATORY on route P (parallel edits to one checkout corrupt each
  other); on route S a single worktree for the whole run is enough.
- Remove worktrees (`git worktree remove`) at run end; list them in the ledger so a
  resumed session can clean up.
- **The harness may force-isolate the executor anyway** (its own `agent-*` worktree,
  with writes to controller-prepared paths and the workspace dir blocked). Plan for
  it: (a) state the required base commit in the dispatch so both trees share it;
  (b) tell the executor that if its report path is write-blocked it writes to its
  scratchpad and returns the exact path; (c) on return, verify the agent worktree's
  base matches, then reconcile — copy the changed files onto the plan-branch
  worktree, confirm `git status` shows exactly the expected paths — and run review
  and verification against the RECONCILED tree, never the agent's.

# Step 3 — Execute

## Route S — sequential loop (per phase, in DAG order)

1. Record `BASE=$(git -C <worktree> rev-parse HEAD)`.
2. Dispatch **@implementation-agent** with:
   - the phase doc path, introduced as "read §4 Copy-paste agent prompt first —
     it is your task, verbatim; §3 Design is your reference"
   - the worktree path (work HERE, not the main checkout)
   - interfaces/decisions from earlier phases the doc cannot know (≤10 lines)
   - the report contract: write a full report to
     `<task-dir>/reports/phase-<N>-report.md`; final message ≤2k tokens (status:
     DONE / DONE_WITH_CONCERNS / NEEDS_CONTEXT / BLOCKED, files+LOC, test counts,
     deviations). **Do not commit** — implement, test, leave the tree dirty; the
     controller owns commits (they are ASK-gated).
3. Review: produce a diff file (`git -C <worktree> diff BASE` plus `--stat` to
   `<task-dir>/reports/phase-<N>-diff.txt`) and dispatch a fresh reviewer subagent
   (spec compliance against the phase's §5 Acceptance criteria + code quality; two
   verdicts required). Critical/Important findings → dispatch a fix subagent →
   re-review. Never skip the re-review.
4. Ledger line (see Durable progress), then next phase.

## Route P — parallel group

1. One worktree + branch per phase in the group (branch suffix `-p<N>`).
2. Dispatch all implementers of the group **in one message** (concurrent), each with
   its own phase doc + worktree, same contract as Route S. Blind independence: no
   agent sees another's draft.
3. As each returns, review it in its own worktree (same loop as S.3).
4. **Integrate in DAG order, not arrival order**: merge each reviewed branch into
   the integration branch sequentially; after each merge run the plan's
   verification commands; a conflict is resolved by a dedicated fix dispatch, never
   by hand-editing both branches.

## Route W — wide fan-out

Only when the fan-out is **closed-scope**: no `manual_legs`, no mid-run human input,
no ASK-gated operations inside the items (workflow subagents cannot prompt). Write a
Workflow script — `pipeline(items, implement, verify)` with `isolation: 'worktree'`
per item only if items mutate files. Anything open-scope stays outside the workflow
on route S/P.

# Durable progress

Append one line per event to `<task-dir>/logs/run-ledger.md`:

```
phase 1: dispatched wt=<path> base=<sha7>
phase 1: review clean (spec ✅ quality ✅) head=<sha7>
phase 2: [MANUAL] browser leg DEFERRED — env down
```

On invocation, **read the ledger first**: phases marked reviewed-clean are DONE —
never re-dispatch them (re-dispatching completed work is the most expensive known
failure after context compaction). Trust ledger + `git log` over recollection.

# Invariants (all routes)

- ASK gates are the controller's: commits, pushes, MRs, migrations, deploys are
  surfaced to the user with the plan's own rollback notes. Executors never commit.
- The plan is authority for WHAT; this skill is authority for HOW-to-run. An
  executor deviating from the plan's design must say so in its report; a plan step
  that turns out wrong goes back to the user (or the planner), not silently "fixed".
- Every dispatch carries the 4-field brief (objective, output format + length
  budget, tools/skills guidance, boundaries) per the delegation contract.
- After the last phase: final whole-branch review (fresh reviewer, full branch
  diff), then SUMMARY.md at the task root (via @summary-generator or inline),
  then worktree cleanup. Only then report done.

# Failure modes

| Failure | Detection | Recovery |
|---|---|---|
| Implementer BLOCKED / NEEDS_CONTEXT | status in report | Missing context → supply and re-dispatch; task too large → split; plan wrong → escalate to user |
| Review loop stuck (>2 fix rounds) | ledger shows 3rd re-review | Stop; present the disagreement to the user |
| Derived manifest mis-reads DAG | README table absent/odd | Treat plan as fully sequential (safe default); note in ledger |
| Merge conflict on route P integration | git merge fails | Dedicated fix dispatch in the integration worktree; re-run phase verification |
| Context compacted mid-run | ledger has entries you don't remember | Resume from ledger + `git log`; never restart phase 1 |
| Plan cites files that no longer exist | executor reports drift | Halt phase; send plan back for revision (planner or user) |
