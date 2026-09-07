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
## Copy-paste Agent Prompt
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

**TICK CONTRACT paragraph** (place right before the report-back
instructions): the platform renders this doc's status and checkboxes live. At
phase start flip the `Status:` header to `In progress`. The moment a task's
verification passes, edit THIS phase doc (spell out its absolute path) and
flip every Acceptance Criteria checkbox that task satisfies `- [ ]` → `- [x]`
— one edit per completed task, immediately, never batched. When the phase's
LAST checkbox is ticked, fill `## Completion Report` — what shipped, commits,
verification output, deviations, blocked calls (≤50 lines); the platform shows
exactly that section as the phase summary. When the plan's final phase lands,
also write `plan/SUMMARY.md` (objective, what shipped per phase, verification
results, follow-ups) — the plan-level summary.

Alongside what shipped, commits, verification output and deviations,
`## Completion Report` carries one more mandatory field:

> **Blocked calls** — which tool calls were refused or failed, and how you got
> around them (or that you did not). One line each. An empty list is written as
> "none" — omitting the field is not the same as having nothing to report.

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
