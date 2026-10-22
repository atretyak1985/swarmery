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

Required content: an `## In plain words` section directly under the H1 (see
"`## In plain words`" below); objective; architecture decisions with real file
paths; risks; Definition of Done; and the phase sequencing table with EXACTLY these
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
**Repo:** `<repo>` · **Branch:** `<branch>` · **Depends on:** … · **Estimate:** …   ← Branch is informational; the daemon lands swarm/phase-<id>
**Verify:** off             ← off | normal | strict (see "Header rows" below)
**Review:** off             ← on | off
**Effort:** high            ← low | medium | high | xhigh — required (see "Header rows")
## In plain words           ← 3–6 jargon-free sentences; see below
## Goal
## Files to Create / Files to Modify
## Implementation Details
## Copy-paste Agent Prompt   ← Verify block: file-backed commands only
## Dependencies
## Acceptance Criteria
- [ ] {measurable criterion with its verification command}
## Notes
## Guard                      ← the planner's `kind: guard` block; see "## Guard" below
## Forecast                   ← optional; see "## Forecast" below
## Review                     ← appended per review round; see "## Review" below
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
Criteria prefixed `[LAND]` or `[MANUAL]` (see "Criterion classes") are not the
executor's: it neither ticks them nor tries to perform them.
When the phase's last EXECUTABLE checkbox is ticked — every criterion that is
not `[LAND]`/`[MANUAL]`; those two classes close later, without an executor —
fill `## Completion Report` — what
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

### Header rows: `**Verify:**`, `**Review:**` and `**Effort:**`

All three are read from the phase doc's header only — the lines before the first
`## ` section, within the first 15 lines — so a `**Verify:**` line quoted in an
agent prompt further down never counts. Put them on their own lines directly
after the `**Repo:** …` line.

| Row | Values | Meaning | Default |
|---|---|---|---|
| `**Verify:**` | `off` \| `normal` \| `strict` | After a run ends `done` or `partial`, a read-only verifier grades the ticked criteria at the given strictness. Any value but `off` also makes the phase gate require a passing verdict before the phase counts as complete. A missing or unrecognized value means `off`. | L/XL: `normal`. S/M: `off`, or `normal` when the phase touches data, schemas or migrations. |
| `**Review:**` | `on` \| `off` | The phase asks for a read-only code-review pass over its diff before the verifier runs; a failed review returns the phase once with the findings appended to the doc. A missing value means `off`, and a daemon without the review stage ignores the row. | L/XL: `on`. S/M: `off`. |
| `**Effort:**` | `low` \| `medium` \| `high` \| `xhigh` | The reasoning depth the phase's run gets (`--effort`), chosen together with the model the phase needs. A value the CLI does not accept fails the run before it starts, naming the doc. Required: a doc without it runs at the daemon's default effort and is linted `phase_effort_missing`. | `low`/`medium` for mechanical edits and config, `high` for ordinary implementation, `xhigh` for design-heavy or cross-cutting work. |

### The header's `**Branch:**` is informational

`**Branch:**` in the phase header is a note for people — the branch a person
would use running the phase by hand. Land never pushes the header branch: a
daemon run commits on its run branch `swarm/phase-<id>`, and landing pushes
and opens the pull request for only that branch; the merge happens on the host. Three reasons:

- The header branch may not exist, or may belong to other work — pushing to it
  would write into someone else's branch.
- The run's commits live on `swarm/phase-<id>`; landing them under another name
  would mean a rename or a force-push, and landing never forces or renames.
- The landing poll, pull-request discovery and the dashboard's branch-to-phase
  mapping are all keyed on the run branch.

When the two differ, the Review tab shows a warning; nothing else changes. So
no criterion names a branch — a `[LAND]` criterion says "the phase's PR is
merged into main", never which branch the PR comes from.

### Criterion classes

An acceptance criterion may start with a class marker that says who closes it.
The marker is upper-case, sits right after `- [ ] ` and is followed by a space;
anywhere else in the label — or inside a fenced block — it is ordinary text.
Unmarked criteria belong to the executor.

| Marker | Who closes it | Example |
|---|---|---|
| `[LAND]` | Landing the work: a push, a pull request, a merge. Names no branch (see "The header's `**Branch:**` is informational"). | `- [ ] [LAND] phase PR merged into main` (Ukrainian plan: `- [ ] [LAND] PR фази злито в main`) |
| `[MANUAL]` | A human only: a production check, a console action, a manual look. | `- [ ] [MANUAL] line-item totals checked by hand in the production console` |

How the daemon treats them:

- **Effective total at settle.** A phase run is measured against every
  criterion except the open `[LAND]` and `[MANUAL]` ones. The continuation
  message lists only the executable criteria, and the phase prompt tells the
  executor that classed criteria are not its to tick or perform.
- **Done + landing ready.** A run whose only open criteria are `[LAND]` ends
  `done`, with the detail "N [LAND] criteria left for landing", and the phase
  shows `landing: ready`.
- **409 `manual-only`.** Starting a run on a phase whose only open criteria are
  `[MANUAL]` is refused with HTTP 409 code `manual-only`; there is nothing for a
  run to do.
- **Needs you `manual_phase`.** The same phase is listed in Needs you as a
  "Manual check" item that links to its Criteria tab.
- **Needs you `awaiting_merge`.** A phase whose only open criteria are `[LAND]`
  and whose landing is ready, pushed or has a pull request open is listed in
  Needs you as an "awaiting merge" item that links to its Review tab (and to the
  pull request once one is open).
- **Who ticks `[LAND]`.** When a phase is landed through the Review tab, the
  daemon ticks its `[LAND]` criteria the moment the pull request turns merged. A
  pull request for the phase's run branch `swarm/phase-<id>` opened outside the
  platform is found too — by the background landing poll (at most 5 phases per
  10-minute tick, each asked about at most once an hour) or at once by the
  Review tab's "Find PR" / "Find MR" button — and from then on is tracked and
  ticked the same way. A pull request from any other branch is not tracked:
  its `[LAND]` criteria stay for the operator to tick by hand. `[MANUAL]`
  criteria are always ticked by hand.
- **The phase gate is unchanged.** Classed criteria stay in the phase's total,
  so the phase becomes complete only after the merge and the manual check are
  ticked.

The Plans Criteria tab flags unmarked, unticked criteria whose wording looks
like landing or manual work — any whole word of
`push|pull request|pr|merge|gh pr|прод|production|console|вручну|manually|по руках`,
case-insensitive — and offers a one-click "Mark [LAND]" /
"Mark [MANUAL]" that writes the marker into the doc. The flag is a hint and
blocks nothing.

The manifest sets `manual_legs: true` for any phase whose doc carries a
`[MANUAL]` marker.

### Engine guarantees: spike every mode

A spike criterion that promises a person sees something — a permission dialog,
a prompt, a notice — is measured on a matrix, not in one run. Its axes are the
permission mode (default / acceptEdits / plan / auto / dontAsk /
bypassPermissions) × the surface (`-p` / interactive). A guarantee observed in
default mode says nothing about auto mode, where the engine can settle an `ask`
without showing anything. Copy `templates/spike-matrix.md` (project
`.claude/templates/` first, then the plugin's), fill one row per cell with an
observed verdict and an evidence file, and let the plan promise only what the
matrix shows. A cell left `not measured` is named in the plan as a gap.

### Engine-boundary phases: live check before [LAND]

A phase that changes a path crossing a process boundary the engine owns — hooks,
mods, IPC between sessions — carries an **executable** live-check criterion,
ordered before its `[LAND]` criterion. The check runs on an isolated dev
environment (its own port, scratch database and workspace root, plugins loaded
with `--plugin-dir`, options passed with `--settings`), never on the live control
plane or the operator's settings. It is not a post-merge `[MANUAL]` item: unit
tests and review do not see what the engine does to a message between two
sessions, and a bug found after the merge costs a second PR.

```markdown
- [ ] live check on the dev environment: session A sends, session B receives, the pair correlates (evidence under `### Live check <date>`)
- [ ] [LAND] phase PR merged into main
```

### Live-check evidence: tick + append immediately

After each live check, tick its criterion and append the evidence — commands,
observed output, verdict — under `### Live check <date>` in the phase doc,
before starting the next check. Scratch files are a backup, not the record: an
executor that dies between checks leaves evidence only where it already wrote
it. A live-check prompt carries this rule next to its TICK CONTRACT.

### Settings-file criteria cite the read channel

Any criterion that names a settings file — `settings.json`,
`settings.local.json`, `.claude.json`, managed settings, a `--settings` file —
cites `resources/config-channels.md` and the fact it relies on (G1 for
`pluginConfigs`, G2 for `${VAR}` substitution, G3 for an `env` block). The engine
reads plugin options only from user settings and `--settings`; a criterion that
puts them in a project's `settings.json` or `settings.local.json` cannot pass.

### Migrations: number at landing

A plan names a migration `NNNN_<name>.sql` but does not fix `NNNN`. The number is
allocated when the branch is rebased for landing: the next number above the
highest on the default branch at that moment. A number picked at write time
collides with whatever lands first while the branch is open, and every collision
costs a renumber commit.

## `## In plain words`

Every plan README (directly under the H1) and every phase doc (directly after
the header rows, before `## Goal`) opens with this section. The rest of the doc
is written for the executor; this one is written for a person who did not
write the plan and does not know the codebase — the operator skimming the
Plans page. The dashboard shows it verbatim on its "In plain words" tab, and
generates a retelling with a model only when the section is missing or empty.

- **Phase doc:** what the phase is for and why it is needed (the problem or
  gap it closes), what will be different once it is done — in terms of what a
  user or the team gets — and, when it matters, what it unblocks.
- **README:** what the plan is about and why it exists, what will be different
  when it is all done, how the work is split in one or two sentences, and the
  main risk if there is one.
- 3–6 sentences of prose (a short bullet list is fine). No file paths, code,
  function names, SC ids or phase numbers; explain an unavoidable technical
  term in a few words. Say only what the doc supports.
- Write it in the language the operator wrote the request in. The heading
  itself stays `## In plain words` — the dashboard matches it by name.
- A revision that changes what a phase does rewrites this section too; a
  stale plain-words section misleads exactly the reader it exists for.

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

## `## Guard` (in every phase doc, written by the planner)

What the phase run must not touch, and when it must stop and report instead of
carrying on. One `kind: guard` yaml block; the platform parses it and a later
guard stage enforces it, so — unlike a forecast — a block that exists but cannot
be read is reported as **unparsable**, not as absent.

````markdown
## Guard

```yaml
kind: guard
protected_paths:
  - ".github/workflows/*.yml"
  - "plugins/*/hooks/hooks.json"
  - "**/*_test.go"
  - "**/*.test.ts"
  - "**/*.test.tsx"
  - "scripts/tests/**/*"
stop_if:
  - out-of-scope change
  - protected path
  - access or login error
  - criterion impossible
```
````

- **Globs name files, never directories.** `scripts/tests/**/*`, not
  `scripts/tests/**` or `scripts/tests/`; `**` matches zero or more directories.
  A glob that ends in `/` or `/**` still parses, with a warning.
- **Quote every glob.** A yaml value that starts with `*` is an alias, so an
  unquoted `- **/*_test.go` makes the whole block unparsable.
- The defaults above protect CI workflows, hook manifests and tests. A phase
  that must edit a protected file (it writes or fixes tests) removes or narrows
  that glob, lists the files it will edit, and says why in `## Notes`.
- `stop_if` keeps the four defaults; add phase-specific triggers after them.
- Both keys are required; an empty list is written `[]`. Unknown keys are
  ignored.

## `## Review` (appended per review round, never by the planner)

Both review engines — the daemon's headless reviewer and `run-plan`'s
orchestrator — record each review round as one `kind: review` block under
`## Review`, immediately before `## Completion Report` (D42: one block per
round, earlier rounds are never rewritten). The reviewer emits the block right
before its `VERDICT:` line; the engine copies it into the doc.

````markdown
## Review

```yaml
kind: review
round: 1                 # 1, 2, … per fix round; plan for the plan-branch review
verdict: FAIL            # PASS | FAIL | INCONCLUSIVE — the same word as VERDICT:
complete: true           # false when the review stopped before covering its scope
scope_left: []           # with complete: false — what was not read
findings:
  - id: R1-1             # R<round>-<n>; stable, a later round's resolves cites it
    type: silent-failure # correctness | silent-failure | contract | plan-conformance | quality
    file: src/lib/export/writer.ts
    line: 84
    criterion: "upload failure is reported"   # the criterion it breaks, if any
    test_caught: false   # an existing test fails because of it
    evidence: "catch {} swallows the putObject error"
    note: "fail the upload in a test: the handler still returns 200"
resolves: []             # ids from earlier rounds this round confirms fixed
ids: []                  # - {id: <symbol/key the change relies on>, evidence: <file:line | missing>}
drift: []                # - {criterion: <ticked but not true>, file: <path>, note: <why>}
checked_callers: []      # call sites read for a changed signature (file:line)
```
````

`round` and `verdict` are required — without either the block is unparsable.
`complete` defaults to `true`; every list may be `[]`. The latest round is the
highest number, and `plan` sorts after every number. A subagent reviewer that
was resumed after hitting its turn limit records `complete: false` even when
its second turn finished.

## Verification hooks for planners

- `test -s` on README.md, manifest.json, and every phase file.
- `python3 -m json.tool plan/manifest.json` exits 0.
- Final chat message: `Plan written: {path} | {N} phases, {L} total lines`.
