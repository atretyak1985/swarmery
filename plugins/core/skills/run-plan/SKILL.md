---
name: run-plan
description: "EXECUTES an existing @planner plan -- parses the phase DAG, dispatches per-phase implement+review loops, preserving ASK gates. NOT for creating plans; NOT usable from inside a subagent."
version: "1.6.0"
owner: "swarmery-core"
docs:
  status: reviewed
  source_sha: 6ab5e18010a1
  updated: 2026-10-23
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
   Three fields are mandatory and written out even when empty: **Blocked calls**,
   **Delegation cost**, and the one-sentence **What would have made this
   cheaper** — omitting one is not the same as having nothing to report. Exact
   wording: `workspace-plans/resources/plan-format.md`.
4. **Executors never commit** — commits, pushes, MRs, migrations, and deploys are
   ASK-gated controller business.
5. **Never skip the re-review** after a fix dispatch; review the reconciled tree,
   never the agent's own worktree.
6. One worktree per concurrent implementer on route P — parallel edits to one
   checkout corrupt each other.
7. **Don't check in between phases** — the ledger is the status, and a progress
   note belongs in the same message as the next dispatch. Stop and ask only when
   a phase cannot continue without the operator, or at one of rule 4's
   ASK-gated actions.
8. **Record every review round in the phase doc** — brief the reviewer with its
   round (`round: 1` for the first review of a phase, `2` for the re-review
   after a fix dispatch), then append the reviewer's `kind: review` yaml block
   verbatim under the doc's `## Review` section, immediately before
   `## Completion Report` (create the section on the first round; never edit
   an earlier round's block). When the reviewer subagent hit its turn limit
   and had to be resumed, set `complete: false` in that round's block and list
   what it did not cover in `scope_left`, whatever the resumed turn said. A
   reviewer reply without the block is recorded as a block you write from its
   findings, with `complete: false`. Shape:
   `workspace-plans/resources/plan-format.md` → "`## Review`".
9. **Brief the reviewer with where the executor changed course** — before each
   review, collect the executor's own workaround words: the guards list
   `workaround`, `instead`, `bypass`, `pre-existing`, `skip`, `skipped`,
   `flaky`, `for now`, `temporarily`, `обхід`, `обійти`, `замість`,
   `тимчасово`, `поки що`, `пропускаю`, `пропустив`, `нестабільний`,
   `вже був зламаний` (whole word, any case, prose only — not code).
   - With the daemon reachable, read them from it: in
     `curl -s http://localhost:7777/api/epics`, the epic whose `planDir` is this
     plan, then the phase by `seq`; its `workaroundHits` hold the word, the
     sentence and the sentences around it. The same list is
     `GET /api/epics/<taskId>/phases/<phaseId>/workaround-hits`. The daemon
     reads the executor's whole transcript, so dispatch every executor with a
     description that starts `Phase <N>` — that is how it attributes the prose
     to the phase.
   - Without a daemon, or while it lists none yet, find the same words in the
     executor's final message yourself.
   - Put them at the top of the code-reviewer brief, under
     `Look first — the agent changed course here:`, one line each (word and
     sentence). They are places to check first, not findings.
10. **Incomplete is never passed; two automatic rounds, then the operator.** A
    round is incomplete when the reviewer timed out or was resumed after a turn
    limit, gave no VERDICT line, gave no readable block (a PASS without a block
    included), said `complete: false`, or its block's `verdict` disagrees with its
    VERDICT line. An incomplete round starts no fix dispatch. Brief round 2 with
    ONLY round 1's open findings (ids, file:line, evidence), its `scope_left` and
    the diff since the tree round 1 reviewed — not the whole phase — and require
    `round: 2` with `resolves: [ids]`. A round-1 finding round 2 neither resolves
    nor re-reports goes into round 2's block as a `plan-conformance` finding
    `unaddressed <id>`, written by you, not the reviewer. There is no third
    automatic round: an incomplete round, or a FAIL at round 2, is handed to the
    operator with exactly two options — **repeat the review** or **accept it as
    is** (recorded as a `D-n` decision in the plan). When the plan is tracked by
    the swarmery daemon and it offers a review `phase_decision` for the phase,
    answer through that instead of asking in chat.
11. **Executors cite the ids they satisfy.** Every executor brief carries:
    "Cite the IDs you satisfy (D-n, SC-n) in the Completion Report; a decision
    you deliberately do not implement is written as
    `not implemented: D-n — <why>`." Check the citations before accepting the
    phase — an id the phase's `**Covers:**` or `**Depends on decisions:**` line
    claims but its report never cites is remaining, not done.
12. **Run the plan-end drift review.** When the last phase settles and the plan
    declares decisions (`## Decisions` in `plan/README.md`) or spec criteria
    (`plan/spec.md`), dispatch one code-reviewer over the whole plan branch.
    Hand it the decisions-and-criteria table — one row per `D-n` and `SC-n`:
    id, text, status (a decision's answer included), and the phases that claim
    it — and the diffs, and tell it to judge each id from the diffs, not from
    the reports. Require a `kind: review` block with `round: plan` and:
    ```yaml
    ids:                     # one entry per row of the table, in table order
      - {id: D-2, evidence: "phase 3 @ abc1234"}   # the phase (and commit) whose diff implements it
      - {id: SC-4, evidence: missing}              # nothing in the diffs implements it
    drift:                   # where the code departed from what a decision says
      - {id: D-4, where: "path/to/file.go:42", how: "adds a second table where D-4 chose a column"}
    ```
    A `not implemented: D-n — <why>` gap is not drift; each drift entry is a
    merge-blocking `plan-conformance` finding. Append the block verbatim under
    `## Review` of the plan's last phase doc, as rule 8 does for a phase round;
    an incomplete or INCONCLUSIVE plan round is recorded as such — the dashboard
    then shows the table as "not verified", never as passed.

# Resources

- Read `resources/execution-routes.md` at the start of a run: locating and
  parsing the plan, the pre-flight review, the route triage table and hand-off
  alternative, worktree isolation and reconciliation, and the S / P / W routes.
- Read `resources/progress-contracts.md` before accepting a phase: ledger format,
  the progress and summary hard gates, invariants, landing a stack of PRs
  (bottom-up, sequence printed by `${CLAUDE_PLUGIN_ROOT}/bin/land-stack.sh`), and
  failure modes.

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
