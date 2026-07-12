# Step 10 — T3: integration (merge A+B+C into main)

## Header

| Field | Value |
|---|---|
| Phase | 4 — Integration, install, ship |
| Duration | 1 agent session, ~2–4 h (MEDIUM confidence — conflicts pre-limited by routes.go blocks) |
| Type | Agent session (merge + glue, no new features) |
| Risk | Medium — contract mismatches surface here |
| Dependencies | Step 09 gate PASS |

## Goal

Merge the three branches into `main` in a fixed order, resolve contract requests,
connect the frontend to the real API/WS, and validate the whole system on this
machine's real history.

## Automation

Fresh Claude Code session in `/Volumes/Work/swarmery/tools/swarmery` (main checkout, not a worktree).

## Agent Prompt

```
Reference: docs/plan/step-10-integration.md

Context:
Репозиторій Swarmery, гілки feat/swarmery-ingest, feat/swarmery-frontend, feat/swarmery-metrics готові
і пройшли gate (step 09). Прочитай зведений список contract-requests із
docs/plan/step-09-quality-gate-parallel-wave.md (Completion Report),
web/CONTRACT-REQUESTS.md з гілок, docs/ws-protocol.md, і дифи всіх трьох
гілок відносно main.

Tasks:
1. Змердж гілки в main у порядку: ingest → metrics → frontend, розвʼяжи
   конфлікти (routes.go має окремі блоки wave A/C — конфліктів бути
   майже не повинно). Розбіжності з CONTRACT-REQUESTS — реалізуй на
   бекенді або задокументуй у файлі, чому ні.
2. Прибери mock-режим за замовчуванням (VITE_MOCK=1 лишається опцією),
   зʼєднай фронтенд з реальним API і WS. Онови types.ts з фінальних
   Go-структур — це ЄДИНЕ місце, де types.ts можна змінювати.
3. make build → один бінарник з embedded фронтендом.
4. Наскрізний прогін: повний backfill реальної історії цієї машини,
   перевір Overview і 3-4 session details різних проєктів
   (swarmery, bloomblum, Skygor) — таймлайн, субагенти, дифи, вартість.

Boundaries:
- Ніяких нових фіч. Тільки мердж, склейка, фікси інтеграційних багів.
- Кожен мердж — окремий merge-коміт (conventional), CI зелений після кожного.

Output / Validation:
go test + npm run build зелені в main. ./swarmery serve на реальних даних:
підсумок — скільки проєктів/сесій/подій заінджестено, сумарна вартість за
сьогодні, URL. Live-тест: нова сесія claude зʼявляється в Overview без
перезавантаження сторінки. Заповни Completion Report у
docs/plan/step-10-integration.md.
```

## Detailed Instructions

- Merge order rationale: ingest brings WS + schema services (foundation), metrics
  hooks into ingest (`EnrichTurn`), frontend consumes both — foundation → wire →
  consume.
- After merges, clean up:
  ```bash
  cd /Volumes/Work/swarmery/tools/swarmery
  git -C /Volumes/Work/swarmery worktree remove ../swarmery-wt-ingest ../swarmery-wt-frontend ../swarmery-wt-metrics
  git branch -d feat/swarmery-ingest feat/swarmery-frontend feat/swarmery-metrics
  ```
- Rollback: each branch lands as its own merge commit — `git revert -m 1 <merge-sha>`
  in reverse order (frontend → metrics → ingest) restores the pre-integration state;
  DB schema was untouched (additive rule), so no migration rollback is needed.

## Success Criteria

- [ ] `main` contains all three merges; CI green; worktrees removed
- [ ] `make build` single binary; dashboard serves embedded SPA (no Vite dev server)
- [ ] Full real backfill succeeds; Overview shows today's tokens + cost
- [ ] 3–4 real session details verified (timeline, nested subagents, diffs, cost)
- [ ] Live test: new `claude` session visible in Overview < 3 s, no reload
- [ ] Every CONTRACT-REQUESTS entry implemented or answered in the file

## Navigation

Previous: [step-09-quality-gate-parallel-wave.md](step-09-quality-gate-parallel-wave.md) · Next: [step-11-install-daemon.md](step-11-install-daemon.md) · Index: [00-plan.md](00-plan.md)

### Completion Report

```
Date/agent: 2026-07-12 / integration agent (Claude Code, main checkout).
Merge SHAs: ingest 834ebc3 → metrics bdd7dfb → frontend eb7984a (fixed order, one merge commit each); contract-request implementation 01fd6ef.
Conflicts: cmd/swarmery/main.go (ingest serve/backfill vs metrics recost — union: all four subcommands kept) and web/CONTRACT-REQUESTS.md (both branches appended at the marker — union of all three entries). routes.go merged clean (wave blocks).
Contract requests: (1) event_appended → {sessionId, event} IMPLEMENTED (ws.go + ws-protocol.md + types.ts + frontend; session-card "now: <last action>" live line enabled on Overview/Sessions); (2) turns.model IMPLEMENTED (migration 0002, ingest writes per-message model, recost COALESCE(turns.model, sessions.model), Turn DTO exposes model); (3) session-list aggregates DEFERRED — answered in web/CONTRACT-REQUESTS.md (phase 2 candidates).
Backfill stats (full ~/.claude/projects, scratch DB): 457 files / 114,545 lines in 6.8 s → 11 projects, 115 sessions, 12,213 turns (11,009 assistant turns, 100% with model), 24,779 events, 2,979 file_changes, 0 skipped, 0 errors.
Today (/api/stats/today): sessions 2, tokens_in 58,345, tokens_out 164,337, cost $40.56, errors 55.
Session-detail spot check (4 largest, different projects): swarmery #114 (158 turns / 813 events / 14 diffs / $27.92 / 15 subagents), Naomi School #25 (436/1902/36/$106.92/18), bloomblum #100 (43/833/34/$6.39/15), CarsFinders #14 (91/1152/9/$23.27/24) — timeline, nested subagents (parentEventId), diffs, per-turn models and cost all present and sane.
WS live check: serve with SWARMERY_PROJECTS_ROOT at a temp dir; new-file append → event_appended with sessionId in 8 ms; incremental tail append → 158 ms (both « 3 s). Embedded SPA verified on the built binary (make build, single binary).
Integration bugs fixed: none beyond the two known conflict points — no contract mismatches surfaced (gate 09 pre-limits held). Note: model "<synthetic>" (50 turns) has no pricing → cost_usd NULL by the honesty rule.
Cleanup: worktrees swarmery-wt-{ingest,metrics,frontend} removed; branches deleted. swarmery-wt-install + feat/swarmery-install untouched (still in flight).
Validation: go vet + go test, tsc --noEmit, npm run build all green on final main.
```
