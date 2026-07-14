# Contract change requests

`web/src/api/types.ts` is **frozen** (gate 05, tag `swarmery-contract-freeze-v1`).
Branch agents (A/ingest, B/frontend, C/metrics) must **not** edit it.

If your branch needs a contract change (new field, new endpoint shape, new WS
event), **append a request below** and keep working around it locally. Requests
are resolved at integration (step 10), where `types.ts` is updated once on the
integration branch.

Request format:

```
## <date> — <branch> — <short title>
- What: <field/type/endpoint to add or change>
- Why: <one or two lines>
- Proposed shape: <TypeScript snippet>
```

---

<!-- Append requests below this line. -->

## 2026-07-12 — feat/swarmery-metrics (wave C) — per-turn model for exact recost

- What: add `model: string | null` to `Turn` (backed by a new `turns.model` column).
- Why: cost is priced per turn, but the model id lives per API message (JSONL §6),
  not per session. Ingest prices with the exact per-message model; `swarmery recost`
  can only fall back to `sessions.model`, which drifts when a session switches
  models mid-file (rare, but real — e.g. fallback models). Persisting the model on
  the turn makes recost exact and lets the UI attribute cost per model.
- Proposed shape:
  ```ts
  export interface Turn {
    // …existing fields…
    model: string | null; // API message model; NULL for user turns
  }
  ```

## 2026-07-12 — feat/swarmery-frontend — event_appended has no session attribution

- What: `WSMessage` `event_appended` payload is a bare `Event`, which carries
  `turnId` but no session id. List views (Overview, Sessions) cannot attribute
  a live event to a session card (e.g. the "now: <current command>" line from
  the mockup), and the detail view can only attribute events whose `turnId`
  already exists in the loaded detail.
- Why: live "current action" per session is a core mockup element; today the
  frontend works around it by ignoring `event_appended` in list views and
  matching via `turnId` in the detail view.
- Proposed shape:

  ```ts
  export type WSMessage =
    | { type: "session_started"; payload: Session }
    | { type: "session_updated"; payload: Session }
    | { type: "event_appended"; payload: { sessionId: number; event: Event } };
  ```

## 2026-07-12 — feat/swarmery-frontend — session list aggregates for cards

- What: optional aggregate fields on `sessionDTO` for list rendering:
  tool-call count, session cost, and a short "current/last action" summary.
- Why: mockup session cards show `12 tool calls · 2 subagents · $0.84` and
  `now: go test ./internal/mail/...`; none of that is derivable from
  `GET /api/sessions` today (cards currently show model · branch · times).
  Nice-to-have, not blocking.
- Proposed shape:

  ```ts
  export interface Session {
    // …existing fields…
    toolCalls?: number; // COUNT(events WHERE type='tool_call')
    costUsd?: number | null; // SUM(turns.cost_usd)
    lastAction?: string | null; // toolName + arg summary of latest event
  }
  ```

---

## Step-10 resolutions (2026-07-12, integration)

- **per-turn model (wave C)** — ACCEPTED & IMPLEMENTED. Migration
  `0002_turn_model.sql` adds `turns.model TEXT NULL`; ingest writes the
  per-message API model; `swarmery recost` resolves
  `COALESCE(turns.model, sessions.model)`; `turnDTO`/`Turn` expose
  `model: string | null`.
- **event_appended session attribution (frontend)** — ACCEPTED & IMPLEMENTED.
  `event_appended` payload is now `{ sessionId: number; event: Event }`
  (internal/api/ws.go, docs/ws-protocol.md, types.ts). Detail view attributes
  by `sessionId`; Overview/Sessions render a live `now: <last action>` line on
  session cards.
- **session list aggregates (frontend)** — DEFERRED, phase 2 candidates.
  `toolCalls`/`costUsd`/`lastAction` on `sessionDTO` need per-session
  aggregate queries (or denormalized counters) on the list endpoint; the live
  `now:` line above covers the most visible gap via WS without any schema or
  list-query cost. Revisit alongside the phase-2 session-card design.

---

## Phase 3.5 resolutions (2026-07-13, feat/swarmery-workspaces — E-lite)

Additive-only, applied on the phase branch (single agent, no parallel wave to
conflict with):

- **`Session` task attribution** — `taskId` / `taskExternalId` /
  `taskLinkSource` / `taskConfidence` (all optional-nullable): the best task
  link per session (explicit beats heuristic, then highest confidence),
  computed in one window-function JOIN in `sessionSelect`. Renders the task
  chip in Session Detail and the task badge in Sessions rows.
- **New endpoints** — `GET /api/tasks?days=<n>` (`TaskSummary[]`, default 14
  days, drives the Overview "Tasks · 14 days" slice) and
  `GET /api/tasks/{id}` (`TaskDetail`: card metadata + `sessionLinks[]` with
  per-session cost + Σ cost; `id` = row id or `externalId`).
- **New types** — `TaskLinkSource`, `TaskOutcome`, `TaskSummary`,
  `TaskSessionLink`, `TaskDetail`, `TasksResponse`.

---

<!-- Phase 2 — parallel wave (gate 2.2 freeze). Resolved at step 2.5/integration. -->

## 2026-07-13 — feat/swarmery-approvals-ui (wave B) — GET /api/approvals filter semantics

- What: confirm the `status` query param value set for `GET /api/approvals`:
  no param → all rows; `status=pending` → pending only; **`status=resolved` →
  meta-filter for ALL terminal statuses** (approved|denied|expired|
  resolved_elsewhere), ordered newest-resolved first, `LIMIT 50` server-side.
- Why: the frozen surface only names "GET /api/approvals?status=" without the
  value set; the step-2.4 plan prompt says "History (status=resolved, limit
  50)". The UI codes against exactly two fetches (`pending` + `resolved`) and
  slices history to 50 client-side as a belt-and-braces. If the backend only
  supports exact `PermissionRequestStatus` values, history needs four calls
  (or a no-param fetch) — say which at integration.
- Proposed shape:
  ```
  GET /api/approvals?status=pending|resolved|<exact status>  → PermissionRequest[]
  ```

## 2026-07-13 — feat/swarmery-approvals-ui (wave B) — POST /api/approvals/{id} conflict status

- What: confirm the non-2xx contract when the row is no longer `pending`
  (raced by terminal dialog, another dashboard, or the expiry sweeper) —
  plan says `409` with the usual `{"error": string}` body.
- Why: the UI resolves optimistically and treats ANY non-2xx as "resolved
  elsewhere first" → silent refetch, with the WS `permission_resolved` as the
  authoritative reconciliation. A defined 409 keeps that behavior intentional
  rather than accidental.

## 2026-07-13 — feat/swarmery-approvals-ui (wave B) — session attribution on PermissionRequest (nice-to-have)

- What: optional denormalized `projectSlug` / `projectName` / `sessionTitle`
  on the `PermissionRequest` DTO (additive, like `Session.projectName`).
- Why: approval cards and history rows show "project · session title"; today
  the UI lazily fetches `GET /api/sessions` once and joins client-side —
  works, but a server-side join removes a fetch and covers requests whose
  session is missing from the list response (plain `session #N` fallback).
  Not blocking.
- Proposed shape:
  ```ts
  export interface PermissionRequest {
    // …existing fields…
    projectSlug?: string;
    projectName?: string | null;
    sessionTitle?: string | null;
  }
  ```

---

## Step-2.6 resolutions (2026-07-13, integration)

- **GET /api/approvals filter semantics** — ANSWERED & ALREADY IMPLEMENTED.
  The backend (internal/api/approvals.go `listApprovals`) supports exactly the
  requested value set: no param / `pending` → pending only; **`status=resolved`
  → meta-filter for every terminal status** (`WHERE status != 'pending'`);
  `all` → everything; a concrete status name filters exactly. Ordered
  `requested_at DESC, id DESC`; `limit` is an optional query param (the UI's
  client-side slice-to-50 remains harmless belt-and-braces). Covered by
  `TestListApprovals`.
- **POST /api/approvals/{id} conflict status** — ANSWERED & ALREADY
  IMPLEMENTED. Racing a terminal state returns **409**
  `{"error":"permission request already resolved"}` (`ErrAlreadyResolved` in
  `resolveApproval`); 404 for unknown ids, 400 for bad action/body. The UI's
  "any non-2xx → silent refetch, WS is authoritative" behavior is therefore
  intentional. Covered by the double-resolve test (second resolve = 409).
- **denormalized projectSlug/projectName/sessionTitle on PermissionRequest** —
  DEFERRED (phase-2 backlog): additive but not trivial — it touches the frozen
  DTO golden key-set in `ws_test.go`, mock fixtures, and a multi-prop refactor
  of `Approvals.tsx`/`Overview` attribution; the existing lazy `/api/sessions`
  join already covers the UX with a `session #N` fallback.

---

## Canvas wave (2026-07-14, full-stack — landed together)

Three additive fields, implemented back-and-front in one change (not a parallel
branch), so `types.ts` was updated at the same time as the Go DTOs.

- **`Session.why`** (`why?: string \| null`, `omitempty`) — one-line intent
  summarised server-side from the first user turn's prose (first non-empty line,
  whitespace collapsed, capped at 160 chars). Source: `sessionSelect` window
  join in `handlers.go` + `summarizeWhy`. Feeds the Sessions row subtitle and
  the Command-deck spine why-line. Present in the WS session frame (golden
  key-set in `ws_test.go` updated).
- **`SessionDetail.recovered`** (`recovered: number`, always present) — count of
  tool errors a later same-tool success cleared (query-time heuristic in
  `getSession`). Feeds the session-detail header "recovered" stat.
- **`StatsToday` / `StatsOverview` `tests_passed|tests_failed|tests_skipped`**
  (`*int64`, `omitempty`) — summed from `test_run` event payloads. `test_run`
  events are emitted at ingest for recognised test-runner Bash calls
  (`internal/ingest/testrun.go`), parsing pytest/jest/vitest/go-`-v` summaries.
  Null when the window has no test signal → the Quality tile degrades instead of
  showing a misleading zero. Feeds the Command-deck Quality tri-stat.
