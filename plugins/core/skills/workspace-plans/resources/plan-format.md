# Workspace plan format — full contract

Everything the control plane parses, field by field. When this file and a
planner's habit disagree, this file wins.

## Location and identity

- Plan root: `${AGENT_WORKSPACE_ROOT}/${AGENT_PROJECT}/workspace/working/{YYYY}/{MM}/{DD}/{slug}/plan/`
- Task-id `yyyy-mm-dd-slug` is DERIVED from the path: date = the `YYYY/MM/DD`
  prefix (task start date), slug = the leaf folder name. Never date-prefix the
  leaf folder.
- Flat layout — no phase subdirectories. `step-NN-*.md` files are legacy
  read-compat only; new plans write `phase-N-<slug>.md`.

## plan/README.md (≤ ~150 lines)

Required content: objective; architecture decisions with real file paths;
risks; Definition of Done; and the phase sequencing table with EXACTLY these
header cells (the platform parses this shape; the Doc cell wraps the filename
in backticks):

```markdown
| # | Phase | Doc | Depends on |
|---|-------|-----|------------|
| 1 | Types & schema | `phase-1-types-schema.md` | — |
| 2 | Backend logic | `phase-2-backend-logic.md` | 1 |
```

## plan/phase-N-<slug>.md (≤ ~150 lines each)

Section order:

```markdown
# Phase N — {Title}
Status: Pending
**Covers:** SC-…            ← only when plan/spec.md exists
## Goal
## Files to Create / Files to Modify
## Implementation Details
## Copy-paste Agent Prompt   ← Verify block: file-backed commands only
## Dependencies
## Acceptance Criteria
- [ ] {measurable criterion with its verification command}
## Notes
## Forecast                   ← optional; see "## Forecast" below
## Completion Report          ← empty stub, ALWAYS the last section
```

The copy-paste agent prompt is one fenced block, self-contained: repo path +
branch, a "read first for conventions" file list, numbered tasks,
verification commands, a TICK CONTRACT paragraph, and report-back
instructions — executable without opening anything else.

**Verification commands are file-backed.** Every command in a Verify block is
either a project script/test invoked by **absolute path**, or a single tool
invocation with its arguments. No `<<EOF` / `<<'PY'` heredoc, no `python3 -`,
no `node -e`. If a check needs more than one line of logic, it is a file in
the repo — `scripts/<name>.sh` or a test — committed with the phase, and the
phase doc names that path.

This removes three failure classes at once: `<stdin>` tracebacks that name no
file, `eval: line N: unexpected EOF` from a delimiter broken by quoting, and
`ERR_MODULE_NOT_FOUND` from a script that resolved its imports against the
wrong root. A file has a path, a stack trace that points at it, and a second
run that behaves the same way.

**TICK CONTRACT paragraph** (place right before the report-back
instructions): the platform renders this doc's status and checkboxes live. At
phase start flip the `Status:` header to `In progress`. The moment a task's
verification passes, edit THIS phase doc — name it by the path the execution
contract gave you (a lent copy inside your root when you are isolated, the
workspace path otherwise); never hard-code an absolute path that points outside
your root. Flip every Acceptance Criteria checkbox that task satisfies
`- [ ]` → `- [x]` — one edit per completed task, immediately, never batched.
When the phase's LAST checkbox is ticked, fill `## Completion Report` — what
shipped, commits, verification output, deviations, and the three mandatory
fields below — blocked calls, delegation cost, and the one-sentence
"what would have made this cheaper" (≤50 lines); the platform
shows exactly that section as the phase summary. When the plan's final phase
lands, also write `plan/SUMMARY.md` (objective, what shipped per phase,
verification results, follow-ups) — the plan-level summary.

Alongside what shipped, commits, verification output and deviations,
`## Completion Report` carries three mandatory fields, in this order:

> **Blocked calls** — which tool calls were refused or failed, and how you got
> around them (or that you did not). One line each. An empty list is written as
> "none" — omitting the field is not the same as having nothing to report.

> **Delegation cost** — every subagent this phase dispatched, with its
> wall-clock and (where the platform reports it) its dollar cost. One line each.
> A phase that dispatched nothing writes "none" — omitting the field is not the
> same as having nothing to report.

> **What would have made this cheaper** — exactly one sentence. Not a retro, not
> a list: the single change that would most have reduced the above. "Nothing, it
> was already minimal" is a legitimate answer and must be written out rather than
> left blank.

The last two exist because of a measured window in which one task produced 7
successful delegations and another 5 recorded **zero** lessons — because nothing
was reworked, and expense on its own was not treated as a finding. They are what
makes a phase that went well but cost too much still leave a trace. Keep both
after `Blocked calls`, inside the same ≤50-line budget; the one-sentence limit on
the second is load-bearing — an open-ended "lessons learned" section is what
agents skip.

The final phase of a multi-phase plan is always a quality gate
(`kind: quality-gate`).

## plan/manifest.json

Machine-readable DAG the run-plan skill executes without parsing markdown.
Mirrors the sequencing table exactly — if they disagree, the manifest is
wrong. Must pass `python3 -m json.tool`.

```json
{
  "task": "<slug>",
  "source": "planner",
  "planner": "planner",
  "phases": [
    { "id": 1, "file": "phase-1-<slug>.md", "title": "…",
      "repos": ["<repo>"], "depends_on": [], "parallel_group": null,
      "kind": "implementation", "manual_legs": false }
  ]
}
```

`manual_legs: true` wherever a phase doc contains `[MANUAL]` markers.

## plan/spec.md (optional, recommended for larger work)

The WHAT/WHY: short problem statement, user stories, and an
`## Acceptance criteria` section whose items are shaped exactly
`- [ ] **SC-1** — <criterion>` (stable SC-n ids, one behavior each). When
spec.md exists, every phase doc MUST carry a `**Covers:** SC-…` line; every
SC id must be covered by ≥1 phase and no phase may cover an undeclared id —
the platform lints coverage.

## `## Forecast` (optional, in a phase doc, before `## Completion Report`)

A small prediction the platform stores and later scores. It is DATA, never a
gate: no run is refused and no phase is incomplete for diverging from one, or
for lacking one. A block the platform cannot read is a lint on a plan that
still ingests.

````markdown
## Forecast

```yaml
kind: prior            # prior (planner) | posterior (executor)
written_at: 2026-09-23T10:12:00Z
areas: [internal/ingest, internal/cost]                    # REQUIRED
files: [internal/ingest/record.go, config/pricing.json]    # optional, globs ok
size_band: M           # XS <20 lines | S <100 | M <400 | L <1500 | XL
duration_band: 30-90m  # <30m | 30-90m | 90m-4h | >4h
outcome: done          # done | partial | blocked
risks: ["migration touches turns table", "recost path diverges"]
confidence: 0.7        # 0..1, how sure the whole forecast holds
```
````

A doc may carry both a prior and a posterior — two fenced blocks in one
section, told apart by `kind`. The planner writes the prior. The executor
writes the posterior **after it has read the code the phase touches and before
its first edit** — that is the last moment at which it is still a prediction.
It is a prediction, not a limit: do whatever the phase actually needs. When the
work turns out different, the Completion Report carries a short "Where reality
diverged" paragraph saying how and why.

Linted (shown on the phase, never enforced): an unrecognized `kind`,
`size_band`, `duration_band` or `outcome`; a `confidence` that is not a number
in 0..1; a forecast with no `areas`; a posterior with no prior to score
against.

**Post hoc.** A forecast is flagged post hoc when it cannot have been a
prediction, and post-hoc forecasts are excluded from calibration. Two
independent observations set the flag, and the phase shows which one did:

| Reason | Applies to | Evidence |
|---|---|---|
| `report-filled` | prior | The doc's `## Completion Report` was already filled when the prior appeared. |
| `after-first-edit` | posterior | The run's transcript shows the posterior was written to the doc after the run's first change to some other file. |

Neither is a gate. A post-hoc forecast still ingests, still renders, and still
lets its phase run; the only consequence is that the learning loop does not
score it.

## Verification hooks for planners

- `test -s` on README.md, manifest.json, and every phase file.
- `python3 -m json.tool plan/manifest.json` exits 0.
- Final chat message: `Plan written: {path} | {N} phases, {L} total lines`.
