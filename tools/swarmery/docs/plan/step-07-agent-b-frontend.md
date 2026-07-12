# Step 07 — Agent B: frontend (Overview + Sessions + Detail)

## Header

| Field | Value |
|---|---|
| Phase | 3 — Parallel wave |
| Duration | 1 agent session, ~3–6 h (LOW-CONFIDENCE — UI polish is open-ended) |
| Type | Agent session (code, runs in parallel with steps 06, 08) |
| Risk | Medium — UX of the timeline is the product's signature |
| Dependencies | Step 05 gate PASS; worktree `/Volumes/Work/swarmery-wt-frontend` (work in `tools/swarmery`) |

## Goal

Ship the three MVP screens in the mockup's design language: Overview (live sessions +
today's counters), Sessions list (filters, live status), Session detail (Timeline with
collapsible nested subagent track, Diffs). Mock-mode so backend readiness never blocks.

## Automation

Fresh Claude Code session in `/Volumes/Work/swarmery-wt-frontend/tools/swarmery` (worktree, branch
`feat/swarmery-frontend`). Playwright available for screenshots.

## Agent Prompt

```
Reference: docs/plan/step-07-agent-b-frontend.md

Context:
Репозиторій Swarmery після T1. Працюєш у гілці feat/swarmery-frontend, ТІЛЬКИ в web/.
Контракти (заморожено): web/src/api/types.ts — включно з WSMessage
(session_started | session_updated {session} | event_appended {event})
і StatsToday (для сьогоднішніх лічильників; бекенд-агент реалізує
/api/stats/today паралельно). docs/ws-protocol.md зʼявиться від
ingest-агента; поки нема — працюй по WSMessage.
Дизайн екранів: swarmery-design.md §3.1 (Overview) і §3.3 (Sessions).
МАСШТАБ MVP: у Session detail ТІЛЬКИ таби Timeline і Diffs (Context і
Tree — пізніші фази); в Overview БЕЗ блоку approvals (Фаза 2).

РЕФЕРЕНС ДИЗАЙНУ — docs/design/swarmery-ui-mockup.html: відтвори цю мову
дизайну в React. Ключові токени звідти (перенеси в Tailwind-конфіг):
  bg #10151D, surface #171E28, surface2 #1E2735, line #28374B,
  ink #DCE6F2, ink-dim #7C8DA3, amber #F5B84A (акцент/waiting),
  green #4ADE9C (active/ok), red #F0716F (error), blue #6FB4F0 (субагент).
Шрифти: Inter (текст), JetBrains Mono (числа/статуси/команди),
Space Grotesk (логотип/заголовки). Кольори ТІЛЬКИ семантичні: зелений =
живе, бурштин = чекає людину, червоний = зламалось. Сигнатурний елемент —
таймлайн із вкладеним треком субагента (синя смуга зліва + фон,
згортається тапом). Навігація: bottom bar на мобільному, sidebar ≥900px.

Бекенд може бути не готовий — mock-режим: MSW або json-server на
testdata/fixtures, перемикач env VITE_MOCK=1.

Tasks:
1. Каркас: React Router (Overview, Sessions, SessionDetail), Tailwind,
   тема з мокапа, mobile-first (я часто з телефона).
2. Overview (§3.1): активні сесії live (WS з reconnect+backoff),
   сьогоднішні лічильники з /api/stats/today (типи StatsToday; cost_usd
   може бути null — показуй "—"), останні завершені сесії.
3. Sessions list: фільтри проєкт (/api/projects) і статус; оновлення по WS.
4. Session detail:
   - Timeline: turns хронологічно; в межах turn — події; tool call =
     компактний рядок (іконка, назва, тривалість, статус), розгортається
     в payload; субагенти (subagent_start..stop) — вкладений блок, що
     згортається; помилки червоним, текст фейлу видимий одразу.
   - Diffs: file_changes згруповані по файлах, unified diff з підсвіткою
     (react-diff-view або аналог), лічильники +/-.
5. Стани loading/empty/error всюди.

Boundaries:
- ТІЛЬКИ web/. Жодних змін у Go-коді і в types.ts (потрібне нове поле →
  запиши в web/CONTRACT-REQUESTS.md, вирішиться на інтеграції).
- Без важких UI-бібліотек (без MUI/AntD) — Tailwind + headless.
- TypeScript strict mode; npm run build без помилок і TS-warnings.

Output / Validation:
npm run build чистий. У mock-режимі: playwright-скріншоти Overview,
Sessions, SessionDetail з fixture із субагентом — вкладеність має бути
видна; поклади їх у web/screenshots/. Conventional commits у feat/swarmery-frontend.
Заповни Completion Report у docs/plan/step-07-agent-b-frontend.md (worktree).
```

## Detailed Instructions

- The mockup (`docs/design/swarmery-ui-mockup.html`) also shows Approvals and Agents
  screens — **reference only**; do not build them or their nav badges. Keep nav slots
  for future screens if trivial, otherwise omit.
- WS reconnect: exponential backoff 1s→30s cap; on reconnect, refetch list state
  (events may have been missed).
- Mobile check: viewport 390×844 — bottom nav, no horizontal scroll except diff/cmd
  blocks (mockup pattern).

## Success Criteria

- [ ] `npm run build` green, 0 TS errors/warnings, strict mode on
- [ ] 3 screenshots committed showing Overview, Sessions, SessionDetail (subagent nesting visible and collapsible)
- [ ] Timeline renders errors in red with failure text visible without expanding
- [ ] `VITE_MOCK=1` fully works offline; without it, app targets `/api` + `/api/ws`
- [ ] Diff touches only `web/` (plus `web/CONTRACT-REQUESTS.md` if needed)

## Navigation

Previous: [step-06-agent-a-ingest.md](step-06-agent-a-ingest.md) (parallel) · Next: [step-08-agent-c-metrics.md](step-08-agent-c-metrics.md) (parallel) · Index: [00-plan.md](00-plan.md)

### Completion Report

```
Date/agent: 2026-07-12 · Agent B (frontend), Claude Code session on feat/swarmery-frontend
Branch head SHA: de1566c (code: 9234878, screenshots: ed627b2)
Status: DONE — npm run build clean (tsc --noEmit strict + vite build, 0 errors/warnings)
Screenshots (web/screenshots/, mock mode, 390×844 + one 1280×800):
  overview.png · sessions.png · session-detail-timeline.png (subagent nesting
  visible, verified collapsible) · session-detail-diffs.png · overview-desktop.png
Verified via playwright-core + system Chrome: no horizontal scroll at 390px on
  all three screens; error text visible without expanding; payload rows expand.
CONTRACT-REQUESTS entries (web/CONTRACT-REQUESTS.md):
  1. event_appended has no session attribution (payload lacks sessionId) —
     workaround: list views ignore event_appended; detail attributes via turnId.
  2. session list aggregates for cards (toolCalls / costUsd / lastAction) —
     nice-to-have for mockup meta line, not blocking.
New deps: react-router-dom (runtime); playwright-core (dev, screenshots only).
Notes: mock mode is an in-app fixture layer (src/mock/) toggled by VITE_MOCK=1 —
  no MSW dependency; diff highlighting is a custom ~40-line renderer, no diff lib.
```
