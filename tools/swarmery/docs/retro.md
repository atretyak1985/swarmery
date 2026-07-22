# Retro — agent-system retrospectives & the improvement loop

The Retro section (`/retro` in the dashboard sidebar) answers one question: **how is the
agent system performing, and what should we change?** It turns the telemetry the daemon
already collects — plus the artifacts the agent workflow writes to the workspace — into
per-agent health scorecards, a friction board, a lessons feed, and concrete,
heuristics-only improvement recommendations with a tracked lifecycle
(`proposed → accepted → adopted → verified`).

No LLM is involved anywhere in this pipeline: every number and every recommendation is a
deterministic fold over SQLite.

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
- `logs/agents.md` — the delegation ledger (`| Агент | Фаза | Вердикт | Артефакт |`,
  English header accepted too).

## 2. Page tour

- **Recommendations rail** (top) — the advisor's output; see §4–5.
- **Health strip** — orchestrator (`main`) cost, total subagent runs, total errors for the
  selected range, each with a vs-previous-window arrow.
- **Agent scorecards** — one card per agent, sorted by runs: runs (+prev delta),
  error rate, success rate, cost, p95 duration, re-dispatch chip, evals chip.
- **Friction board** — most-denied tools with a one-click `+ rule` (creates an
  auto-approve rule), recurring error groups (expandable, with sample sessions),
  approval-wait stats.
- **Lessons feed** — every parsed lesson across all tasks, newest first, with a
  client-side filter. The `action` chip is the point: it is the reusable instruction.
- **Estimation accuracy** — per task: estimated vs actual hours, variance badge
  (green ≤ ±20 %, amber ≤ ±50 %, red beyond), loop count, delegation verdict split.

Range presets (7/14/30/90 d) and the header project scope apply to everything.

## 3. Metric definitions

Aggregation grains deliberately mirror the Analytics page and the advisor, so a number
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

## 4. The advisor — rules R1–R6

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

The `Retro` sidebar badge shows the count of `proposed` recommendations.

## 6. Working with Retro — playbooks

### Daily loop

1. Open `/retro` when the badge is non-zero.
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
writes it via `@retrospective-agent`, and `agent-work.sh complete` warns when a task is
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
| `GET /api/retro/tasks?from&to&project` | estimation variance, loops, delegation verdicts (limit 200) |
| `GET /api/retro/recommendations?status=` | CSV filter; default `proposed,accepted,adopted`; `status=all` |
| `PATCH /api/retro/recommendations/{id}` | `{"status":"accepted"\|"dismissed"}`; legal transitions only (422), conflict → 409; local-origin only |
| `POST /api/retro/advise` | run the advisor now; returns `{proposed, updated, adopted, verified}`; local-origin only |

## 8. Storage

- Migration **0018** — `task_artifacts` (hash gate), `task_retros`, `retro_lessons`,
  `retro_improvements`, `task_loops`, `task_delegations`.
- Migration **0019** — `recommendations` (rule, target, evidence JSON, status, unique
  `dedup_key`, baseline JSON).

## 9. Cadences

| Job | Cadence |
|---|---|
| transcript ingest | tail-follow (near-realtime) |
| workspace artifact scan | 60 s, hash-gated (reparse only on content change) |
| system registry scan (agent versioning) | periodic; picks up plugin-cache changes |
| advisor | daemon startup + every 24 h + `Analyze now` |
