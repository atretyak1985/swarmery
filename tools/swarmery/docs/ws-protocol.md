# WS protocol — `/api/ws`

Live-update stream for the dashboard. Implemented by the ingest pipeline
(wave A); the message names and payload shapes follow the **frozen**
`WSMessage` type in [`web/src/api/types.ts`](../web/src/api/types.ts) exactly.
Any change to this protocol is a contract change and goes through
`web/CONTRACT-REQUESTS.md`.

## Endpoint

```
GET /api/ws        → WebSocket upgrade (RFC 6455), no subprotocol
```

- Same host/port as the REST API (default `localhost:7777`).
- Cross-origin upgrades are allowed (the vite dev server proxies from another
  origin); the daemon is a localhost-only tool.
- The client never sends application frames; anything it sends is discarded.
- If the daemon runs without the ingest pipeline (`serve --no-ingest`), the
  endpoint returns `503` instead of upgrading.

## Frames

Every server frame is one **text** frame containing one JSON object:

```ts
type WSMessage =
  | { type: 'session_started'; payload: Session }
  | { type: 'session_updated'; payload: Session }
  | { type: 'event_appended';  payload: { sessionId: number; event: Event } }
  // phase 2 — approvals (frozen at gate 2.2):
  | { type: 'permission_requested'; payload: PermissionRequest }
  | { type: 'permission_resolved';  payload: PermissionRequest }
  // phase 4 — system registry (frozen at step-03):
  | { type: 'system_item_updated'; payload: SystemItemUpdate }
  // fusion phase 1 — task board:
  | { type: 'task_updated'; payload: BoardTask }
  // plans-page-lifecycle phase 1 — epics:
  | { type: 'plan_updated'; payload: { taskId: number; projectId: number } }
  // board task delete:
  | { type: 'task_deleted'; payload: { taskId: number; projectId: number } }
  // learning loop phase 13 — attention:
  | { type: 'phase_surprise'; payload: PhaseSurpriseAttention };
```

### `phase_surprise` (learning loop phase 13)

Emitted at most once per phase run, when the run's forecast-vs-actual surprise
score (`internal/surprise`, table `phase_surprise`) first reaches
`SWARMERY_SURPRISE_NOTIFY` (default 0.6; `off` disables). Advisory — nothing
gates on it. Payload: `{taskId, projectId, phaseId, phaseName, planTitle,
sessionUuid, index, top, summary}`. The notch currently ignores it (unknown
frames are skipped by design); the dashboard refetches the plan on the
`plan_updated` that accompanies every score change.

```json
{"type":"phase_surprise","payload":{"taskId":42,"projectId":1,"phaseId":7,"phaseName":"Surprise scoring","planTitle":"Learning loop","sessionUuid":"9f22…","index":0.72,"top":"outcome_miss","summary":"surprise 0.72 — top: outcome_miss; forecast done, run partial"}}
```

`Session` and `Event` are byte-for-byte the same JSON DTOs the REST API
serves (`GET /api/sessions`, `GET /api/sessions/{id}.events[]`) — defined in
`internal/api/handlers.go` (`sessionDTO`, `eventDTO`) and mirrored in
`types.ts`. There is no envelope beyond `type` + `payload`.

### `session_started`

Emitted once when a transcript for a previously unknown session UUID is first
ingested (a new `sessions` row was created).

```json
{"type":"session_started","payload":{
  "id":1,"projectId":1,"projectSlug":"-home-dev-example","sessionUuid":"9f22596e-…",
  "model":null,"gitBranch":"main","cwd":"/path/to/example","status":"active",
  "startedAt":"2026-07-12T14:03:54.000Z","endedAt":"2026-07-12T14:03:54.000Z",
  "title":"live tail demo","source":"jsonl"}}
```

### `session_updated`

Emitted when an existing session gets new transcript records, **and** by the
status ticker whenever a session transitions `active → idle → completed`
(time-based thresholds, default 2 min / 30 min). The payload is the full
fresh `Session` — clients should upsert it by `id`, not diff it.

### `event_appended`

Emitted once per newly created `events` row, in insert order, after
`session_started`/`session_updated` for the same batch. The payload wraps the
`Event` DTO with its owning `sessionId` so list views can attribute live
events to a session card (contract change accepted at step 10 — see
`web/CONTRACT-REQUESTS.md`).

```json
{"type":"event_appended","payload":{"sessionId":1,"event":{
  "id":2,"turnId":2,"ts":"2026-07-12T14:03:58.000Z","type":"user_prompt",
  "toolName":null,"parentEventId":null,"status":null,"durationMs":null,
  "payload":{"content":"second live line","promptSource":"typed"}}}}
```

`payload.event.payload` is the raw event payload JSON (`unknown` client-side),
exactly as the REST detail endpoint returns it.

### `permission_requested` (phase 2)

Added at gate 2.2 (phase 2 — approvals); the MVP trio above is unchanged and stays
byte-identical. Emitted by the approvals layer once per **new**
`permission_requests` row created by `POST /api/hooks/permission-request`
([`docs/hooks-protocol.md`](hooks-protocol.md)). Deduplicated concurrent requests
attach to the existing pending row and do **not** re-emit. The payload is the full
`PermissionRequest` DTO; `requestJson` is the raw hook stdin as a JSON string.

```json
{"type":"permission_requested","payload":{
  "id":7,"sessionId":42,"toolName":"Bash",
  "requestJson":"{\"session_id\":\"9f22596e-…\",\"hook_event_name\":\"PermissionRequest\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"curl -sI https://example.com | head -1\",\"description\":\"Fetch HTTP status line\"},\"permission_suggestions\":[…]}",
  "status":"pending",
  "requestedAt":"2026-07-13T10:15:04.000Z",
  "resolvedAt":null,"resolvedVia":null,"reason":null,
  "expiresAt":"2026-07-13T10:17:04.000Z"}}
```

### `permission_resolved` (phase 2)

Emitted by the approvals layer whenever a pending request leaves `pending` — for
**every** terminal status: `approved`, `denied`, `expired`, and
`resolved_elsewhere` (expiry and client-disconnect emit it too, so badge counters
always converge). The payload is the same full `PermissionRequest` DTO with the
resolution fields populated; clients upsert by `id`.

```json
{"type":"permission_resolved","payload":{
  "id":7,"sessionId":42,"toolName":"Bash",
  "requestJson":"{…verbatim hook stdin…}",
  "status":"approved",
  "requestedAt":"2026-07-13T10:15:04.000Z",
  "resolvedAt":"2026-07-13T10:15:31.000Z",
  "resolvedVia":"dashboard","reason":null,
  "expiresAt":"2026-07-13T10:17:04.000Z"}}
```

Session status changes caused by approvals (`→ waiting_approval` and back) ride the
existing `session_updated` message, unchanged.

### `system_item_updated` (phase 4)

Added at step-03 (phase 4 — system registry); everything above is unchanged.
Published on the internal bus by `internal/sysscan` whenever one config item —
agent, skill, hook entry, or command — is created, changes content (a new
`*_versions` row for agents/skills), or is soft-deleted. The payload carries
ids only: it is a **cache-invalidation hint**, not data — clients refetch the
item from the `/api/system/*` endpoints. `kind` names the registry table the
id points into.

```json
{"type":"system_item_updated","payload":{"kind":"agent","itemId":42}}
```

Emission note: the bus constant (`ingest.NoteSystemItemUpdated`) and the
payload contract are frozen at step-03; the WS-side hydration of this frame is
wired together with the `/api/system/*` endpoints at step-05. Until then the
note exists on the internal bus only.

### `plan_updated` (plans-page-lifecycle phase 1)

Added by the plans-page-lifecycle program; everything above is unchanged.
Published on the internal bus (`ingest.NotePlanUpdated`) by two emitters:

1. **wsingest** — whenever a workspace task's `plan/` content hash (or its
   on-disk location) changes during a scan pass, i.e. a checkbox flip, a plan
   doc edit, or a zone move was ingested;
2. **the epic lifecycle endpoint** (`POST /api/epics/{taskId}/lifecycle`) —
   after every pause / resume / archive / restore action.

The payload is intentionally thin — a **cache-invalidation hint**, not data:
clients refetch `GET /api/epics` (the same pattern the Plans page uses for
`task_updated`). `taskId` is the workspace task (`tasks.id`), `projectId` its
owning project.

```json
{"type":"plan_updated","payload":{"taskId":42,"projectId":1}}
```

`web/src/api/types.ts` picks the union member up in phase 2 of the program.

### `task_deleted`

Added with the board's delete action; everything above is unchanged. Published
by `DELETE /api/board/tasks/{id}` (`ingest.NoteTaskDeleted`) after the row is
permanently removed — the escape hatch `archived` is not, since an archived task
still sits in a column.

This is the one board frame that carries **no data**: the row is gone by the
time the frame is built, so it cannot be hydrated into a `BoardTask` the way
`task_updated` is. Clients drop the card by `taskId`; `projectId` rides along on
the notification (it cannot be looked up afterwards) so a board scoped to
another project can ignore the frame.

```json
{"type":"task_deleted","payload":{"taskId":42,"projectId":1}}
```

A running task is refused with `409` instead of deleted, so no frame is emitted
for it.

## Delivery semantics

- **Hint stream, not a source of truth.** Delivery is at-most-once: a slow
  consumer's buffer (256 messages) drops the overflow silently. On connect —
  and after any suspected gap — clients should resync via REST
  (`GET /api/sessions`, `GET /api/sessions/{id}`) and treat WS messages as
  cache-invalidation hints with payloads fresh enough to apply directly.
- **No replay.** Messages published before the socket connected are gone;
  there is no cursor/ack protocol in the MVP.
- **Ordering** is per-connection FIFO. Within one ingest batch the session
  message precedes its `event_appended` messages.
- **Reconnect** with plain exponential backoff; the server sends no pings
  beyond the standard WebSocket keepalive handled by the library
  (`github.com/coder/websocket`).

## Emission sources (server internals)

| Source | Emits |
|---|---|
| Tail of a transcript creating a session row | `session_started` |
| Tail of a transcript adding records to a known session | `session_updated` |
| Every new `events` row from a tail batch | `event_appended` |
| Status ticker transition (active→idle→completed) | `session_updated` |
| Approvals: new `permission_requests` row (phase 2) | `permission_requested` |
| Approvals: request leaves `pending` — any terminal status (phase 2) | `permission_resolved` |
| sysscan: config item created / new content version / soft-deleted (phase 4, WS wiring at step-05) | `system_item_updated` |
| wsingest: a task's `plan/` content hash or location changed during a scan pass | `plan_updated` |
| Epic lifecycle endpoint: pause / resume / archive / restore applied | `plan_updated` |
| Board create / patch (column move, field edit, pause) | `task_updated` |
| `DELETE /api/board/tasks/{id}`: a queue row permanently removed | `task_deleted` |

## BoardTask provenance fields

`GET /api/board/tasks`, the POST/PATCH responses and the `task_updated` payload all
carry the same `BoardTask`. Three fields describe where a card came from and what
became of it (migration 0066); each is `null` when it does not apply.

| Field | Type | Meaning |
|---|---|---|
| `source` | `{ sessionId, turnUuid, quote, files } \| null` | Capture provenance. `null` for a hand-written card and for a verifier fix task. `sessionId` is the session the card was captured from, `turnUuid` the transcript record that minted it, `quote` that session's opening prompt (clipped to 400 characters at capture; older rows keep the longer excerpt the migration moved out of `prompt`), `files` the files the session had touched by then (never `null` inside a non-null `source`). |
| `staleAfter` | RFC 3339 `\| null` | When the inbox sweeper will retire the card: the same predicate `SweepStaleInbox` runs (`source='queue'`, `boardColumn='triage'`, `origin` in `session`/`llm`, no worktree) and the same clock (`columnMovedAt`, else `createdAt`) plus `SWARMERY_INBOX_TTL`. `null` for every card the sweep can never touch, and for every card when the sweep is disabled. Derived per request, never stored. |
| `dispatchedPrompt` | string `\| null` | The exact first-stage prompt the dispatcher handed the runner — card body, provenance block, rendered recipe stage and execution contract — recorded at dispatch. `null` until the card has been dispatched. |

`origin` gained the value `verify-fix` for fix tasks the verifier mints off a failed
verdict; `originSessionId` stays as the flat form of `source.sessionId`.

A captured card's `prompt` no longer carries the session's opening prompt as prose
(`That session opened with: …`); that text is `source.quote`, and the dispatcher
appends it to the run exactly once.
