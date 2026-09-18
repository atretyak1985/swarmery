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

## Verification hooks for planners

- `test -s` on README.md, manifest.json, and every phase file.
- `python3 -m json.tool plan/manifest.json` exits 0.
- Final chat message: `Plan written: {path} | {N} phases, {L} total lines`.
