# Step 04 — T1: vertical slice (skeleton end-to-end)

## Header

| Field | Value |
|---|---|
| Phase | 2 — Vertical slice & contract freeze |
| Duration | 1 agent session, ~2–4 h (MEDIUM confidence — scaffold-sized session) |
| Type | Agent session (code, TDD) |
| Risk | Medium — this skeleton is what three parallel agents extend |
| Dependencies | Step 03 gate PASS |

## Goal

A thin end-to-end path on `main`: full-schema migrations → single-file JSONL ingest →
REST API → raw sessions page → one-binary build. Plus minimal CI so the parallel wave
lands on a protected trunk (fix F5).

## Automation

Fresh Claude Code session in `/Volumes/Work/swarmery/tools/swarmery`.

## Agent Prompt

```
Reference: docs/plan/step-04-vertical-slice.md

Context:
Репозиторій Swarmery. Прочитай swarmery-design.md і docs/jsonl-format.md
повністю. Fixtures у testdata/fixtures/. Стек зафіксований: Go 1.22+
(модуль github.com/atretyak1985/swarmery), modernc.org/sqlite (без CGO),
net/http ServeMux (Go 1.22 patterns), React 18 + TS strict + Vite + Tailwind
через go:embed. Без ORM і фреймворків.

Tasks (тонкий наскрізний шлях — скелет для паралельних агентів):
1. Структура репо: cmd/swarmery, internal/{ingest,store,api}, web/, Makefile.
2. store: ВЛАСНИЙ простий migration runner з embedded .sql (НЕ golang-migrate —
   бюджет залежностей). Міграція 0001 — ПОВНА схема зі swarmery-design.md §2
   (усі таблиці, включно з майбутніми фазами) + службова
   file_offsets(file_path, byte_offset, inode). agents і skills створюй ДО
   events (events має FK на них). БД у ~/.swarmery/swarmery.db, PRAGMA WAL.
3. ingest: мінімальний парсер — один .jsonl файл (`swarmery ingest <file>`),
   розкласти в projects/sessions/turns/events/file_changes згідно мапінгу з
   docs/jsonl-format.md. Незнайомий тип запису → events type='unknown',
   payload=сирий JSON. Битий рядок → warn у лог, пропустити.
   Статуси сесій: тільки active|idle|completed (waiting_approval/killed — Фаза 2).
4. api: GET /api/projects; GET /api/sessions?project=&status=;
   GET /api/sessions/{id} (з turns, events, file_changes). JSON.
   Реєстрацію роутів винеси в internal/api/routes.go з окремими блоками-
   секціями і коментарями "// wave A: WS" та "// wave C: stats" — щоб
   паралельні гілки не конфліктували в одному місці.
5. web: одна сторінка — список сесій, клік → сирий detail (без дизайну).
   Vite dev proxy на :7777. TypeScript strict.
6. Порт: прапорець --port + env SWARMERY_PORT, дефолт 7777.
7. Makefile: build (vite build + go build з embed), dev, test.
8. CI: .github/workflows/ci.yml — go vet ./..., go test ./..., npm ci &&
   npm run build (web/). Тригер: push/PR на main і feat/**.

Boundaries:
- БЕЗ watcher/fsnotify, БЕЗ WebSocket, БЕЗ дедуплікації, БЕЗ вартості,
  БЕЗ Overview — це наступні кроки. Тільки скелет.
- Парсер пиши ВИКЛЮЧНО проти структури з docs/jsonl-format.md і fixtures.
- Максимум 3 залежності поза stdlib на бекенді (зараз потрібна лише
  modernc.org/sqlite; websocket і fsnotify додасть гілка A).
- TDD: спочатку тест парсера на fixture, потім реалізація.

Output / Validation:
1. go vet + go test зелені; тест парсера на кожному fixture: кількість
   sessions/turns/events і що субагент став events.type='subagent_start/stop'
   з parent_event_id.
2. make build → один бінарник; ./swarmery ingest <реальний файл з
   ~/.claude/projects> → ./swarmery serve → покажи URL і скільки записів
   створено. Conventional commits. Заповни Completion Report у
   docs/plan/step-04-vertical-slice.md.
```

## Detailed Instructions

- Post-session human verification:
  ```bash
  cd /Volumes/Work/swarmery/tools/swarmery && go vet ./... && go test ./... && make build
  ./swarmery ingest ~/.claude/projects/-Volumes-Work-swarmery/<newest>.jsonl
  ./swarmery serve   # open http://localhost:7777
  ```
- Dependency budget ledger (backend, max 3 total for MVP): `modernc.org/sqlite` (here),
  `github.com/coder/websocket` + `github.com/fsnotify/fsnotify` (added by Agent A).
- If the parser hits a fixture construct the format doc doesn't explain — stop and
  update `docs/jsonl-format.md` first (evidence rule from step 02 still applies).

## Success Criteria

- [ ] `go vet`, `go test`, `make build` green; single binary produced
- [ ] Migration creates all design-§2 tables + `file_offsets`; `agents`/`skills` before `events`
- [ ] Parser test per fixture asserts exact sessions/turns/events counts + subagent `parent_event_id`
- [ ] `curl :7777/api/projects`, `/api/sessions`, `/api/sessions/{id}` return JSON from a real ingested file
- [ ] `--port`/`SWARMERY_PORT` work; backend deps ≤ 3 non-stdlib (`go list -m all`)
- [ ] CI workflow present and green on the commit

## Navigation

Previous: [step-03-quality-gate-format-review.md](step-03-quality-gate-format-review.md) · Next: [step-05-quality-gate-contract-freeze.md](step-05-quality-gate-contract-freeze.md) · Index: [00-plan.md](00-plan.md)

### Completion Report

```
Date/agent: 2026-07-12 · Claude Code (Fable 5) executor session
Commit SHA: 0b5935b (code HEAD: d971252 store → 885ec19 ingest → 1b27c4b api/cli → 0b5935b web/ci)
Records ingested (real file): ~/.claude/projects/-Volumes-Work-swarmery/4fdcbda4-….jsonl (744 KB
  + 6 sidechain files) → 1 project, 1 session (status=idle), 58 turns, 256 events
  (228 tool_call, 10 error, 6 subagent_start, 5 subagent_stop, 5 user_prompt, 2 skill_use,
  0 unknown), 6 file_changes, 7 file_offsets rows; 0 malformed lines skipped.
  Endpoints verified: /api/projects, /api/sessions?status=, /api/sessions/{id} (+ SPA at /),
  --port flag and SWARMERY_PORT both exercised. go test: 7 tests green; deps: modernc.org/sqlite only.
Deviations from format doc: none — no fixture or real-file construct fell outside
  docs/jsonl-format.md (real file produced zero type='unknown' events). Two implementation
  notes, both anticipated by the design DDL comments, not doc changes:
  (1) sidechain dedup keys are scoped as <agentId>:<uuid> because sidechain uuid space
      restarts per file (C3; the fixture's colliding uuids prove raw uuid is insufficient);
  (2) system/compact_boundary is stored as a payload-only events row (type='unknown', raw
      JSON) since the schema defines no dedicated event type for it.
  Observed during validation: a still-running subagent kept appending to its sidechain file,
  so consecutive ingests of a "hot" session legitimately add rows — dedup held (idempotency
  proven on static fixtures); relevant for step 06's tail-follow design (Q11).
```
