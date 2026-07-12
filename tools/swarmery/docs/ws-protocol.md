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
  | { type: 'event_appended';  payload: { sessionId: number; event: Event } };
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
  "id":1,"projectId":1,"projectSlug":"-Volumes-Work-example","sessionUuid":"9f22596e-…",
  "model":null,"gitBranch":"main","cwd":"/Volumes/Work/example","status":"active",
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
