# Retro — agent-system retrospectives & the improvement loop

The retro loop — the dashboard's **Health** place (`/health`; the old `/retro` address
redirects there) — answers one question: **how is the agent system performing, and what
should we change?** It turns the telemetry the daemon
already collects — plus the artifacts the agent workflow writes to the workspace — into
per-agent health scorecards, a friction board, a lessons feed, and concrete,
heuristics-only improvement recommendations with a tracked lifecycle
(`proposed → accepted → adopted → verified`).

No LLM is involved in the numbers or the recommendations: every one is a deterministic fold
over SQLite. Models are called only by the explicit **Improve** actions (an agent rewrite,
or the whole-system analysis) and by the daily trajectory judge — see the Concepts doc.

---

## 1. Data sources

Retro merges three layers that used to live in disconnected worlds:

| Layer | Source | Ingested by | Lands in |
|---|---|---|---|
| **Session telemetry** | Claude Code transcripts (`~/.claude/projects/**.jsonl`) | `internal/ingest` (tail-follow) | `sessions`, `turns` (per-agent attribution), `events` (tool calls, errors, subagent start/stop), `file_changes` |
| **Workspace artifacts** | `$AGENT_WORKSPACE_ROOT/<project>/workspace/{working,archive}/YYYY/MM/DD/<slug>/` | `internal/wsingest` (rescan every 60 s, SHA-256 hash-gated) | `task_retros`, `retro_lessons`, `retro_improvements`, `task_loops`, `task_delegations` |
| **Eval results** | promptfoo `results.json` from the `evals/` suite | `swarmery evals-import --agent <name> <file>` (manual CLI) | `eval_suites`, `eval_cases`, `eval_runs`, `eval_results` |

Three workspace artifacts are parsed per task (tolerant contract — malformed input is
warn-and-skip, never a scan failure):

- `phases/09-retrospective.md` — the Duration metrics row, `### Lesson N:` entries
  (with their `**Action**:` lines), and the Process Improvements table.
- `ORCHESTRATION.md` — `## Loop {N}` re-dispatch journal sections.
- `logs/agents.md` — the delegation ledger (Ukrainian or English header). Two row
  layouts are accepted: the legacy 4-cell `agent | phase | verdict | artifact`
  and the assessment 7-cell `agent | phase | verdict | loops | quality | mistakes | artifact`
  (tech-lead ≥ core 2.2.0). The artifact is always the last cell; a literal `|`
  inside mistakes is re-joined. Out-of-range loops (outside 0..99) and quality
  (outside 1..5) degrade to NULL without dropping the row.

## 2. Where it lives in the dashboard

Health's tabs split the loop by question; the status strip on top carries one date range
for all of them.

| Tab | What it holds |
|---|---|
| **Overview** | One sentence about the fleet, the agents that moved it, and the friction removable right now |
| **Agents** | The **health strip** — orchestrator (`main`) cost, total subagent runs, total errors, each with a vs-previous-window arrow — and the **agent scorecards**: one card per agent, sorted by runs: runs (+prev delta), error rate, success rate, cost, p95 duration, re-dispatch chip, evals chip |
| **Friction** | The **friction board** — most-denied tools with a one-click `+ rule` (creates an auto-approve rule), recurring error groups (expandable, with sample sessions), approval-wait stats |
| **Estimates** | **Estimation accuracy** — per task: estimated vs actual hours, variance badge (green ≤ ±20 %, amber ≤ ±50 %, red beyond), loop count, delegation verdict split — and the **lessons feed**: every parsed lesson across all tasks, newest first, with a client-side filter and the **Group by lesson** view (§7). The `action` chip is the point: it is the reusable instruction |
| **Advisor** | The **recommendations** — the advisor's output (§4–5) — and the agent/skill change **proposals** awaiting a decision |
| **Cost & tokens** | Cost, tokens, runs and cache over time |

Range presets (7/14/30/90 d) and the project scope (the sidebar's project switcher)
apply to everything. Open recommendations and proposals also land in the **Inbox**
(its *advisor* and *proposals* tabs), which is where the sidebar's only badge counts them.

## 3. Metric definitions

Aggregation grains deliberately mirror Health → Cost & tokens and the advisor, so a number
cited by a recommendation matches what the pages show.

| Metric | Definition |
|---|---|
| **runs** | `subagent_start` events, grouped by `payload.subagent_type` (folded: `core:x` → `x`; `NULL` → `main`, shown separately). |
| **errors** | Raw count of error events attributed to the agent via `parent_event_id → subagent_start` (events' `turn_id` is deliberately NOT used — ingest zeroes it for sidechain events). Mirrored `subagent_start` error rows are excluded to avoid double-counting failed runs. |
| **error_rate** | **Failed-run share**: distinct runs with ≥ 1 error ÷ runs, deduped on the run's `subagent_start` id. Clamped to ≤ 1 (a run spanning the window start can contribute a failed run without contributing to the run count). This is *not* errors-per-run — a run with five tool errors counts once. |
| **success_rate** | Manual session outcomes: `success / (success + fail)` over outcome-carrying sessions containing the agent's turns. Null until you judge sessions (the ✓/✗/⊘ picker on a session). |
| **cost / tokens** | Summed from `turns` where `turns.agent_name` folds to the agent (exact per-agent attribution from sidechain turns). |
| **re-dispatch rate** | Share of the agent's delegation-ledger rows whose verdict matches the bilingual grammar `re-dispatch / fail / reject / повтор / відхил / провал / фейл` (cells over 40 runes are treated as prose, not verdicts). Caveat: for *reviewer* agents a re-dispatch verdict usually means the reviewer did its job. |
| **evals chip** | `passed/total` from the latest imported eval run for the agent. |
| **approx flag** | Set when the range (or its comparison window) overlaps pruned days that only exist as daily rollups — counts there are honest but incomplete. |

## 4. The advisor — rules R1–R6 (plus R10, R11, R13)

> [!NOTE]
> The advisor registers more rules than this section documents. `advisor.Run` in
> `internal/advisor/advisor.go` is the authoritative list; the rules described below
> are the original six, plus R10 (the auto-memory index budget), R11 (the recurring
> lesson) and R13 (auto-memory stating merged PRs as open), each with its own subsection
> after the table.

`internal/advisor` runs at daemon startup, on a 24 h ticker, and on demand
(`Analyze now` → `POST /api/retro/advise`). Every rule evaluates the trailing
**14-day** window; re-runs update open recommendations' evidence in place (no
duplicates). Every card carries an `evidence` JSON with the window, counts, and sample
session ids.

| Rule | Target | Fires when | Suggested action |
|---|---|---|---|
| **R1** tool friction | tool | denied ≥ 5× and no enabled approval rule covers the tool | add an auto-approve rule (the friction board's `+ rule` does it in one click) |
| **R2** agent error rate | agent | runs ≥ 10, error events ≥ 3, and failed-run share > 2× the fleet median (among ≥ 10-run agents) | review the agent's prompt; the card cites its top error group |
| **R3** recurring error | error group | same normalized error on ≥ 3 distinct days | fix the underlying cause (prompt rule, hook, allowlist, infra) |
| **R4** re-dispatch | agent | ≥ 3 ledger rows and re-dispatch share > 25 % | sharpen the agent's brief / acceptance criteria; consider an eval case |
| **R5** stale improvement | process | a high-priority Process-Improvements row still open 14 d after its retro was ingested | do it, then mark the row `done` in the retro doc |
| **R6** cache regression | config | cache hit rate dropped > 10 p.p. vs the preceding window | check prompt/session structure changes |
| **R10** auto-memory index | memory | the project's `MEMORY.md` is > 6 KiB, **or** > 50 % of its ≥ 10 index lines are closed | run `swarmery memory consolidate` (see below) |
| **R13** stale memory fact | memory | an auto-memory line names a PR and says it is open, and the project's git history already carries its merge | fix the line by hand or on Knowledge → Memory; `swarmery memory lint` lists them (see below) |
| **R11** recurring lesson | skill | one lesson identity (`norm_title`) turns up in ≥ 3 **distinct** tasks in the window | move the lesson into the `SKILL.md` the next run actually reads |

### R10 — the auto-memory index budget

`<claude-dir>/projects/<slug>/memory/MEMORY.md` is loaded into **every** conversation in the
project, so its bytes are re-read on every turn forever and every line describing finished work
is rent paid for nothing. R10 measures that, deterministically and without an LLM: it reads the
index off disk each pass (the same auto-memory root Knowledge → Memory uses), parses its
`- [Title](file.md) — hook` lines, and classifies each hook as **closed** when it carries a
closed marker (`DONE|MERGED|CLOSED|SHIPPED|RESOLVED|LIVE`) and **no** open marker
(`OPEN`, `open:`, `tail =`, `impl open`, `awaits`). A line carrying both is an open tail and
counts as live — `MERGED … OPEN: flip the guard` stays, `FULLY CLOSED …; no open tail` closes.

Two independent triggers, because the two failure shapes differ: an index can be over budget
while entirely live (too many topics), and it can be small but mostly dead (a long tail of DONE
lines). The finding's `detail` leads with the numbers — `index 8.1 KB, 28/53 lines closed
(53%)` — and its baseline metric is `memory_index_closed_share` (lower is better).

Like R7/R8/R9 it is **self-checking**: it re-examines the world every pass, so a consolidated
index simply stops firing and the row is `resolved` automatically. `target_kind` is `memory`
(migration `0071`), it has no adoption signal, and `verify()` never selects it — it is a
notification, not a measured improvement.

Acting on it is `swarmery memory consolidate --project <path> [--dry-run]`, or the
**Consolidate index** panel on the project's Knowledge → Memory tab (`POST /api/memory/consolidate`).
Consolidation moves a closed entry's topic file to `memory/closed/`, stamps it
`closed_at` + `status: closed`, moves its index line to `memory/closed/INDEX.md`, and removes it
from `MEMORY.md`. Moving the **file** is the load-bearing half — a file left in `memory/` is
still a recall candidate even with no index line. Nothing is deleted, every touched file is
backed up into one timestamp dir first, and `--dry-run` (the default on the API) writes nothing
at all. Two entries are always held back: one with an open tail, and one whose file an **open**
memory still references with a `[[link]]` — closing that would dangle a live dependency.
There is no LLM merge here and no duplicate detection beyond these exact markers.

### R13 — auto-memory that states merged PRs as open

R10 measures the index's **size**; R13 measures its **truth**, for the one fact class that is cheap
to verify offline. An auto-memory note is written once and trusted forever — `PR #366 UNMERGED
(needs a review approval)` stayed in this repo's `MEMORY.md` weeks after the merge landed, and
every later session planned around it. Nothing re-read the fact against the world.

`internal/memlint` does, deterministically and without an LLM, network or `gh`: it reads every
`*.md` directly under the project's auto-memory root (the same `memconsolidate.AutoMemoryDirIn`
resolver R10, the Memory page and the CLI share; `closed/` is not descended), cuts each line into
**clauses** (at `;`, `, `, `. `, a spaced dash or a table bar), and in every clause that carries an
**open marker** (`OPEN|open|UNMERGED|needs a review/merge/approval|awaits|pending|not merged`) and
**no closed marker** (`MERGED|CLOSED|DONE|SHIPPED|RESOLVED`) each `#<n>` ref at a word start is a
**claim**, once per line. So `PR #340 … MERGED; #341 open` claims only #341, `MERGED + deployed
(#260, #261); OPEN: flip the guard` claims nothing, and `issues/12#issuecomment-1` is no ref at all.
The scope is a clause and not the line on purpose: the per-line first cut flagged 45 of this repo's
46 claims, nearly all lines whose "open" was about something else. Each distinct claim is checked
once against the project's own history with
`git -C <project> log --all --max-count=1 --fixed-strings --grep="Merge pull request #<n> " --grep="(#<n>)"`
— the two shapes GitHub leaves behind, a merge commit and a squash commit; the trailing space and the
parentheses keep `#36` out of `#366`. A hit is a **finding** with the merge sha and committer date. A
project without `.git` has claims it cannot verify and therefore no findings; a project without a
memory directory is skipped.

R13 fires once per non-archived project with ≥ 1 finding. `detail` names the first three —
`model-lineup.md:12 says PR #366 is open; it merged 2026-09-22 (2151242)` — and the evidence carries
`counts{claims, stale}`, every finding, and `index_path`. The baseline metric is `memory_stale_claims`
(lower is better; 0 once the lines are fixed). `target_kind` is `memory`, like R10, and like R10 it is
**self-checking**: it re-lints every pass, so a corrected line stops it firing and the row resolves on
its own. The rule never edits a memory file — the correction is the operator's, by hand or through
the project's Knowledge → Memory tab.

The same report is `swarmery memory lint --project <path> [--json]` (never opens the database),
`swarmery memory lint --all` (every non-archived project; the one path that reads the database,
read-only and without migrations), and `GET /api/projects/{id}/memory/lint`. All three exit or answer
successfully whether or not they found anything: it is a report, not a gate.

### R11 — the lesson the fleet keeps re-learning

A retrospective lesson is, by construction, something that already went wrong once. The fleet
writes it down honestly and then learns it again three tasks later, because a lesson lives in a
finished task's retro doc and nothing carries it forward into the procedure the next run reads.

R11 detects exactly that. `retro_lessons` rows are DELETE+reinserted on every rescan, so the row
id could never be a cross-task identity — migration **0070** adds `retro_lessons.norm_title`,
written by `wsingest.NormalizeLessonTitle`: lowercase, a leading `Lesson N:` ordinal dropped,
punctuation folded to spaces, the stop-words `the a an of to in for and` removed, whitespace
collapsed, 80 runes. So `Lesson 3: Sync-Cache Before Build!` and
`sync the cache before the build` are one identity. The fold is deliberately not clever — no
stemming, no edit distance, no synonyms: a fold that guesses would merge two different lessons
under one heading and nobody could see it had happened. A title that folds to nothing keeps
`norm_title = ''`, and **every** reader skips `''` — it is the "not folded yet" marker, never an
identity. Rows written before 0070 are folded once per daemon start by the wsingest backfill
(`retro lessons backfilled: N` in the log); the insert path folds everything since.

The rule fires when one `norm_title` appears in ≥ 3 **distinct tasks** inside the 14-day window.
Distinct tasks, never rows: one retro repeating itself is a duplicated paragraph, not a pattern.
`detail` reads `"<title>" recurred in N tasks: <slug…>` (the first 5 ids, then `(+K more)`) plus
the most recent `**Action**:` line; the baseline metric is `lesson_task_count` (lower is better).

`target_kind` is **`skill`** (migration `0072`), not `process` like R5: R5 points at one retro's
improvement row and asks a human to do it, R11 points at a procedure that should have carried the
lesson and never did — and the improve loop's skill proposals route on that kind, so it is
load-bearing, not decoration. Unlike R7/R8/R9/R10, R11 is **not** self-checking: it reads stored
rows over a trailing window, so a fortnight with no retrospectives looks exactly like a fortnight
in which the lesson was finally absorbed, and closing an `accepted` row on that would be guessing.
A `proposed` row is still swept when the rule goes quiet, and re-proposed if the lesson returns.

The same fold backs the **Group by lesson** toggle on Health → Estimates — see `?group=1` in §7.

### Skill proposals — what R11 turns into

Accepting an R11 recommendation routes it into the **same** human-gated diff pipeline agent
rewrites use (`internal/improve`), pointed at a `SKILL.md` instead of an agent definition. The
proposal row says which kind of file it targets: `agent_change_proposals.target_kind` is `agent`
or `skill`, and `target_path` holds the repo-relative `plugins/<pack>/skills/<name>/SKILL.md`
(migration `0074`). Nothing else about the pipeline changes — the diff still needs a human
Approve, and it still has to pass the neutrality scan, the frontmatter check, the hard path-scope
gate and the ≤120-line cap before a PR exists.

**Resolving the target.** A recommendation's `target` is the lesson identity (`norm_title`), not a
file. The file comes from the lesson's most recent `**Action**:` line when it names a skill —
the literal token `skills/<name>` — resolved against `origin/main` of the apply repo. Two things
can go wrong, and both end in a row you can see rather than in silence:

| Action line | Outcome |
|---|---|
| names `skills/<name>` that a pack ships | proposal `proposed`, `target_path` set, diff generated |
| names no skill, or one no pack ships | proposal **`needs_target`**, `target_path` empty, no model run |

A `needs_target` row is real evidence with an unknown file: Health → Advisor shows it with the
reason on the card, the target cell reading `no target file — dismissing is the only transition`,
and a **Dismiss** button. Dismissing is the only transition it has — approving
is refused, because there is nothing to apply. There is no target picker in this phase; if one is
added, that cell's copy and this paragraph change together. It counts as OPEN for the one-open invariant, so a
lesson nobody can place cannot accumulate duplicate rows; dismissing frees the slot and the rule
re-proposes if the lesson recurs.

**What the loop may NOT do.** The path-scope gate still permits exactly one changed path, and for
a skill proposal that path is the `SKILL.md`. A skill's sibling `resources/*.md` files are
off-limits in this phase — a diff touching one is rejected whole with gate `path scope`, not
trimmed. The model is told so in its prompt, and the gate enforces it regardless.

**One open proposal per TARGET**, not per name (migration `0074` replaces `0022`'s index). An
agent and a skill may share a name without colliding; two lessons routed at the same `SKILL.md`
collide, and the second is refused. The semver bump follows whichever pack owns the edited file,
with `plugins/core` additionally mirroring the marketplace `metadata.version`.

## 5. Recommendation lifecycle

```
proposed ──Accept──▶ accepted ──(auto)──▶ adopted ──(auto, ≥7 d)──▶ verified
    │                    │
    └──Dismiss──▶ dismissed (30-day snooze, then re-proposed if still firing)
```

- **Accept** = "I will act on this". It snapshots a **baseline** of the rule's metric.
  The button changes nothing else — the actual fix is yours.
- **Adopted** is detected automatically, per target kind:
  - *agent* — the agent's prompt file changed (a new `agent_versions` row after
    acceptance; the system registry versions every agent by content hash). A target
    absent from the registry (an ad-hoc delegation-ledger label) has no adoption
    signal and verifies directly from `accepted`;
  - *tool* — an enabled approval rule covering the tool was created after acceptance;
  - *process* — the referenced improvement row's status flipped to done/closed/виконано;
  - *error_group* / *config* — no detectable adoption; they go straight from `accepted`
    to verification.
- **Verified** requires ≥ 7 days after adoption, enough post-change traffic (activity
  floors — absence of data never verifies), and ≥ 20 % relative improvement vs the
  baseline. Otherwise the card stays `adopted` with a "no measurable improvement yet"
  note. Baselines are metric-versioned: if a metric is ever redefined, the comparer
  re-baselines instead of comparing incompatible numbers.
- While the clock runs, the card's status chip counts down to the check ("verify
  check in N d", then "awaiting ≥20 % improvement") and shows the metric's baseline
  value, its latest observed value, and the target it must reach.
- All transitions are predicate-guarded — a dismiss racing the 24 h run cannot be
  resurrected, and the API returns 409 on conflicting PATCHes.

Proposed recommendations are counted in the Inbox (sidebar badge, *advisor* tab) and in
the Advisor tab's `N open` count.

## 6. Working with Retro — playbooks

### Daily loop

1. Open the Inbox's *advisor* tab (or Health → Advisor) when it has something open.
2. Expand `evidence` on new cards; follow sample session ids when in doubt.
3. **Accept** what you will fix, **Dismiss** noise (it returns in 30 days if real).
4. Make the fix (prompt edit, approval rule, convention, infra).
5. The system advances the card to `adopted`/`verified` on its own; the collapsed
   Verified section accumulates the proof that changes actually worked.

### Closing an agent recommendation (worked example)

The first production R2 (implementation-agent, 90 % failed-run share) was closed like
this: query the DB for the agent's error taxonomy → top class was worktree-path
violations → Accept in the dashboard (baseline snapshotted) → add a targeted
"tool-error hygiene" section to the agent's prompt → bump the plugin semver, merge,
sync the plugin cache → sysscan versions the file → the next advisor run flips the card
to `adopted` automatically. Verification compares failed-run share a week later.

### Feeding the lessons pipeline

Lessons only exist if tasks finish **phase 9**. The parser expects, inside
`phases/09-retrospective.md`:

- a metrics table row `| **Duration** | <est>h | <act>h | <var>% |` (cells independently
  optional);
- a `## 💡 Lessons Learned` section with `### Lesson N: <title>` entries — body until the
  next heading, optional `**Action**: <one-line instruction>`;
- a `## 📈 Process Improvements` table (`| text | priority | owner | status |`) —
  `{{placeholder}}` rows are skipped; **update `status` to `done` when an improvement
  ships**, both for honesty and because R5 watches this column.

The template lives at
`plugins/core/templates/working/phases/09-retrospective.template.md`; the tech-lead flow
writes it via the `session-closeout` skill, and `agent-work.sh complete` warns when a task is
archived without one.

### Importing eval results

```bash
cd evals && npx promptfoo@latest eval   # produces results.json
swarmery evals-import --agent tech-lead path/to/results.json
```

Idempotent per (suite, started_at); unknown agent names are a hard error listing the
known ones. The latest run appears as the scorecard's `evals` chip.

## 7. API reference

| Endpoint | Purpose |
|---|---|
| `GET /api/retro/agents?from&to&project` | scorecard rows + `main` + prev-window block |
| `GET /api/retro/friction?from&to&project` | denied tools (`has_rule`), error groups, approval waits |
| `GET /api/retro/lessons?from&to&project` | lessons feed (newest first, limit 100) |
| `GET /api/retro/lessons?group=1&…` | the SAME window folded by `norm_title`, most-recurring first |
| `GET /api/retro/tasks?from&to&project` | estimation variance, loops, delegation verdicts (limit 200) |
| `GET /api/retro/recommendations?status=` | CSV filter; default `proposed,accepted,adopted`; `status=all` |
| `PATCH /api/retro/recommendations/{id}` | `{"status":"accepted"\|"dismissed"}`; legal transitions only (422), conflict → 409; local-origin only |
| `POST /api/retro/advise` | run the advisor now; returns `{proposed, updated, adopted, verified}`; local-origin only |

### `?group=1` — the grouped lessons view

`group=1` (also `true`/`yes`/`on`, or a bare `?group`; `0`/`false` opts out) keeps the window and
the project scope identical and only changes the fold, so the two views can never disagree about
what the range contained:

```json
{ "groups": [
  { "norm_title": "sync cache before build",
    "title": "Sync-cache before build",
    "tasks": ["2026-09-10-task-a", "2026-09-08-task-b", "2026-09-04-task-c"],
    "count": 3,
    "latest_action": "add it to the build skill" }
] }
```

Ordered by `count` descending, `norm_title` breaking ties so two calls over an unchanged window
return the same order; limit 100 groups. `count` is **distinct tasks**, `title` and
`latest_action` come from the most recent occurrence (`latest_action` is `null` when that
occurrence carried no `**Action**:` line), `tasks` is newest first. Rows with an empty
`norm_title` are excluded — grouping by the "not folded yet" marker would pile every unrelated
pre-0070 lesson into one bogus, high-count group at the top of exactly the view meant to show
what recurs. At `count ≥ 3` the **Group by lesson** view marks the row amber: that
is the same threshold R11 fires on.

## 8. Storage

- Migration **0018** — `task_artifacts` (hash gate), `task_retros`, `retro_lessons`,
  `retro_improvements`, `task_loops`, `task_delegations`.
- Migration **0019** — `recommendations` (rule, target, evidence JSON, status, unique
  `dedup_key`, baseline JSON).
- Migration **0070** — `retro_lessons.norm_title` + `idx_retro_lessons_norm_title`: the lesson's
  cross-task identity (R11, `?group=1`). `NOT NULL DEFAULT ''`; `''` means "not folded yet".
- Migration **0072** — widens the `recommendations.target_kind` CHECK with `'skill'` (R11).
  Numbered **above 0071 on purpose**: migrations apply in filename order and 0071 rebuilds the
  table with its vocabulary spelled out in full, so a widening numbered below it would be
  silently undone on a fresh database with no error anywhere.
- Migration **0074** — `agent_change_proposals.target_kind` + `target_path`, the `needs_target`
  status, and a one-open partial unique index keyed on
  `(target_kind, COALESCE(NULLIF(target_path,''), agent))` instead of `0022`'s `(agent)`. Numbered
  **above 0073** for the same filename-order reason as 0072.

## 9. Cadences

| Job | Cadence |
|---|---|
| transcript ingest | tail-follow (near-realtime) |
| workspace artifact scan | 60 s, hash-gated (reparse only on content change) |
| system registry scan (agent versioning) | periodic; picks up plugin-cache changes |
| advisor | daemon startup + every 24 h + `Analyze now` |
