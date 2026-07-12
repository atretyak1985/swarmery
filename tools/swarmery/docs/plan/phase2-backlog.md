# Phase 2 backlog — dogfooding notes (T4)

> Захоплюємо кожен gap під час реального користування дашбордом.
> Формат: `- [екран] чого не вистачало — чому це важливо`.
> Цей файл — обовʼязковий вхід для плану Фази 2 (Approvals + hooks), див. design doc §4.

## Entries

- [x] [session-detail] не видно зведення, які скіли використовувались і які агенти працювали в сесії — власник хоче бачити це одразу, без прокрутки таймлайну (2026-07-12, перший день dogfooding) → реалізовано як MVP+ chips у header деталі сесії; повноцінний зріз «агенти × задачі» — кандидат на Phase 3 (Agents registry)
- [ ] (наступні записи — під час dogfooding)
- [x] [session-detail] wave subagent blocks show 0.1s duration while agents ran ~20 min — subagent_stop duration/parenting for background (run_in_background) agents needs investigation → fixed in ingest: "async_launched" results are no longer errors and the duration is derived from the sidechain span (subagent_start.ts → last sidechain record, refined on every tail batch); migration 0004 heals existing rows (2026-07-12)
- [x] [session-detail] "unassigned events" bucket appears at the bottom of the timeline — events without turn attribution (likely events whose turn linkage is null); investigate attribution rule → root cause: sidechain events tailed before their subagent_start existed (live-tail race) kept parent_event_id NULL; fixed in ingest — orphans are adopted by dedup-key scope (agentId) as soon as the parent appears; migration 0003 heals existing rows; no frontend change needed (2026-07-12)
- [ops] launchd reinstall race: bootout is async, immediate bootstrap fails with exit 5 — installer should retry bootstrap with short backoff

## Known candidates carried from MVP

- [sessions] session-list aggregates (toolCalls / costUsd / lastAction) — deferred contract request #3 from step 10
- [config] 2026-09-01: switch claude-sonnet-5 pricing to $3/$15 and run `swarmery recost` (intro pricing ends)
- [ops] `<synthetic>` model turns (50 шт.) мають cost NULL — вирішити, чи мапити на нульову ціну явно
