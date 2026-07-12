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
