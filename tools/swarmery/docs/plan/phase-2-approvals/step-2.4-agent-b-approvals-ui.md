# Step 2.4 — Agent B: Approvals screen (frontend, frozen contract)

## Header

| Field | Value |
|---|---|
| Phase | 2 — Approvals + hooks (parallel wave) |
| Duration | 1 agent session, ~3–4 h (MEDIUM — analogous to MVP step 07, smaller surface) |
| Type | Agent session (code, runs in parallel with step 2.3) |
| Risk | Medium — pure consumer of the frozen contract; works against mocks until integration |
| Dependencies | Gate 2.2 PASS; worktree `/Volumes/Work/swarmery-wt-approvals-ui` (work in `tools/swarmery`), branch `feat/swarmery-approvals-ui` |

## Goal

Approvals screen per design §3.2: pending list (tool, collapsed `request_json`
summary, session link, live age), Approve/Deny actions, decision history (audit:
`resolved_via`/when), nav badge with the pending count, `waiting_approval` made
visible everywhere sessions render, Overview pending widget (design §3.1).

## Automation

Fresh Claude Code session in `/Volumes/Work/swarmery-wt-approvals-ui/tools/swarmery`
(worktree, branch `feat/swarmery-approvals-ui`). Backend endpoints do not exist yet —
develop against extended mocks (`web/src/mock/`), same pattern as MVP step 07.

## Agent Prompt

```
Reference: docs/plan/phase-2-approvals/step-2.4-agent-b-approvals-ui.md

Context:
Swarmery SPA після MVP (React 18 + TS strict + Vite + Tailwind, дизайн-
мова — темна editorial, див. web/src/pages/* і swarmery-ui-mockup.html).
Контракт заморожено: web/src/api/types.ts вже містить PermissionRequest,
ApprovalsResponse, WS-типи permission_requested|permission_resolved;
HTTP — docs/hooks-protocol.md. Прочитай їх, web/src/lib/ws.ts,
web/src/mock/{data,ws}.ts, App.tsx (nav), swarmery-design.md §3.1–3.2.
Працюєш у гілці feat/swarmery-approvals-ui (worktree). Паралельно
Agent A робить бекенд — НЕ чіпай internal/**, cmd/**, migrations,
types.ts (запити на зміну контракту → web/CONTRACT-REQUESTS.md).

Tasks:
1. web/src/pages/Approvals.tsx (роут /approvals): секція Pending —
   картка: іконка/назва tool, розумний summary з requestJson
   (Bash → command; Edit|Write → file_path; MCP → імʼя інструмента;
   решта — перший рядок JSON), розгортання повного JSON (<pre>,
   як payload у таймлайні), лінк на сесію (/sessions/{sessionId},
   projectSlug + sessionTitle), живий вік ("висить 1м 23с", тікер 1с),
   бейдж часу до expiresAt. Кнопки Approve / Deny (Deny — з опційним
   reason у popover) → POST /api/approvals/{id}, optimistic-переніс у
   History, 409 → тихий refetch. Секція History (status=resolved,
   limit 50): рішення, resolved_via як chip (dashboard|terminal|mobile),
   відносний час.
2. Nav: пункт Approvals у App.tsx (bottom bar / sidebar) з amber-бейджем
   pending-каунту (як "Approvals (3)" у design §3.2); каунт живе через
   WS: permission_requested → +1, permission_resolved → −1, ресинк
   через GET /api/approvals?status=pending на mount/reconnect (WS —
   hint stream, не джерело істини, див. docs/ws-protocol.md).
3. WS-інтеграція: розшир lib/ws.ts обробкою двох нових типів (naming
   строго з types.ts); Approvals-екран оновлюється без reload; порожній
   стан — "No pending approvals" у стилі наявних empty-states.
4. waiting_approval видимість: SessionCard/ui.tsx вже мають amber-стилі
   для waiting_approval — перевір, що бейдж/фільтр статусу на Sessions
   його показують; Overview: віджет "Pending approvals: топ-3 + кнопка
   в чергу" (design §3.1) — компактний список над сесіями; поле
   waiting_approval у StatsOverview вже в контракті.
5. Моки: mock/data.ts — фікстури PermissionRequest (pending різного
   віку + resolved усіх статусів, включно expired/resolved_elsewhere);
   mock/ws.ts — сценарій: permission_requested через 3с, resolved через
   10с (для ручної перевірки бейджа).
6. Мобільний: список/картки адаптивні як решта екранів; swipe-дії
   (approve вправо / deny вліво) — SLC-бонус, тільки якщо без нових
   залежностей (Q-C: стретч, не блокер).
7. Скріншоти: web/screenshots/approvals.png + approvals-desktop.png
   через наявний scripts/screenshot.mjs (мок-режим).

Boundaries:
- НЕ чіпай internal/**, cmd/**, migrations/**, web/src/api/types.ts.
- Нові npm-залежності: 0.
- TS strict, без any; DRY з наявними компонентами (ui.tsx, format.ts).

Output / Validation:
npm run build + наявні перевірки зелені; скріншоти додано. Conventional
commits у feat/swarmery-approvals-ui. Заповни Completion Report у
docs/plan/phase-2-approvals/step-2.4-agent-b-approvals-ui.md (у worktree).
```

## Detailed Instructions

- Age/expiry tickers: one shared 1 s interval for the page, not per-card timers.
- Optimistic resolve must reconcile with the авторитетним WS `permission_resolved`
  (idempotent upsert by `id` — the same message will arrive for your own action).
- The badge count lives in the app shell (App.tsx) — lift the WS subscription or
  reuse the existing shared WS connection from `lib/ws.ts`; do not open a second
  socket.
- `requestJson` is `unknown` — narrow via type guards (no `as any`); malformed
  payload renders as raw JSON, never crashes the card.

## Success Criteria

- [ ] `npm run build` green (TS strict); zero new dependencies
- [ ] Mock scenario: badge appears within 3 s, decrements on resolve, survives reload (REST resync)
- [ ] Approve/Deny flows work against mocks incl. 409 path; Deny records a reason
- [ ] Pending card: tool summary, expandable JSON, session link, live age, expiry countdown
- [ ] History shows `resolved_via` chips for all five statuses; Overview shows the top-3 pending widget
- [ ] Screenshots committed; diff touches only `web/**` (except `web/src/api/types.ts`) and `docs/`

## Navigation

Previous: [step-2.3-agent-a-hooks-backend.md](step-2.3-agent-a-hooks-backend.md) (parallel) · Next: [step-2.5-quality-gate-parallel-wave.md](step-2.5-quality-gate-parallel-wave.md) · Index: [00-phase-2-plan.md](00-phase-2-plan.md)

### Completion Report

```
Status: DONE (2026-07-13, branch feat/swarmery-approvals-ui — worktree, not pushed)

Built (web/** + docs only; types.ts untouched, zero new npm deps):
- pages/Approvals.tsx (route /approvals): PENDING cards — tool name (mono),
  collapsed tool_input essential (Bash→command, Edit/Write→file_path, generic
  key fallback → compact JSON → raw string; type-guarded via lib/payload
  pickString, malformed stdin never crashes a card), expandable full hook-stdin
  JSON (<pre>), session link (project · title via lazy /api/sessions join,
  "session #N" fallback), live "hangs Ns" age + "expires in Ns" countdown
  (ONE shared 1 s ticker for the page), Approve (sage) / Deny (danger, inline
  optional-reason input — no modal) / Open session. HISTORY below: status chips
  (approved sage / denied danger / expired + elsewhere dim), via-chip
  (resolvedVia), relative time, reason line; limit 50, newest first.
  Optimistic resolve reconciles with the authoritative WS permission_resolved
  (idempotent upsert by id); any POST failure (409 race) → silent refetch.
- lib/approvals.ts: requestJson parsing/summary/pretty helpers + fmtClock.
- lib/ws.ts: ONE shared /api/ws connection app-wide (shell badge + page
  subscribe to the same socket — no second socket), same reconnect/backoff +
  60 s reconcile; new applyPermissionMessage upsert reducer next to
  applySessionMessage. Unknown WS types still ignored by MVP views.
- App.tsx: Approvals nav item (between Overview and Sessions) with live amber
  pending-count badge — REST resync on mount/reconnect (source of truth),
  WS permission_* as the hint stream; count kept as a Set<id> so duplicate
  resolves stay idempotent. Mobile: amber alert dot on the icon.
- Overview.tsx: PENDING APPROVALS rail card (top-3 oldest-first + "all
  approvals →" link, only when count > 0); ACTIVE tile "N waiting approval"
  subline now live from pending approvals (amber when > 0), stats fallback.
- waiting_approval visibility: verified already handled (ui.tsx StatusChip/
  LiveDot amber, SessionCard amber border, Sessions "waiting" filter chip,
  Overview HeroCard) — no changes needed.
- Mocks: mock/approvals.ts mutable store (2 pending of different ages, one of
  each terminal status in history incl. expired + resolved_elsewhere);
  approve/deny transitions locally + emits permission_resolved; WS scenario:
  permission_requested injected ~3 s after load, live expiry sweep resolves
  overdue pendings as expired. mockApi.approvals/resolveApproval in data.ts.
- api.ts: fetchApprovals(status?) + resolveApproval(id, action, reason?).

Contract requests appended (web/CONTRACT-REQUESTS.md):
1) GET /api/approvals `status=resolved` meta-filter + no-param + limit/order
   semantics (UI codes against pending+resolved, slices 50 client-side);
2) POST /api/approvals/{id} 409 contract confirmation (UI: silent refetch);
3) nice-to-have: denormalized projectSlug/projectName/sessionTitle on the DTO.

Validation:
- npx tsc --noEmit + npm run build: green (TS strict, zero new deps).
- Playwright smoke (mock, port 5199): badge 3 after 3 s injection → 2 after
  approve → 1 after deny-with-reason; expanded JSON shows tool_input; history
  gains the rows; reload resyncs from the (fresh) mock store.
- Screenshots: approvals.png (390×844) + approvals-desktop.png (1440) ADDED to
  scripts/screenshot.mjs (waits out the 3 s injection); overview{,-desktop}.png
  regenerated (rail card + amber subline); all other shots restored (timestamp
  noise only). No-horizontal-scroll assertions pass on every page incl.
  /approvals at 390 px.

Skipped per plan: swipe actions (Q-C stretch, deferred).
Known mock limitation: the mock store lives in-page, so a reload resets it —
"survives reload" is a REST-resync property that only fully shows against the
real daemon (step 2.5 integration).
```
