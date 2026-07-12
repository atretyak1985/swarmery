# Phase 2.5 — Agent D: Reporter & Reports

## Header

| Field | Value |
|---|---|
| Phase | 2.5 — Reporter + Reports (design doc §3.8, §4 п.2.5) |
| Duration | 1–2 agent sessions (MEDIUM — HTML-звіт і pipeline незалежні, checklist залежить від tasks) |
| Type | Agent session (code) |
| Risk | Medium — headless `claude -p` виклики, guard від самотригерингу, редакція секретів |
| Dependencies | Фаза 2 (Approvals + hooks) — Stop-hook приходить тим самим каналом; повна активація checklist — з чергою задач у фазі 5 |
| Branch | `feat/swarmery-reporter` |

## Goal

Додати шар звітності: Reporter-агент генерує наративи по завершених сесіях,
демон збирає з них самодостатні HTML-звіти (наратив + телеметрія з БД),
live view заморожується у снепшот, checklist задачі заповнюється сам,
weekly digest і incident-звіти — автоматом.

## Prerequisites / ordering

- **Після фази 2 (Approvals + hooks)**: авто-тригер — це Stop-hook; без хуків
  працює лише ручна генерація (POST) і статус done з черги задач.
- **Checklist активується повністю у фазі 5** (Tasks queue): парсинг
  Validation-секції відбувається при створенні задачі. До фази 5 звіти
  працюють для сесій без task_id — секція checklist просто відсутня.

## Agent Prompt

```
Reference: docs/plan/phase-2.5-reporter-agent-d.md

Context:
Репозиторій Swarmery (tools/swarmery): Go-демон + React SPA + SQLite, MVP
відвантажено, фаза 2 (Approvals + hooks) завершена. Прочитай
swarmery-design.md (§2 DDL — секція "фаза 2.5: Reporter", §3.8 Reports),
internal/ingest, internal/store, internal/api. Працюєш у гілці
feat/swarmery-reporter. Дедуплікація подій — record uuid / SHA-256
(див. events.dedup_key), субагентський tool називається `Agent`.

Objective:
1. Міграція (адитивна): narratives, reports, task_checklist_items —
   ДОСЛІВНО за swarmery-design.md §2, без змін наявних таблиць.
2. Reporter pipeline: по Stop-hook (або task status=done) для сесій
   з >30 подій (конфіг) — headless `claude -p --model <з конфігу>
   --output-format json` читає транскрипт і повертає СТРОГО JSON
   {done[], decisions[{what,why}], failures[], risks[], followups[]}
   → narratives (trigger=auto_stop). Ліміт токенів на вхід; retry 1 раз
   при битому JSON; не більше N авто-звітів/годину; guard від
   самотригерингу по cwd/маркеру (сесії Reporter-а інджестяться, але
   report-of-report не породжують); редакція секретів перед відправкою
   транскрипта в API — той самий фільтр, що на ingest. Вартість/токени
   Reporter-а пиши в narratives — вони видні в Analytics.
3. Звіт session/task: самодостатня HTML-сторінка (стилі inline, дані
   inline JSON, нуль зовнішніх запитів): наратив зверху → checklist
   (якщо є task) → diffs по файлах → тести (спроби → PASS) → субагенти →
   вартість/токени → out-of-scope. УСІ числа — з БД; числа з LLM
   ігноруються. Зберігання в reports, version++ при регенерації.
4. Live view GET /live/{session_id}: та сама сторінка + JS-підписка на
   /api/ws — checklist заповнюється, diffs доїжджають live; по
   session_end сторінка заморожується у снепшот (frozen=1).
5. Checklist: при створенні задачі парсити Validation-секцію промпта →
   task_checklist_items; евристики (Bash event з PASS + збіг ключових
   слів → passed, checked_by=heuristic, event_id=доказ); решту відмічає
   Reporter (checked_by=reporter); людина може перемкнути (checked_by=human).
   Кожна галочка клікабельна в таймлайн через event_id. До фази 5
   (Tasks queue) — код готовий, але сесії без task_id рендеряться без
   checklist-секції.
6. Weekly digest: `swarmery digest [--week 2026-W28]` — другий виклик
   Reporter по наративах тижня (trigger=digest), групування по проєктах
   (що зашипили, вартість, провали, топ follow-ups), kind=weekly_digest
   + endpoint і кнопка в UI. Incident-звіт для failed-задач: хронологія
   спроб (цикли edit→test→fail), місце зациклення, спалені токени до
   фейлу; генерується автоматично при status=failed.
7. API: GET /api/reports?kind=&ref=, GET /api/reports/{id},
   POST /api/sessions/{id}/report (ручна генерація для сесій ≤30 подій —
   trigger=manual), експорт .html з Content-Disposition: attachment.
   Пункт Reports у навігації SPA + кнопка "Generate report" на деталі
   сесії.

Boundaries:
- НЕ змінюй наявні таблиці; тільки нові з §2 (адитивна міграція).
- Reporter повертає ТІЛЬКИ текст — жодне число з LLM не потрапляє у звіт.
- Жодних зовнішніх запитів з HTML-звіту (self-contained artifact).
- Нові Go-залежності — 0 (stdlib + наявні); claude CLI викликається
  як subprocess, шлях/модель з конфігу.
- Секрети: транскрипт проходить redaction ДО API-виклику; тест на це
  обовʼязковий.

Output / Validation:
go vet + go test зелені; npm run build зелений. Тести: guard
самотригерингу (сесія Reporter-а не породжує звіт), retry на битому
JSON, rate limit, redaction, version++ при регенерації, frozen=1 після
session_end, парсер Validation-секції → items. Живий тест: заверши
коротку сесію claude → наратив у narratives і звіт відкривається з
/live/{session_id}; збережений .html відкривається з диска офлайн.
Conventional commits у feat/swarmery-reporter. Заповни Completion
Report у docs/plan/phase-2.5-reporter-agent-d.md.
```

## Success Criteria

- [ ] Міграція створює narratives / reports / task_checklist_items точно за §2; повторний запуск ідемпотентний
- [ ] Stop-hook на сесії з >30 подій → рядок у narratives (trigger=auto_stop) без ручних дій; ≤30 подій → тільки POST
- [ ] Сесія самого Reporter-а видна у Swarmery, але звіт по ній не генерується
- [ ] Експортований .html відкривається офлайн (0 зовнішніх запитів у DevTools)
- [ ] /live/{session_id} оновлюється по WS; після session_end у reports зʼявляється frozen=1
- [ ] `swarmery digest --week <W>` створює kind=weekly_digest
- [ ] failed-задача → incident-звіт автоматично
- [ ] Жодного секрету в тілі API-виклику Reporter-а (тест з фікстурою)

## Navigation

Previous: phase 2 — Approvals (план буде окремо, backlog: [phase2-backlog.md](phase2-backlog.md)) · Index: [00-plan.md](00-plan.md)

### Completion Report

```
(заповнюється виконавцем після завершення)
```
