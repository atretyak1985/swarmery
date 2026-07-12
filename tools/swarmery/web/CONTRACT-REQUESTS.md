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
