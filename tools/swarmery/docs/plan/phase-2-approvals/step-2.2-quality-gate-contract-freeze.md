# Step 2.2 — QUALITY GATE: hooks contract review + freeze

## Header

| Field | Value |
|---|---|
| Phase | 2 — Approvals + hooks |
| Duration | 2–3 h (human review + one short agent session for the freeze commit) |
| Type | GATE (human, critical) + agent-assisted freeze commit |
| Risk | Gate — protects both parallel branches from building on wrong hook semantics |
| Dependencies | Step 2.1 complete (`docs/hooks-format.md` committed) |

## Goal

Human validates the spike findings and freezes every cross-branch contract:
`types.ts` additions, `docs/ws-protocol.md` additions, the new
`docs/hooks-protocol.md` (HTTP contract the hook shim and the daemon share), and
the migration `0006` text. After this gate, agents A and B work in parallel and
may not change any frozen shape (requests go to `web/CONTRACT-REQUESTS.md`).

## Gate checklist (human)

- [x] `docs/hooks-format.md` verdict: D1 holds (PermissionRequest fires only for would-prompt calls — E8 green) — or the documented fallback is chosen and this plan's steps 2.3/2.4 prompts are amended accordingly
- [x] D3 holds: E5/E6 show no-output/timeout → native dialog appears, session survives
- [x] E4 answer reviewed → set the default `approval_timeout` (Q-A) and record it in `docs/hooks-protocol.md` — 120 s poll owned by the shim, installed hook config `timeout: 130` (E6: per-hook timeout kills the shim)
- [x] Owner sign-off on D2 (settings.local.json placement) and D4 (localhost, no auth) — these are policy, not code
- [x] Q-B/Q-C decided or explicitly deferred (recorded in 00-phase-2-plan.md) — both remain deferred as recorded in the plan (Q-B: `permission_suggestions` persisted verbatim in `request_json`; Q-C: stretch item in 2.4)
- [ ] Freeze commit reviewed and merged to main (see below) — commit produced on `chore/swarmery-phase2-freeze` (not pushed); merge pending

## Agent Prompt (freeze commit)

```
Reference: docs/plan/phase-2-approvals/step-2.2-quality-gate-contract-freeze.md

Context:
Swarmery, гілка main. Спайк docs/hooks-format.md прийнято людиною.
Прочитай його розділ "Contract for phase 2", swarmery-design.md §2
(permission_requests), web/src/api/types.ts (FROZEN contract MVP),
docs/ws-protocol.md, internal/store/migrations/. Це docs+types-only
коміт — жодної Go/React-логіки.

Tasks:
1. web/src/api/types.ts — додай (нічого не змінюючи в наявному):
   export type PermissionRequestStatus =
     'pending'|'approved'|'denied'|'expired'|'resolved_elsewhere';
   export type ResolvedVia = 'dashboard'|'terminal'|'mobile';
   export interface PermissionRequest {
     id: number; sessionId: number; projectSlug: string;
     sessionTitle: string | null; toolName: string;
     requestJson: unknown;            // повний stdin хука (tool_input, permission_suggestions…)
     status: PermissionRequestStatus; requestedAt: string;
     expiresAt: string; resolvedAt: string | null;
     resolvedVia: ResolvedVia | null; reason: string | null;
   }
   /** GET /api/approvals?status=&limit= */
   export type ApprovalsResponse = PermissionRequest[];
   WSMessageType += 'permission_requested' | 'permission_resolved';
   WSMessage += { type:'permission_requested'; payload: PermissionRequest }
              | { type:'permission_resolved';  payload: PermissionRequest };
   HealthResponse += hooks_last_seen?: string | null;  // optional, additive
2. docs/ws-protocol.md — задокументуй два нові повідомлення (джерело
   емісії, приклади JSON, семантика: resolved емітиться і для expired).
3. docs/hooks-protocol.md (новий) — HTTP-контракт демона для хуків:
   POST /api/hooks/permission-request
     body: сирий stdin хука (pass-through JSON);
     200 {"decision":"allow"|"deny"|"none","reason":string|null,
          "requestId":number}   // "none" → шим виходить 0 без stdout
     long-poll до approval_timeout (дефолт — значення з гейту, E4/Q-A);
     429 → шим виходить 0 без stdout (rate limit);
   POST /api/hooks/stop — body: сирий stdin; завжди 202 (фаза 2 — лише
     heartbeat; канал для фази 2.5);
   GET /api/approvals?status=pending|resolved&limit=N → ApprovalsResponse;
   POST /api/approvals/{id} {"action":"approve"|"deny","reason"?:string}
     → 200 PermissionRequest | 409 якщо вже resolved;
   помилки — {"error":string}, як у наявному API. Плюс: семантика
   дедуплікації (SHA-256(session_uuid|tool_name|canonical tool_input);
   ідентичний pending → attach до наявного запиту), client-disconnect →
   resolved_elsewhere/via=terminal (згідно з E4), Origin-перевірка (D4),
   формат виводу шима (hookSpecificOutput за docs/hooks-format.md).
4. Текст міграції internal/store/migrations/0006_approvals.sql —
   ТІЛЬКИ додавання: ALTER TABLE permission_requests ADD COLUMN
   dedup_hash TEXT; ADD COLUMN expires_at TEXT; ADD COLUMN reason TEXT;
   CREATE INDEX idx_pr_dedup ON permission_requests(session_id,
   dedup_hash, status). Файл комітиться зараз (Agent A його підключить),
   існуючі 0001–0005 не чіпати.
5. npm run build (типи компілюються), bash -n не потрібен; go vet
   зелений (міграція — .sql, Go не змінюється).

Boundaries:
- Наявні імена/поля в types.ts, ws-protocol.md, міграціях 0001–0005 —
  недоторканні (MVP-контракт byte-identical).
- Жодної імплементації — тільки типи, docs, .sql.

Output / Validation:
Один conventional commit "docs(swarmery): phase 2 contract freeze —
approvals HTTP/WS/types + migration 0006" на main. Заповни Completion
Report у step-2.2-quality-gate-contract-freeze.md.
```

## Success Criteria

- [x] Freeze commit on main: types.ts additions compile (`npm run build`), MVP names untouched (`git diff` shows additions only) — `npx tsc --noEmit` green; on the freeze branch pending merge
- [x] `docs/hooks-protocol.md` fully determines both sides of the HTTP contract (a stranger could implement either end)
- [x] Migration 0006 text is additive-only and committed — renumbered to **0007** (`0007_approvals.sql`): 0006 is taken by the parallel workspaces branch
- [ ] All gate checkboxes above ticked by the human — a red box stops the phase (5/6; merge-to-main box pending)

## Navigation

Previous: [step-2.1-hooks-spike.md](step-2.1-hooks-spike.md) · Next: [step-2.3-agent-a-hooks-backend.md](step-2.3-agent-a-hooks-backend.md) + [step-2.4-agent-b-approvals-ui.md](step-2.4-agent-b-approvals-ui.md) (parallel) · Index: [00-phase-2-plan.md](00-phase-2-plan.md)

### Completion Report

```
Status: DONE. Gate verdict: PASS (owner). Contracts frozen on branch
chore/swarmery-phase2-freeze (worktree /Volumes/Work/swarmery-wt-p2freeze),
one commit, not pushed — merge to main is the remaining human step.

Frozen artifacts:
- web/src/api/types.ts (additive): PermissionRequestStatus,
  PermissionRequest DTO {id, sessionId, toolName, requestJson: string
  (verbatim hook stdin TEXT), status, requestedAt, resolvedAt,
  resolvedVia: string|null, reason, expiresAt}; WSMessageType/WSMessage
  gain permission_requested|permission_resolved (payload:
  PermissionRequest); HealthResponse gains optional hooks_last_seen.
  MVP names byte-identical (diff is additions only).
- docs/hooks-protocol.md (NEW): daemon↔shim contract.
  POST /api/hooks/permission-request — body = PR hook stdin verbatim;
  daemon MINTS the request id (no tool_use_id in stdin, E1; subagents
  share parent session_id, E11); long-poll 200 {decision:allow|deny,
  message?} → hookSpecificOutput, 204/timeout/error → exit 0 silent
  (D3 fail-open, E5/E6/E7). POST /api/hooks/stop — 202 heartbeat,
  phase-2.5 channel. Timing: connect 500 ms, poll ≤120 s, installed
  hook config timeout: 130 (E6: Claude's per-hook timeout kills the
  shim → native dialog; default 60 s would cut the poll). Dedup (D6):
  dedup_hash = hex(SHA-256(session_id + "\n" + tool_name + "\n" +
  canonical_json(tool_input))), compared over status='pending' rows
  only; identical concurrent requests attach to the one pending row,
  decision fans out to all waiters. Known boundary: headless claude -p
  never fires the PR hook (spike headline).
- docs/ws-protocol.md: permission_requested / permission_resolved
  appended with payload examples, marked phase-2; resolved emits for
  every terminal status incl. expired/resolved_elsewhere; MVP trio
  unchanged.
- internal/store/migrations/0007_approvals.sql: additive-only ALTERs
  (dedup_hash, expires_at, reason) + partial index idx_pr_dedup ON
  permission_requests(dedup_hash) WHERE status='pending'.
  RENUMBERED 0006 → 0007: migration 0006 is being taken by the
  parallel workspaces branch. Verified migrate.go needs no
  registration (embed FS, filename order, per-version tracking — the
  0006 gap is harmless; if 0006 merges later it still applies).

Deviations from the plan wording:
- D6 said "migration 0006" → shipped as 0007 (parallel-branch collision).
- D6/step-prompt index (session_id, dedup_hash, status) → partial index
  on (dedup_hash) WHERE status='pending' per gate instruction; dedup_hash
  already encodes session_id, so the composite was redundant.
- Step-prompt draft DTO fields projectSlug/sessionTitle, ResolvedVia
  union, ApprovalsResponse, and the /api/approvals REST pair were NOT
  frozen — gate instruction scoped the freeze to the DTO/WS/health
  types and the daemon↔shim endpoints; requestJson is string (raw
  TEXT), not unknown. /api/approvals shapes go through
  web/CONTRACT-REQUESTS.md at 2.3/2.4 if needed.
- Long-poll response is {decision: allow|deny, message?} with 204 for
  no-decision (draft had decision:"none" + requestId in-body).

Collateral (required for validation, not new UI): web/src/lib/ws.ts +
web/src/pages/SessionDetail.tsx narrowing updated for the widened
WSMessage union (behavior unchanged — permission_* frames are ignored
by MVP views until 2.4).

Validation: npx tsc --noEmit GREEN; go vet ./internal/store GREEN;
migration applied on a scratch DB via a throwaway go test (Open →
Migrate → re-Migrate idempotent, max version = 7, version 6 absent,
new columns queryable, partial index present, pending-row insert +
dedup lookup OK) — test deleted before commit; markdown fences
balanced.
```
