# Step 2.3 — Agent A: hooks backend (shim, endpoints, installer, statuses)

## Header

| Field | Value |
|---|---|
| Phase | 2 — Approvals + hooks (parallel wave) |
| Duration | 1 agent session, ~4–6 h ([LOW-CONFIDENCE] — long-poll concurrency + installer JSON surgery are new territory) |
| Type | Agent session (code, runs in parallel with step 2.4) |
| Risk | High — first write endpoints in the API; touching users' settings files |
| Dependencies | Gate 2.2 PASS; worktree `/Volumes/Work/swarmery-wt-hooks` (work in `tools/swarmery`), branch `feat/swarmery-hooks` |

## Goal

Everything server-side and CLI-side of approvals: the `swarmery hook` runtime shim,
long-poll daemon endpoints, `permission_requests` persistence with dedup/expiry,
`waiting_approval` status interplay, WS emission, heartbeat in `/api/health`, and
the idempotent `swarmery hooks install|uninstall|status` config manager.

## Automation

Fresh Claude Code session in `/Volumes/Work/swarmery-wt-hooks/tools/swarmery`
(worktree, branch `feat/swarmery-hooks`). Runs concurrently with Agent B (frontend).

## Agent Prompt

```
Reference: docs/plan/phase-2-approvals/step-2.3-agent-a-hooks-backend.md

Context:
Swarmery після MVP. Контракт заморожено — прочитай docs/hooks-protocol.md,
docs/hooks-format.md (фактична поведінка хуків, версія Claude Code),
docs/ws-protocol.md, web/src/api/types.ts (НЕ чіпати), swarmery-design.md
§2, internal/{api,ingest,store,installer}, cmd/swarmery/main.go.
Міграція 0006 вже закомічена. Працюєш у гілці feat/swarmery-hooks
(worktree). Паралельно Agent B робить екран Approvals по цьому ж
контракту — НЕ чіпай web/; свої роути додавай ТІЛЬКИ у новий блок
"// wave: approvals" в internal/api/routes.go.

Tasks:
1. Пакет internal/approvals: store-обгортка над permission_requests
   (create з dedup_hash = SHA-256(session_uuid|tool_name|canonical
   JSON tool_input), resolve, expire) + in-memory pending-реєстр:
   map[requestID]chan decision, attach для дублікатів (ідентичний
   pending → той самий канал, БЕЗ нового рядка/події), expiry-sweeper
   (тікер 5с: pending з expires_at у минулому → status=expired).
   Ліміт: >20 pending на сесію → миттєве "none" (429 на HTTP-рівні).
2. POST /api/hooks/permission-request (блокуючий, до approval_timeout
   з конфігу): резолв сесії за session_id (session_uuid); якщо сесія
   невідома — створи рядок sessions (project за cwd → projects.path,
   source='hook'); insert permission_requests (pending) + events row
   type=permission_request, dedup_key='hook:'+новий UUID, payload=повний
   stdin; сесію → status='waiting_approval'; Publish на bus:
   session_updated + event_appended + permission_requested. Чекай на
   каналі рішення / таймаут / r.Context().Done(). Disconnect клієнта
   до рішення → status='resolved_elsewhere', resolved_via='terminal'
   (семантика E4 з docs/hooks-format.md). Відповідь — за
   docs/hooks-protocol.md.
3. Резолюція (спільний шлях для approve/deny/expire): update рядка,
   events row type=permission_resolved (payload {requestId, decision,
   via}, status ok|denied|timeout), WS permission_resolved; якщо в
   сесії немає інших pending — статус за евристикою
   ingest.StatusFor(last_activity) + session_updated. Тікер статусів
   НЕ чіпай (він уже ігнорує waiting_approval — status.go).
4. POST /api/hooks/stop: 202 завжди, зберігання нічого (канал фази
   2.5); кожен виклик /api/hooks/* оновлює in-memory heartbeat
   (per-project last-seen) → /api/health віддає hooks_last_seen.
5. GET /api/approvals + POST /api/approvals/{id} — строго за
   docs/hooks-protocol.md (409 на повторний resolve; resolved_via=
   'dashboard'). DTO збирай з JOIN на sessions/projects (projectSlug,
   sessionTitle).
6. Безпека (D4): serve біндиться на 127.0.0.1:{port} (зараз ":{port}"
   — всі інтерфейси); новий флаг --bind для свідомого overriding.
   Усі POST-ендпоінти: якщо є заголовок Origin і він не
   http(s)://localhost:*|127.0.0.1:* → 403.
7. Шим: swarmery hook permission-request|stop — читає stdin, POST на
   http://127.0.0.1:$SWARMERY_PORT (дефолт 7777), connect-timeout
   500мс, загальний дедлайн = approval_timeout+10с. Будь-яка помилка/
   "none" → exit 0 БЕЗ stdout (fail-open D3, рідний діалог). "allow"/
   "deny" → hookSpecificOutput точно за фікстурами docs/hooks-format.md.
   Один рядок у ~/.swarmery/hook.log на виклик (ts, tool, outcome).
8. Менеджер: swarmery hooks install [--project <path>|--all] [--user]
   [--port <n>], uninstall (ті ж таргети), status. Пише PermissionRequest
   (matcher "*", timeout за контрактом) + Stop у .claude/settings.local.json
   проєкту; command = абсолютний шлях поточного бінаря. --all — усі
   незаархівовані projects з БД. --user = ~/.claude/settings.json ТІЛЬКИ
   з явним інтерактивним підтвердженням (правило MVP). Ідемпотентність:
   read-modify-write зі збереженням чужих полів/хуків; свої entry
   впізнавай за підрядком "swarmery hook"; перший запис → .bak поруч;
   битий JSON → abort без запису. uninstall прибирає ТІЛЬКИ swarmery-
   entries. hooks status: таблиця проєкт → installed/stale (шлях
   бінаря змінився) / not installed.
9. Тести (go test -race обовʼязково): long-poll approve/deny/timeout/
   client-disconnect (httptest); дедуп — 2 конкурентні ідентичні
   запити → 1 рядок, обидва отримали рішення; expiry-sweeper; статуси
   waiting_approval → назад; Origin 403; шим fail-open (сервер лежить
   → exit 0, порожній stdout, <1.5с); installer: ідемпотентність
   (другий запуск = no diff), збереження чужих хуків byte-for-byte,
   битий JSON → no write, uninstall surgical. Fake-runner патерн — як
   в internal/installer.

Boundaries:
- НЕ чіпай web/**, types.ts, наявні міграції; тільки 0006 (вже є).
- MVP WS-повідомлення і REST-шейпи byte-identical; нові роути тільки
  в блоці "// wave: approvals" у routes.go.
- Нові Go-залежності: 0 (stdlib + наявні).
- Потреби у зміні контракту → web/CONTRACT-REQUESTS.md, не самодіяльність.

Output / Validation:
go vet + go test -race ./... зелені. Смок: serve + hooks install у
тестовий проєкт + curl-довгий-poll → approve через curl POST
/api/approvals/{id} → шим отримав allow. Conventional commits у
feat/swarmery-hooks. Заповни Completion Report у
docs/plan/phase-2-approvals/step-2.3-agent-a-hooks-backend.md (у worktree).
```

## Detailed Instructions

- Long-poll wakeup: resolve writes the decision to the channel **inside** the same
  transaction scope that flips the row — a crash between DB update and channel send
  must leave the row authoritative; waiters also re-check the row on timeout.
- `waiting_approval` never comes from the ticker: `RecomputeStatuses` selects
  `status IN ('active','idle')` (internal/ingest/status.go:55) — the approvals layer
  owns entry/exit of this status exclusively.
- Session upsert race: ingest may later re-upsert a session created with
  `source='hook'` — make ingest's upsert promote `hook`→`both` (one-line change in
  internal/ingest, covered by a test) rather than overwriting to `jsonl`.
- The API layer gains its first writes: pass the bus into the approvals handler the
  same way `AttachBus` does for WS — do not thread it through `Handler` (keeps the
  parallel-branch diff conflict-free).
- Installer settings surgery: unmarshal into `map[string]any`, mutate only
  `hooks.PermissionRequest`/`hooks.Stop` arrays, `json.MarshalIndent` with 2 spaces;
  test round-trips a fixture containing unknown keys + existing foreign hooks.

## Success Criteria

- [ ] `go vet` + `go test -race ./...` green incl. the new tests listed in task 9
- [ ] Smoke: curl long-poll resolved by dashboard-style POST within the same second; shim prints valid hookSpecificOutput JSON
- [ ] Daemon down → shim exits 0, empty stdout, < 1.5 s (measured in a test)
- [ ] `hooks install` twice → second run reports "already installed", file unchanged; foreign hooks byte-identical
- [ ] `serve` binds 127.0.0.1 by default; POST with foreign Origin → 403
- [ ] Diff touches only `internal/approvals` (new), `internal/api` (routes block + new handlers), `internal/ingest` (source promote), `internal/installer` or new `internal/hookcfg`, `cmd/swarmery`, `docs/`; `web/**` untouched

## Navigation

Previous: [step-2.2-quality-gate-contract-freeze.md](step-2.2-quality-gate-contract-freeze.md) · Next: [step-2.4-agent-b-approvals-ui.md](step-2.4-agent-b-approvals-ui.md) (parallel) · Index: [00-phase-2-plan.md](00-phase-2-plan.md)

### Completion Report

Status: **DONE** — all success criteria met; branch `feat/swarmery-hooks`,
committed, not pushed.

**New packages / files**

- `internal/approvals/` — store layer + dedup (frozen D6 `dedup_hash`) +
  in-memory long-poll waiter registry (fan-out to all attached waiters) +
  expiry sweeper (5 s ticker; overdue → `expired`, waiters get 204 upstream;
  self-heals sessions stuck in `waiting_approval`) + heartbeat.
- `internal/api/approvals.go` — HTTP endpoints + `requireLocalOrigin` (D4).
- `internal/hookshim/` — `swarmery hook <permission-request|stop>` shim.
- `internal/hookcfg/` — `swarmery hooks install|uninstall|status`.
- Touched: `internal/api/{routes,ws,health}.go`, `internal/ingest/{bus,ingest}.go`,
  `cmd/swarmery/main.go` (`--bind` 127.0.0.1 default, `--approval-timeout`,
  `hook`/`hooks` subcommands).

**Endpoints** (all writes behind the Origin check; no-Origin/curl/shim pass):
- `POST /api/hooks/permission-request` — verbatim stdin in; long-poll ≤120 s;
  200 `{decision, message?}` / 204 (expiry/elsewhere) / 429 (>20 pending) /
  400 (malformed). Mints request identity (E1/E11); dedup attaches, one
  decision fans out; emits `session_updated`+`event_appended`+`permission_requested`.
- `POST /api/hooks/stop` — always 202, heartbeat only.
- `POST /api/approvals/{id}` `{action:approve|deny, reason?}` — 200 DTO / 404 /
  409 (repeat resolve); `resolved_via='dashboard'`; restores session status.
- `GET /api/approvals?status=&limit=` — newest first; pending default;
  `resolved`/`all` meta-filters.
- `GET /api/health` — additive `hooks_last_seen`.

**Shim**: 500 ms connect timeout, ≤120 s poll; 200 → exact `hookSpecificOutput`
JSON (E2/E3); anything else → exit 0 with NO stdout (fail-open D3). Port from
`SWARMERY_PORT`. Audit line per call in `~/.swarmery/hook.log`.

**Installer**: writes PermissionRequest (matcher `*`, timeout 130) + Stop into
`.claude/settings.local.json` invoking `~/.swarmery/bin/swarmery hook …`;
create-if-absent, parse-fail aborts w/o write, `.bak` before first write,
idempotent, uninstall removes ONLY `swarmery hook` entries (foreign settings
byte-for-byte), `--all` iterates the daemon DB projects, `status` reports
installed/stale/not-installed. No `--user` (deferred).

**Tests** (`go test -race ./...` green): dedup fan-out (store + HTTP), long-poll
approve/deny/timeout-204/client-disconnect, expiry sweeper + stuck-session
heal, Origin allow/deny, shim allow/deny/204/non-contract/daemon-down-<1.5 s/
stop, installer idempotency + preserve-foreign + uninstall-only-ours + broken-
JSON-abort + stale-detect + port-bake, WS golden-key for both `permission_*`
shapes, ingest source `hook`→`both` + `waiting_approval` non-overwrite.

**Live validation** (scratch daemon `:7799`, scratch DB, throwaway
`/tmp/p2-live/proj`, real Claude Code 2.1.170 over PTY; real `:7777` daemon
untouched):
1. Approve — session `ee1f16cd`, request id=4, dashboard approve → TUI
   `HTTP/2 200` (curl proceeded).
2. Deny — session `c9d00d8c`, request id=5, deny+reason → TUI `Denied`, reason
   stored (`SWARMERY-DENY-MARKER blocked by reviewer`).
3. Daemon down — native `Do you want to proceed?` dialog appeared (fail-open);
   shim connect fail-open measured **0.031 s** (≤1 s budget).
Installer idempotency + Origin 403 + loopback-only bind verified live.
`/tmp/p2-live` and the throwaway hooks + test `hook.log` cleaned up.

**Contract requests**: none — the frozen contract matched exactly.
**Deviations**: none material. Heartbeat kept in-memory (a stale value
surviving a restart would misreport hooks as alive). `--user` deferred per D2.
