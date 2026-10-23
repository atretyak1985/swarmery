# Plan revisions

A saved plan's markdown is the source of truth; a **revision** is a staged,
reviewable proposal to change it. Nothing under the plan dir moves until the
operator applies the revision — the `plan_revisions` / `plan_revision_files`
rows (migration 0052) ARE the proposal.

## Lifecycle

```
staged ──► applied      operator approved; files landed atomically on disk
       ──► rejected     operator declined (optional note appended to reason)
       ──► superseded   system: staged >14 days with no decision (daily prune)
       ──► failed       apply hit an I/O error mid-write; every completed step
                        was rolled back from its pre_image
```

One open (`staged`) revision per plan. Rows are never deleted — they are the
audit trail (`origin` records operator_revise vs phase_diagnosis, `decided_by`
records operator vs system). Document bodies (`proposed`, `pre_image`) are
nulled 90 days after the decision; the metadata and hashes stay.

## Scratch handoff contract

A revise wizard (`planning_sessions.mode='revise'`) runs a headless claude
session seeded with the full current plan. The agent writes its proposal into
`<db dir>/revisions/<session uuid>/`:

- `revision.json` — `{reason, summary, files:[{path, action, renameFrom?}]}`
- one full proposed file per non-delete entry, under the TARGET name

and ends its reply with `REVISION STAGED: <scratch dir>`. The daemon
(`planning.Stage`) validates the proposal against the LIVE plan and inserts the
rows. Validation failure is an interview turn, not a terminal state: the
session falls back to `awaiting_answer` with `REVISION REJECTED: <why>` and the
scratch dir is kept for amendment. Success removes the scratch dir. Orphaned
scratch dirs (session terminal or gone) are swept on daemon start.

## Validation rules (staging)

- Only `README.md` and `phase-*.md` at the plan root; `step-*.md` is refused.
- Actions: create / update / delete / rename; README may only be created/updated.
- DONE phase docs (all criteria ticked) are immutable; a RUNNING phase's doc
  (target or rename source) is refused — revise after the run ends.
- create needs a new name; update/delete need their target; rename needs its source.
- Every proposed phase doc must keep a `## Completion Report` section.
- The post-revision README phase table must resolve: every Doc cell names a
  file that will exist, every dependency names a table row (THE scanner's parser).
- Each file records `base_hash` — the sha256 of the live bytes it was staged
  against (rename: the source; create: none).

## Conflict semantics

The review diff (`GET /api/revisions/{id}`, `planrev.LiveDiffs`) is computed
against the live bytes at request time; `stale: true` flags drift from
`base_hash`. Apply re-checks every hash first and aborts with **409 + the full
conflict list** before writing anything; a running target phase is also a 409.
The write phase is atomic: deletes → renames → creates/updates, each step
recording an undo, any failure rolling all of them back and stamping `failed`.
Renames move `epic_phases.doc_path` explicitly so daemon-owned run state
survives; one rescan runs after all writes.

After a successful apply the daemon **re-lends** every doc the revision
created, updated or renamed (`phaserun.Service.RelendDocs` →
`worktree.RelendPlanDoc`): a phase worktree that still holds a lent copy at
`.swarmery/plan/<doc>.md` gets the revised text, so nothing graded or guarded
later reads the pre-revision doc. A worktree without a copy is left alone —
nothing is planted. Apply already refuses a running phase, so the re-lend never
races an executor; the next run lends the doc afresh anyway.

## Endpoints

| Method/path | Purpose |
|---|---|
| `POST /api/epics/{taskId}/revisions` | start a revise wizard (`{reason, phaseId?}`) |
| `GET /api/epics/{taskId}/revisions` | history, newest first (actions, no content) |
| `GET /api/revisions/{revisionId}` | detail: live per-file diffs + stale flags |
| `POST /api/revisions/{revisionId}/apply` | conflict-guarded atomic apply |
| `POST /api/revisions/{revisionId}/reject` | reject (`{note?}`) |
