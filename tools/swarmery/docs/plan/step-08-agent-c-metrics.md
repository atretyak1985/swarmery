# Step 08 — Agent C: cost & today-stats

## Header

| Field | Value |
|---|---|
| Phase | 3 — Parallel wave |
| Duration | 1 agent session, ~2–3 h (MEDIUM confidence — smallest of the wave) |
| Type | Agent session (code, runs in parallel with steps 06, 07) |
| Risk | Low-Medium — wrong pricing silently corrupts $ totals |
| Dependencies | Step 05 gate PASS; worktree `/Volumes/Work/swarmery-wt-metrics` (work in `tools/swarmery`) |

## Goal

Per-turn USD cost from usage fields + `config/pricing.json` (web-verified prices),
the frozen `GET /api/stats/today` endpoint, and a `recost` backfill command.
Honesty rule: unknown model → cost NULL, never 0.

## Automation

Fresh Claude Code session in `/Volumes/Work/swarmery-wt-metrics/tools/swarmery` (worktree, branch
`feat/swarmery-metrics`). Needs web search for pricing verification.

## Agent Prompt

```
Reference: docs/plan/step-08-agent-c-metrics.md

Context:
Репозиторій Swarmery після T1. Гілка feat/swarmery-metrics (worktree). Прочитай
swarmery-design.md (розділи 1-2), docs/jsonl-format.md (секція про usage),
internal/store. НЕ чіпай web/ і internal/ingest (крім хука нижче) —
паралельні агенти. У internal/api — тільки блок "// wave C: stats"
у routes.go.

Tasks:
1. config/pricing.json: ціни моделей (input/output/cache_read/cache_write
   за 1M токенів). Заповни актуальними цінами Claude-моделей — ПЕРЕВІР
   через web search на platform.claude.com, НЕ з памʼяті. Читання на
   старті, hot-reload не потрібен.
2. internal/cost: розрахунок cost_usd для turn з usage-полів + назви
   моделі; невідома модель → cost NULL + warn (не 0, щоб не брехати
   в сумах).
3. Інтеграційна точка: чиста функція EnrichTurn(turn) — виклич її з
   ОДНОГО місця в ingest (мінімальний дотик, познач коментарем
   // metrics hook для мерджу).
4. API: GET /api/stats/today?project= — відповідь СТРОГО за типом
   StatsToday з web/src/api/types.ts (заморожено):
   {sessions, active, tokens_in, tokens_out, cost_usd, errors} —
   агрегат по events/turns за сьогодні (локальна TZ). Без rollup-таблиць —
   прямий запит, на MVP-обсягах ок.
5. Backfill-команда: swarmery recost — перерахунок cost_usd для всіх turns
   (на випадок зміни pricing.json); попереджай, якщо демон запущений
   (одночасний запис у WAL).

Boundaries:
- НЕ створюй daily_rollups логіку (Фаза 6). НЕ чіпай web/ і types.ts.
- Зміни в internal/ingest — тільки один виклик EnrichTurn.
- Жодних нових зовнішніх залежностей.

Output / Validation:
go test: табличні кейси розрахунку (з cache-токенами; невідома модель →
NULL; нульовий usage). curl /api/stats/today повертає адекватні числа на
заінджещених fixtures. Conventional commits у feat/swarmery-metrics. Заповни
Completion Report у docs/plan/step-08-agent-c-metrics.md (worktree).
```

## Detailed Instructions

- Cost formula per turn: `(in/1e6)*p.input + (out/1e6)*p.output +
  (cache_read/1e6)*p.cache_read + (cache_write/1e6)*p.cache_write`; round only at
  display time, store full float.
- `pricing.json` keys must match model names as they appear in JSONL (see
  `docs/jsonl-format.md`); include a `fallback_prefixes` map if JSONL uses versioned
  ids (e.g., prefix-match `claude-sonnet-…`).
- SUM over NULL costs: `cost_usd` in StatsToday is NULL only if **all** turns are
  unpriced; otherwise sum priced turns and log how many were skipped.

## Success Criteria

- [x] `go test ./internal/cost/...` green with table-driven cases incl. cache tokens and unknown-model→NULL
- [x] `pricing.json` values match platform.claude.com at implementation time (cite URL in commit body)
- [x] `curl ':7777/api/stats/today'` on fixtures returns numbers consistent with fixture usage sums
- [x] `swarmery recost` recomputes all turns idempotently
- [x] Diff touches only `internal/cost`, `config/`, one `EnrichTurn` call in ingest, routes.go wave-C block, `cmd/` (+ new `internal/api/stats*.go` handler files, allowed)

## Navigation

Previous: [step-07-agent-b-frontend.md](step-07-agent-b-frontend.md) (parallel) · Next: [step-09-quality-gate-parallel-wave.md](step-09-quality-gate-parallel-wave.md) · Index: [00-plan.md](00-plan.md)

### Completion Report

```
Date/agent: 2026-07-12 · Agent C (cost & today-stats), Claude Code session
Branch head SHA: 5adf313 (code; this docs commit follows)
Pricing source URL: https://platform.claude.com/docs/en/about-claude/pricing (fetched 2026-07-12)
Models priced: claude-fable-5, claude-mythos-5, claude-opus-4-8, claude-opus-4-7,
  claude-opus-4-6, claude-opus-4-5, claude-opus-4-1, claude-sonnet-5,
  claude-sonnet-4-6, claude-sonnet-4-5, claude-haiku-4-5
  (+ fallback_prefixes for date-suffixed ids, e.g. claude-haiku-4-5-20251001)
CONTRACT-REQUESTS entries: 1 — add `model: string | null` to Turn (turns.model
  column) so recost can price per-turn exactly instead of via sessions.model.

Validation evidence:
- go vet ./... && go test ./... green (cost: 4 test funcs / 12 subtests; api:
  stats endpoint httptest incl. all-unpriced→null and day-boundary cases).
- Fixtures re-dated to today, ingested into scratch --db, served, curled:
  {"sessions":3,"active":0,"tokens_in":25891,"tokens_out":4976,
   "cost_usd":1.020192,"errors":1}
  — identical to independently computed fixture sums (dedup by message.id).
- `swarmery recost` on the scratch db: 18 turns examined, 14 priced,
  4 no-usage (NULL); values identical to ingest-time costs and stable across
  repeated runs; daemon-running warning fires when the API port answers.

Notes for integration (step 10):
- cache_write priced at the 5m-TTL rate; JSONL usage doesn't split 5m/1h.
- claude-sonnet-5 carries introductory pricing (ends 2026-08-31) — bump
  pricing.json + `swarmery recost` on 2026-09-01.
- SWARMERY_PRICING env overrides the embedded table without a rebuild.
- Sidechain (subagent) usage is not priced: sidechains create no turns rows
  by design; their tokens are visible via subagent_stop payload totalTokens.
```
