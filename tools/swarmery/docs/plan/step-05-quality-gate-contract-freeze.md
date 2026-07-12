# Step 05 — QUALITY GATE: contract freeze + worktrees

## Header

| Field | Value |
|---|---|
| Phase | 2 — Vertical slice & contract freeze (gate) |
| Duration | ~30 min (HIGH confidence) |
| Type | Quality gate — human + one short agent task |
| Risk | High if skipped — parallel branches would diverge on contracts |
| Dependencies | Step 04 |

## Goal

Freeze the shared contracts before three agents work in parallel: (a) DB schema —
additive-only from here; (b) `web/src/api/types.ts` generated from Go structs as the
single source of truth, **including the `/api/stats/today` response shape** (fix F4);
(c) WS event names. Then create the three worktrees.

## Automation

Short agent session in `/Volumes/Work/swarmery/tools/swarmery` for types generation; human for tag +
worktrees.

## Agent Prompt

```
Reference: docs/plan/step-05-quality-gate-contract-freeze.md

Context: репозиторій Swarmery після T1 (step 04). Схема БД і API-відповіді
заморожуються перед паралельною хвилею.

Tasks:
1. Згенеруй web/src/api/types.ts з Go-структур API-відповідей
   (Project, Session, Turn, Event, FileChange, відповіді
   /api/projects, /api/sessions, /api/sessions/{id}).
2. Додай туди ЗАРАЗ типи майбутніх контрактів паралельної хвилі:
   - StatsToday: {sessions:number; active:number; tokens_in:number;
     tokens_out:number; cost_usd:number|null; errors:number}  // Agent C реалізує
   - WSMessage: {type:'session_started'|'session_updated'|'event_appended';
     payload: Session|Event}                                   // Agent A реалізує
3. Перевір: npm run build у web/ зелений, tsc strict без помилок.
4. Коміт: "chore: freeze API contract types before parallel wave".

Output: вміст types.ts. Заповни Completion Report.
```

## Detailed Instructions

Human steps after the agent commit (worktrees are of the **swarmery** repo —
swarmery lives at `tools/swarmery` inside it; each wave agent works in
`<worktree>/tools/swarmery`):

```bash
cd /Volumes/Work/swarmery
git tag swarmery-contract-freeze-v1
git worktree add ../swarmery-wt-ingest   -b feat/swarmery-ingest
git worktree add ../swarmery-wt-frontend -b feat/swarmery-frontend
git worktree add ../swarmery-wt-metrics  -b feat/swarmery-metrics
```

Freeze rules (record in the gate verdict):
- Schema changes from now on: **new tables/columns only**, never renames/drops.
- `types.ts` changes only via integration (step 10); branch agents request changes
  through `web/CONTRACT-REQUESTS.md`.
- WS event names fixed: `session_started | session_updated | event_appended`.

## Success Criteria

- [ ] `types.ts` committed, includes `StatsToday` and `WSMessage`; `tsc` strict green
- [ ] Tag `swarmery-contract-freeze-v1` exists on the freeze commit
- [ ] Three worktrees exist with branches `feat/swarmery-ingest`, `feat/swarmery-frontend`, `feat/swarmery-metrics`
- [ ] GATE VERDICT recorded: PASS / FAIL

## Navigation

Previous: [step-04-vertical-slice.md](step-04-vertical-slice.md) · Next: [step-06-agent-a-ingest.md](step-06-agent-a-ingest.md) · Index: [00-plan.md](00-plan.md)

### Completion Report

```
Date: 2026-07-12 · Tag SHA: 4858f2d (swarmery-contract-freeze-v1) · Worktrees created: /Volumes/Work/swarmery-wt-{ingest,frontend,metrics} (feat/swarmery-{ingest,frontend,metrics}) · Verdict: PASS. Note: freeze commits rewritten once before tagging to purge an accidentally committed 15MB binary (old 'swormery' name escaped the anchored .gitignore); history is clean.
```

Agent part details:
- `web/src/api/types.ts` created from the Go DTOs in `internal/api/handlers.go`
  (Project, Session, Turn, Event, FileChange, SessionDetail + endpoint response
  aliases), plus frozen future contracts `StatsToday` and `WSMessage`
  (`session_started | session_updated | event_appended`).
- `web/src/api.ts` and `web/src/App.tsx` refactored to import from `types.ts`
  (no duplicate type declarations remain).
- `web/CONTRACT-REQUESTS.md` created — branch agents append change requests
  there; resolved at integration (step 10).
- Verified: `npm run build` green; `npx tsc --noEmit` (strict) green.
