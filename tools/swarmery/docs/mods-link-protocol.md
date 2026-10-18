# Link HTTP protocol — daemon ↔ swarmery link mod

**Status:** v1 — additive changes only. Shipped by the mods-swarmery-link program
(phases 1–10) and matched against the code at its close (phase 11). This document
defines the HTTP contract between the daemon and the swarmery link, a Claude Code mod
(the plugin `plugins/swarmery-link`, one `hooks/register.ts` module) that reports live
session state to the daemon. A new field, event kind or route may be added; a breaking
change needs a new route, not an edit to this one.

Sections: [Transport](#transport) and [Auth](#auth), [Events](#events-post-apilinkevents),
[Commands](#commands), [Permission](#permission), [Forecast](#forecast),
[Swarm](#swarm), [Plan](#plan).

Every mod-side fact below comes from the live spike
[`mods-format.md`](mods-format.md) (Claude Code `2.1.296`). M1…M7 refer to its
experiments. The shim protocol ([`hooks-protocol.md`](hooks-protocol.md)) is
untouched: `/api/hooks/*` work exactly as before, with or without the mod.

Implementation: `internal/api/link.go` (routes), `internal/link` (token, auth,
ingest), `internal/ingest/status.go` (status override), `internal/prune` (retention),
migration `0107_link_sessions.sql`. The control channel (phase 4, [Commands](#commands))
adds `internal/api/link_commands.go`, `internal/link/commands.go` and migration
`0108_link_commands.sql`. The approvals channel (phase 5, [Permission](#permission))
adds `internal/api/link_permission.go`, `internal/approvals/mod.go` and migration
`0115_permission_channel.sql`. The swarm (phase 6, [Swarm](#swarm)) adds
`internal/api/link_swarm.go`, `internal/link/swarm.go` and migration
`0116_link_swarm.sql`. The plan view (phase 7, [Plan](#plan)) adds
`internal/api/link_plan.go`, `internal/link/drift.go` and migration
`0117_session_plan.sql`. The turn forecast (phase 10, [Forecast](#forecast)) adds
`internal/link/forecast.go`, `internal/turnforecast`, `internal/api/link_forecast.go`
and migration `0113_turn_forecasts.sql`. Outside this contract, the System → Mods audit
(phase 9: `internal/modscan`, `internal/sysscan`) adds migration `0112_mods.sql`. The
program's migrations are therefore 0107, 0108, 0112, 0113, 0115, 0116 and 0117; the
gap 0109–0111 is deliberate (those numbers moved past the highest applied version
before merging), and 0114 is an unrelated routing migration that landed first.

## Transport

- Base URL: the daemon's REST origin (default `http://127.0.0.1:7777`). The mod
  dials the loopback address. It never uses a name that needs DNS.
- **Host fence applies.** Every `/api/*` route, these included, requires a `Host`
  header that names the daemon (loopback name or address on the bound port, the
  listener's own address, or a `SWARMERY_TRUSTED_ORIGINS` host). Anything else gets
  `403`. This is the DNS-rebinding fence (`internal/api/origin.go`
  `requireLocalHost`). `$.http.fetch` to `http://127.0.0.1:<port>` passes by
  construction.
- **No origin fence.** Unlike every other write route, `/api/link/*` are **not**
  wrapped in `requireLocalOrigin`. The mod sends no `Origin`, so that fence would
  wave it through and prove nothing. The bearer token below is the credential
  instead. A request with a token is served whatever its `Origin` says. A browser
  page cannot read the token file, so it cannot produce one.
- `Content-Type: application/json` on requests with a body. Responses are always
  JSON. Errors are `{"error": string}`, like the rest of the API.

## Auth

| | |
|---|---|
| Token file | `~/.swarmery/link-token` (`internal/link.TokenPath`), beside `swarmery.db` |
| Mode | `0600`. A wider mode found on disk is narrowed back to `0600` before the file is read. A missing `~/.swarmery` is created `0700` |
| Content | 32 random bytes, hex-encoded (64 chars), one trailing newline. Readers trim whitespace |
| Minted | by the daemon at start (`swarmery serve`, next to the DB open) or by the first `swarmery link token`, whichever comes first. A missing or empty file is replaced by a fresh token |
| Header | `Authorization: Bearer <token>`. The scheme is case-insensitive, the comparison constant-time (`crypto/subtle`) |
| Logged | the **path** only (`swarmery link token at …` at daemon start). The value never reaches a log, a response body or an error message |

**Reading the token in the mod (M7).** `~` is not expanded by `$.fs.read`, which
resolves it against the session cwd. Spell `HOME` out:

```ts
const home = await $.env.get('HOME')
const token = (await $.fs.read(`${home}/.swarmery/link-token`)).trim()
```

Wrap the read in `try`. A missing file rejects with `HooksError: … ENOENT`, and that
is the mod's "link off" path (see Degradation).

**401.** Any request without a valid token (no header, another scheme, an empty
credential, a wrong value) gets:

```
HTTP/1.1 401 Unauthorized
Content-Type: application/json
WWW-Authenticate: Bearer realm="swarmery-link"

{"error":"link token required"}
```

The body is exact and stable. The mod can match it.

**Rotation.** `swarmery link token --rotate` writes a new token to a `0600` temp file
in the same directory and renames it over the old one. Readers see either the old
token or the new one, never a partial file. A running daemon needs no restart and no
signal. It keeps the token in a `link.Holder`. Every request that carries a bearer
first refreshes the Holder from the file, throttled to at most one read per second
across all requests, and only then compares. A missing, empty or unreadable file
keeps the current value. In practice:

1. The operator rotates. The file now holds the new token.
2. Within about 1 s the next authenticated request re-reads the file. It does not
   matter which token that request carries.
3. From then on the old token is `401` and the new one is served (`202`/`200`).

> **Boundary.** Inside the one-second throttle window that follows the last
> re-read, the daemon still answers from its cached value. The old token keeps
> working there, and the new one gets `401`. Once the window passes, the old token
> stops working with no restart, whether or not any client has presented the new
> token. Rotating is therefore enough to revoke a leaked token.

A mod that gets `401` should re-read the token file once and retry once, no sooner
than 1 s after the `401` so the retry lands past the throttle window, then treat
the link as off for the rest of the session. A re-read and retry on every event
would hammer the disk and the daemon.

## Events: POST /api/link/events

Delivers a batch of session events.

### Request

The body is a JSON **object** with an `events` array (an envelope, so fields can be
added later without breaking older mods). A bare top-level array is rejected with
`400`.

```json
{
  "events": [
    { "sessionId": "efaa9b56-306f-48b7-bba8-51c5ffa08f79", "cwd": "/Users/me/work/app",
      "kind": "turn.start", "at": "2026-10-13T10:00:00.000Z" },
    { "sessionId": "efaa9b56-306f-48b7-bba8-51c5ffa08f79", "cwd": "/Users/me/work/app",
      "kind": "tool.call", "at": "2026-10-13T10:00:01.250Z",
      "payload": { "tool": "Bash", "inputHash": "9f2c…", "agentId": "a-1" } },
    { "sessionId": "efaa9b56-306f-48b7-bba8-51c5ffa08f79", "cwd": "/Users/me/work/app",
      "kind": "session.measure", "at": "2026-10-13T10:00:07.900Z",
      "payload": { "context": { "tokens": 32108, "window": 1000000, "percent": 3 },
                   "rateLimits": [ { "kind": "five_hour", "percentUsed": 9,
                                     "resetsAt": "2026-10-13T20:20:00.000Z" } ],
                   "cost": { "usd": 0.0043368899999999995 },
                   "changed": ["context", "rateLimits", "cost"] } },
    { "sessionId": "efaa9b56-306f-48b7-bba8-51c5ffa08f79", "cwd": "/Users/me/work/app",
      "kind": "turn.complete", "at": "2026-10-13T10:00:08.010Z" }
  ]
}
```

| Field | Type | Rule |
|---|---|---|
| `sessionId` | string, required | `$.session.id()`. M1: it **is** the JSONL `sessionId` and the transcript file name, so the daemon keys on `sessions.session_uuid` with no translation. A `--fork-session` gets a new id by design |
| `cwd` | string | `$.session.cwd()`. Resolves the project when the daemon has to mint the session row, and drives the exclude check. May be empty (the row is parked on the `(unknown)` project until the transcript shows the real cwd) |
| `kind` | string, required | one of the kinds below. Anything else is rejected **per event** |
| `at` | string, required | RFC 3339 with or without fractional seconds and in any offset. The daemon stores it as fixed-width UTC with milliseconds (`2006-01-02T15:04:05.000Z`) |
| `payload` | object, optional | per kind, below. Must be valid JSON. Stored verbatim. `null` and absent are the same |

Limits:

| Limit | Value | Over it |
|---|---|---|
| Body size | 1 MiB (`http.MaxBytesReader`) | `413 {"error":"body exceeds 1 MiB"}` |
| Events per batch | 200 | `413 {"error":"batch exceeds 200 events"}` (nothing stored; split the batch) |

### Event kinds and payloads

| `kind` | Mod hook | `payload` | Effect on `sessions` |
|---|---|---|---|
| `session.start` | `session.start` | optional, e.g. `{ "surface": "terminal", "isInteractive": true }` (M1 `e`) | `link_seen_at` |
| `turn.start` | `prompt.submit` | none | `link_status = 'working'` |
| `tool.call` | `tool.call` | `{ "tool": string, "inputHash": string, "agentId"?: string }` | `link_seen_at` |
| `session.measure` | `session.measure` | the M2 `e` shape: `context{tokens?,window,percent?}`, `rateLimits[]`, `cost{usd}`, `changed[]` ([`testdata/fixtures/mods/session-usage.json`](../testdata/fixtures/mods/session-usage.json)) | `link_context_pct`, `link_rate_limits`, `link_cost_usd` |
| `turn.complete` | `turn.complete` | none required | `link_status = 'waiting'` |
| `session.end` | `session.end` | optional, e.g. `{ "reason": "prompt_input_exit" }` | `link_status = 'done'` |

Every accepted event also advances `sessions.link_seen_at` to the newest `at` seen.

**`tool.call` carries no input.** The mod sends the tool name, a hash of the input
(`inputHash`, e.g. hex SHA-256 of the canonical JSON) and the subagent's `agentId`
when the call runs inside one. It never sends the input itself. The full input is in
the JSONL transcript already, and a second copy over HTTP is a second place for file
contents and secrets to leak. The daemon stores the payload verbatim and does not
check this. The rule is the mod's to keep.

**`session.measure` units are optional (M2).** An absent `context.percent` (before
the first response) or an empty `rateLimits` (off a subscription) leaves the previous
column value in place. "Not reported" is not "zero". `cost.usd` is **per process**:
it restarts at 0 after `--resume`, so `link_cost_usd` is that process's running total,
not the session's lifetime cost.

**Order.** `session.measure` and `turn.complete` arrive in no fixed order (M2), and
batches can be retried. The daemon never rolls state back. Only the newest
status-bearing event (`turn.start`, `turn.complete`, `session.end`) by `at` decides
`link_status`, and only the newest `session.measure` decides the measure columns.
The mod does not need to sort.

### Response

`202 Accepted` whenever the batch was processed, even if some events were rejected:

```json
{ "accepted": 3, "duplicates": 1, "rejected": 1,
  "rejects": [ { "index": 4, "reason": "unknown kind \"turn.bogus\"" } ] }
```

| Field | Meaning |
|---|---|
| `accepted` | stored now |
| `duplicates` | already stored by an earlier request (see Dedup) and skipped. Not an error |
| `rejected` | not stored and never will be: invalid event or excluded project |
| `rejects` | one entry per rejected event (omitted when none). `index` is the event's position in **the batch the mod sent** |

`accepted + duplicates + rejected` = the number of events sent. An empty `events`
array is `202` with all three at `0`.

Per-event reject reasons: `sessionId is required`, `unknown kind "<k>"`,
`at "<v>" is not RFC 3339`, `payload is not valid JSON`,
`session.measure payload: <decode error>`, `excluded project`. A rejected event is
final. Resending it gets the same answer, so the mod drops it.

### Status codes

| Code | When | Mod action |
|---|---|---|
| `202` | batch processed (body above) | drop the batch. Rejected events are not retried |
| `400` | body is not JSON, not the `{"events":[…]}` object, or has no `events` key | a mod bug: log once and drop the batch |
| `401` | missing or wrong token (exact body above) | re-read the token file once and retry once, then treat the link as off |
| `403` | `Host` does not name the daemon | a mod bug (wrong base URL): drop it |
| `413` | body > 1 MiB or > 200 events | split and resend. Nothing from this request was stored |
| `500` | database error. The **whole batch** was rolled back | retry later with the same batch. Dedup makes that safe |
| `503 {"error":"link disabled"}` | the daemon could not load a token at start, so the link is off | treat the link as off. The JSONL tail still records the session |

## GET /api/link/health

The mod's "is the daemon there, and does my token work" probe, e.g. on
`session.start`. Behind the same token.

```
200 {"ok":true,"daemon":"0.2.1-213-g0631890a"}
```

`daemon` is the daemon's build identity (`internal/version.String()`, the same value
as `GET /api/health` `build`). A `401` here means the token is stale, so re-read the
file. Connection refused means the daemon is down (see Degradation). `503` means the
link is off.

## Commands

**Status:** phase 4 contract (control channel). The dashboard queues a command for
one session, and the mod of that session long-polls the queue, runs the command and
acks it. Two kinds exist:

| `kind` | `payload` | The mod runs |
|---|---|---|
| `stop` | `{}` | `$.turn.abort()` |
| `prompt` | `{"text": string, "asUser": bool}` | `$.prompt.submit({ text, asUser })` |
| `message` | `{"to": string, "text": string}` | `$.session.send({ to, text })` — a dashboard message relayed through this (the sending) session; see [Swarm](#swarm) |

The mod runs commands only when its `userConfig.control` is `true`. With `control`
off it never polls, so its sessions are never controllable and the dashboard's
requests get `409`. Commands run from the mod's background poll loop only, never from
inside a `tool.check` or `tool.call` hook (`$.prompt.submit` rejects while a turn waits
on a hook).

### Controllable

A session is **controllable** while its mod's last poll is younger than 60 s
(`link.PollFreshness`). The daemon keeps the poll times in memory. After a restart no
session is controllable until its mod polls again, which a healthy loop does within
one long-poll (≤ 28 s). The dashboard reads it as `link.controllable` on the session
DTO (a `link` object is present whenever this is true, even before the mod's first
event), and the per-session routes below answer `409` without it. A command can therefore
only reach a session whose own mod polls with the token, so the dashboard cannot steer
a session that has no mod.

### Lifecycle

```
queued ──poll──▶ delivered ──ack done──▶ done
  │                  │  ╰──ack failed──▶ failed
  │                  ├──ack failed "turn in progress" (prompt, < 3rd time)──▶ queued, held for turn.complete
  │                  ├──stop, no ack, older than 30 s──▶ expired
  │                  ╰──prompt, no ack for 30 s, older than 10 min──▶ expired
  ╰──too old, never delivered──▶ expired
```

| Rule | Value |
|---|---|
| Delivery order | oldest first (`created_at`, then `id`), one command per poll |
| Re-delivery | a `delivered` command with no ack is handed out again on the first poll more than 30 s (`link.RedeliverAfter`) after its last delivery. A restart or a lost reply never strands it. `delivered_at` holds the latest delivery. A `stop` is re-delivered only while it is younger than 30 s (`link.StopTTL`, from `created_at`); an older one becomes `expired` instead, so a stop the mod ran but whose reply was lost never aborts a later turn |
| Stop expiry | a `stop` still undelivered 30 s after it was queued becomes `expired` and is never delivered. A late stop must not kill a later turn. A later ack of an expired stop is a no-op `204` |
| Prompt expiry | a `prompt` still undelivered 10 min after it was queued becomes `expired`. This covers a prompt held for a turn end that never comes. A `delivered` prompt with no ack that no poll has re-delivered for 30 s also becomes `expired` once it is 10 min old: its mod went away |
| Turn in progress | an ack `{"result":"failed","message":"turn in progress"}` (exact string) on a `prompt` sends it back to `queued` (`delivered_at` cleared, `attempts`+1), held until the next `turn.complete` link event for that session is **stored** (a resent duplicate does not count). If that turn already ended — a stored `turn.complete` at or after the command's `delivered_at`, with no `turn.start` after it, because the mod's events beat its ack — the prompt is deliverable again at once and a waiting poll wakes. The 3rd such ack is a final `failed` |
| Other failures | any other `failed` ack is final |

Expiry runs inline on every poll and on a 5 s daemon ticker, so the `expired` frame
arrives on time even when no mod polls.

### GET /api/link/commands

The mod's long-poll. Behind the bearer token like `/api/link/events`. There is no
origin fence, and the Host fence applies.

```
GET /api/link/commands?session=<Claude session id>&wait=<seconds>
```

| Parameter | Rule |
|---|---|
| `session` | required, the `$.session.id()` the events carry. A session the daemon has no row for yet is legal and gets `204` |
| `wait` | integer seconds. Missing means `0`, above `25` is clamped to `25`, not an integer or negative is `400` |

Every poll, including a `204`, records the session as polled (see Controllable).

| Code | Body | When |
|---|---|---|
| `200` | `{"id": 7, "kind": "stop", "payload": {}}` | the oldest deliverable command. It is now `delivered` with `delivered_at` set |
| `204` | none | nothing became deliverable before `wait` elapsed or the request ended |
| `400` | `{"error":…}` | `session` missing, bad `wait` |
| `401` / `503` | as for events | token / link off |

A queued command wakes a waiting poll at once, and so does a `turn.complete` that
releases a held prompt. The mod should race the request against a 28 s timer (its
`$.http.fetch` has no timeout), loop at once after a `204`, and back off 3 s after a
network error or `5xx`, never more than one request per second while failing.

### POST /api/link/commands/{id}/ack

```json
{ "result": "done" }
{ "result": "failed", "message": "turn in progress" }
```

| Field | Rule |
|---|---|
| `result` | `done` or `failed` |
| `message` | optional, at most 500 characters |

| Code | When |
|---|---|
| `204` | recorded. A repeat ack, or an ack of a command that is not currently `delivered` (already acked, expired, or back in the queue), is also `204` and changes nothing, so a re-delivered command's second ack is safe |
| `400` | `id` not an integer, body not JSON, bad `result`, `message` too long |
| `404` | no command with that id |

The mod keeps the ids it has already executed. A re-delivered id is acked `done` again
**without** running `abort` or `submit` a second time.

### Dashboard routes

These are SPA routes behind `requireLocalOrigin` (a foreign `Origin` gets `403`), not
the token. `{id}` is `sessions.id`.

| Route | Body | Answers |
|---|---|---|
| `POST /api/sessions/{id}/link/stop` | none | `202 {"commandId": n}`, `404` unknown session, `409 {"error":"session has no link"}` not controllable |
| `POST /api/sessions/{id}/link/prompt` | `{"text": string, "asUser"?: bool}` | `202 {"commandId": n}`, `400` text empty, whitespace or over 16 KiB, `404`, `409` as above |
| `POST /api/sessions/link/bulk-prompt` | `{"sessionIds": [int], "text": string}` | `200 {"queued": [{"id", "commandId"}], "skipped": [{"id", "reason"}]}` with reason `session has no link` or `not found`. `400` for empty or too-long text, an empty list, or more than 50 ids |

A bulk prompt is sent with `asUser: false`. A repeated id is queued once. A non-integer
`{id}` is `400`. With the control channel not attached, every route here and both mod
routes answer `503 {"error":"link disabled"}`. The existing `POST /api/sessions/{id}/stop`
(daemon-spawned runs) and `/message` (headless resume) are unrelated and unchanged.

### Live updates

Every transition (queued, delivered, done, failed, expired, and the return to `queued`
after `turn in progress`) is published on the WS bus as a `link_command` frame
([`ws-protocol.md`](ws-protocol.md)):

```json
{"type":"link_command","payload":{"sessionId":12,"commandId":7,"kind":"prompt","state":"queued","message":"turn in progress"}}
```

### Storage

Table `link_commands` (migration `0108`): `id, session_id, kind, payload, state,
attempts, created_at, delivered_at, acked_at, result, message, wait_turn`, indexed on
`(session_id, state, created_at)`. There is no foreign key, for the reason `0107`
gives.

## Permission

**Status:** phase 5 contract (approvals channel). The mod asks the daemon about a
tool call from inside its `tool.check` hook and polls for the operator's decision
(design A of [`mods-format.md`](mods-format.md) "Design consequences (a)", verified by
M3). All three routes sit behind the bearer token like the rest of `/api/link/*`: no origin
fence, the Host fence applies, `401 {"error":"link token required"}` without a valid
token, `503 {"error":"link disabled"}` with the link off and
`503 {"error":"approvals unavailable"}` with the approvals service off.

**Layered on the shim, not beside it.** The shim protocol
([`hooks-protocol.md`](hooks-protocol.md), frozen) is **unchanged**:
`POST /api/hooks/permission-request` keeps its body, statuses and behaviour byte for
byte. This channel writes the **same** `permission_requests` rows through the same
approvals service: the same D6 dedup hash (`session + tool + canonical tool_input`,
no agent id, so a shim request for the same call hashes equal), the same
production-deploy guard, the same auto-approve rules, the same per-session cap, the
same dashboard actions (`POST /api/approvals/{id}`), WS frames and needs-you items.
Only the delivery differs: the shim holds one long-poll for the whole window, the mod
asks once and then polls in short GETs. The shim stays the fallback whenever the mod
hands a call back.

| Column | Meaning |
|---|---|
| `channel` | `shim` or `mod`: which hook channel delivered the row (migration `0115`). The `PermissionRequest` DTO and the needs-you item carry it as `channel` |
| `tool_use_id` | the engine's `toolu_…` id of the call (`e.tool_use_id`). NULL on shim rows |
| `resolved_via` | **who decided**, as before: `dashboard`, `rule`, `terminal`, `local-only`, `restart`, or NULL on an expiry. Never `mod` |

### POST /api/link/permission

Body (≤ 4 MiB):

```json
{"sessionId":"<uuid>","cwd":"/abs/path","toolName":"Bash","toolInput":{"command":"make test"},
 "toolUseId":"toolu_…","agentId":"…","permissionMode":"default"}
```

`sessionId` and `toolName` are required. `agentId` and `permissionMode` are optional.
The row's `request_json` is synthesized in the hook-stdin shape the shim stores —
`{"session_id","cwd","tool_name","tool_input","tool_use_id","agent_id"?,"permission_mode"?,"hook_event_name":"tool.check"}`
— so the dashboard, the needs-you preview and the AskUserQuestion answer path read a
mod row exactly like a shim row.

| Code | Body | When |
|---|---|---|
| `202` | `{"id":N,"status":"pending","expiresAt":"2026-10-14T12:10:00.000Z","pollMs":25000}` | the row is waiting for a decision. `expiresAt` is the end of the approval window (RFC 3339, ms, UTC): stop polling there and return `next(e)` |
| `200` | `{"id":N,"decision":"local"}` | production deploy: the row is handed to the session's own terminal at once (`resolved_elsewhere`, via `local-only`). Return `next(e)` so the engine's own dialog asks. The dashboard can never approve it (`403 prod_deploy_local_only`) |
| `200` | `{"id":N,"decision":"allow","via":"rule"}` | an enabled auto-approve rule matched |
| `200` | `{"id":N, …}` + the GET body below | a re-POST of a call (same `sessionId` + `toolUseId`) that is already decided |
| `204` | none | the cwd is excluded and the session has no row: nothing is stored. Return `next(e)` |
| `400` | `{"error":…}` | body not JSON, `sessionId` or `toolName` missing |
| `413` | `{"error":…}` | body over 4 MiB |
| `429` | `{"error":…}` | 20 requests of this session already pending (the shim's cap) |

**Idempotent.** A re-POST with the same `toolUseId` (a retry after a lost reply) finds
the row the first POST created (`(session, tool_use_id)`): `202` with the same id while
it is pending, its decision once it is not. A POST whose call matches a **pending** row
by dedup hash (any channel) attaches to that row instead of opening another one. A
shim-created row it attaches to takes the mod's `tool_use_id`.

### GET /api/link/permission/{id}

```
GET /api/link/permission/{id}?session=<uuid>&wait=<seconds>
```

| Parameter | Rule |
|---|---|
| `session` | required, the `sessionId` of the POST. A row of another session is `404` |
| `wait` | integer seconds. Missing means `25`, above `25` is clamped to `25`, negative is `0`, not an integer is `400` |

| Code | Body | When |
|---|---|---|
| `204` | none | still pending after `wait` (or the request ended). Poll again at once |
| `200` | `{"decision":"allow"}` | approved |
| `200` | `{"decision":"allow","answer":{"questions":[…],"answers":{…}}}` | an `AskUserQuestion` answered from the dashboard. `answer` is the `{questions, answers}` object verbatim; return it as `tool.call`'s `{ result }` (M4). This is the form **whatever** `serve --answer-delivery` says: that flag only switches the shim's wire form |
| `200` | `{"decision":"deny","message":"<reason>"}` | denied. `message` is the operator's reason, possibly `""` |
| `200` | `{"decision":"pass","status":"expired"\|"resolved_elsewhere"}` | no decision: the window closed, the operator chose "answer in terminal", the call is a production deploy, or the lease lapsed. Return `next(e)`: the engine's own verdict (and the shim, see below) takes over |
| `400` | `{"error":…}` | `id` not an integer, `session` missing, bad `wait` |
| `404` | `{"error":…}` | no such row, or a row of another session |

### DELETE /api/link/permission/{id}

```
DELETE /api/link/permission/{id}?session=<uuid>
```

The mod gives up on a request: its hook's dispatch was abandoned mid-wait
(`next.signal` aborted — the person interrupted, or a hook above settled first).
Without it the row would stay pending under a live lease for up to 40 s, and a
dashboard decision in that gap would answer `200` while reaching no hook.

| Code | Body | When |
|---|---|---|
| `204` | none | given up. A **pending** row with no shim long-poll on it resolves `resolved_elsewhere` via `terminal` at once (the record a lapsed lease leaves); with a shim attached, only the mod's lease and its own in-flight polls are dropped, and the shim's decision or hang-up settles the row. A row already terminal is left as is (idempotent) |
| `400` | `{"error":…}` | `id` not an integer, `session` missing |
| `404` | `{"error":…}` | no such row, or a row of another session |

The mod sends it fire-and-forget, raced against a 2 s `$.clock` timer, and never
holds the hook's return on it: a lost `DELETE` only falls back to the lease lapsing.

### Lease

The shim's single long-poll is how the daemon knows someone can still receive a
decision. The mod polls in GETs with gaps between them, so it claims its row with a
**lease** instead: every POST and GET (pending or timed-out alike) renews it to
**40 s** from now (`approvals.ModLeaseTTL`: one 25 s poll plus slack).

- While the lease is live the row counts as listened to: a dashboard approve, deny,
  answer or terminal hand-off made **between two polls** succeeds (`200`, not the
  `410` of a row with no waiter), and the decision is kept for the mod's next GET.
- When the lease lapses with nobody attached, the asking hook is gone: the sweeper
  (every 5 s) resolves the row `resolved_elsewhere` via `terminal`, the same record the
  shim's client-disconnect leaves. A dashboard decision on such a row is `410`. A mod
  that gives up says so at once with `DELETE` (above) instead of letting the lease run
  out.
- A kept decision is held for 80 s (2 × the lease); after that, or after a daemon
  restart, a GET rebuilds it from the row. An `AskUserQuestion` answer is not stored
  in the row, so a GET that comes that late sees a plain `allow`. A healthy mod polls
  again within milliseconds.
- A daemon restart expires every pending row (`resolved_via = restart`), mod rows
  included; the mod's next GET reads `pass`/`expired`.

The mod should race each GET against a 28 s timer (`$.http.fetch` has no timeout),
check `next.signal.aborted` after every fetch (and `DELETE` the request when it is),
stop at `expiresAt`, and fall through to
`next(e)` on any network error or `5xx` (fail open to the shim, never to `deny`).

### Shim after mod

When the mod returns `next(e)` without a decision of its own, the engine may show its
permission dialog and fire the shim's `PermissionRequest` hook for the **same** call:
after a `pass` from `tool.check`, and on the `AskUserQuestion` path after an `allow`
that carries no answer (`tool.call` wraps the permission stage, `mods-format.md` M4).
The shim's stdin has no `tool_use_id`, so the daemon matches it by dedup hash:

- a **pending** row with that hash (the mod is still asking): the shim attaches to it
  and waits on the same row. One row, and one dashboard decision reaches both;
- a `channel = 'mod'` row with that hash that reached **any** terminal status in the
  **last 5 s** (`approvals.ShimAfterModWindow`): the shim's long-poll is answered at
  once **from that row** — no row and no event are written:

  | Mod row | Shim answer |
  |---|---|
  | `denied` | `200 {"decision":"deny","message":<the row's reason>}` |
  | `approved` with an answer's `updatedInput` (kept for the mod's next poll) | the allow + `updatedInput`, delivered per `serve --answer-delivery` exactly like a dashboard answer on a shim row |
  | `approved` without one, on an `AskUserQuestion` row | `204`: a plain allow would resolve the question with empty answers (`hooks-format.md` E12d), so the native dialog asks |
  | `approved` on any other tool | `204`: the mod already returned that allow itself, so a shim request with the same hash is a **fresh** identical call; replaying the allow would approve a call no person saw, so the native dialog asks |
  | `expired`, `resolved_elsewhere` | `204`: Claude Code shows its native dialog |

  Every one of these is a response the frozen shim protocol already allows;
- otherwise (no match, or the decision is older than 5 s): the shim opens its own row
  as always.

A dashboard deny of a question is not answered by the mod: a question has nothing to
refuse, so the mod hands the call on (`next(e)`). Where the shim is installed, the
engine's permission stage then fires it for the same call, and this window delivers
the operator's deny to it; without the shim, the question's native dialog opens.

**Parallel subagents.** The dedup hash carries no `agentId` (the shim cannot see one,
E11), so two subagents asking the identical call at the same moment share one row and
one decision, exactly as two shim requests do.

## Dedup

A link event's identity is `(session, kind, at)`: the `UNIQUE(session_id, kind, at)`
key of `link_events`, where `session_id` is the resolved `sessions.id` and `at` is the
normalized ms-UTC string. Normalization runs first, so `2026-10-13T12:00:00+02:00` and
`2026-10-13T10:00:00.000Z` are the same instant and the second one is a duplicate.

Consequences for the mod:

- **Resend freely.** A retry after a timeout or a `500` counts already-stored events
  as `duplicates`, so nothing is stored twice and state is never applied twice.
- **Keep `at` unique per session and kind.** Two distinct events of the same kind in
  the same millisecond (two parallel `tool.call`s, say) collide, and the second is
  counted as a duplicate. Use `$.clock.now()` at hook time, and bump by 1 ms when it
  repeats the previous event of that kind.
- Never rewrite `at` on a retry. A resend must carry the same `at` it had the first
  time.

## Binding to sessions

The event's `sessionId` is looked up as `sessions.session_uuid` (M1).

- **Row exists** (the JSONL tail or the hook shim got there first): the event binds
  to it. A `source = 'jsonl'` row becomes `'both'`.
- **No row yet** (the mod started before the transcript was tailed): the daemon mints
  a minimal row with `source = 'mod'`, `status = 'active'` and `started_at` = the
  earliest `at` in the batch for that session. The project comes from `cwd` through
  the same resolver every channel uses (`ingest.UpsertProject`). Terminal identity
  parked by an earlier `SessionStart` hook is applied to it. When the JSONL ingest
  later reaches the transcript, it fills the row in and promotes `'mod'` to `'both'`.
- Each batch publishes `session_started` (for a row it minted) or `session_updated`
  once per session touched, after commit, on the WS bus.

### Status

`sessions.status` keeps its own vocabulary. The mod's `link_status` is folded in by
the status ticker (`ingest.RecomputeStatuses`) while the mod is **fresh**, meaning
`link_seen_at` is younger than `2 × AwaitAfter` (6 min by default):

| `link_status` (fresh) | `sessions.status` |
|---|---|
| `working` | `active` |
| `waiting` | `awaiting_reply` (interactive sessions). A headless run (`entrypoint = 'sdk-cli'`) keeps the timer's status |
| `done` | `completed` |
| NULL (only `tool.call` / `session.measure` seen so far) | the timer's `active`/`idle`/`completed`, never the timer's `awaiting_reply` |

A fresh row is never moved to `awaiting_reply` by the quiet-time timer, because the
mod reports it live. A stale or NULL `link_seen_at` (the mod went away, or the session
never had one) falls back to the timer path unchanged. The ticker only considers rows
still `active`/`idle`/`awaiting_reply`. It never reopens a `completed` row. Reopening
is the ingest path's job.

## Excluded projects

Events whose `cwd` matches the daemon's exclude list (`--exclude-projects`,
`SWARMERY_EXCLUDE`, default `/tmp/*,/private/tmp/*`, the same list the JSONL ingest
and the approvals hook honour) are rejected before anything is stored, with reason
`excluded project`. No session, project or event row is written for them. Unlike the
approvals hook, which serves an excluded cwd when a session row already exists, the
link filters by `cwd` alone. A mod in a throwaway directory never writes anything.

## Retention

`link_events` is a dedup ledger, not history. The prune pass (`swarmery prune`, and
the daemon's daily tick) drops rows whose `at` is older than the retention cutoff,
the same horizon as `events`. It goes by the row's own `at`, not through the session
candidate set, because a `mod`-minted session may never get an `ended_at`. The
`sessions.link_*` columns keep the folded state. A resend of an event older than the
cutoff would be accepted again, which is harmless: its state is older than what the
columns already hold.

`link_commands` ages on the same cutoff by its own `created_at`. Every command has
expired or finished long before that.

## Degradation

The link is an **optional sensor**. Nothing else depends on it:

- **Daemon down** (connection refused or timeout): the mod drops the event, or keeps a
  small bounded in-memory queue and flushes it on the next successful request. It
  never blocks a hook on the daemon and never fails a hook because of it. The session
  is still recorded from its JSONL transcript once the daemon is back.
- **Token missing or unreadable**: the mod treats the link as off for that session.
- **Daemon without a token** (`503 link disabled`): same.
- **No mod installed**: every `sessions.link_*` column stays NULL, `link_events` stays
  empty, and statuses come from the timer exactly as before this feature.
- **The shim and the JSONL tail never need `/api/link/*`.** `/api/hooks/*`, the
  ingest pipeline and every dashboard read keep working with the link off, and
  nothing in this contract changes them.

## Version fragility

The mod API is pinned to what `2.1.296` showed (M1 session id parity, M2 measure
shape). If a future Claude Code changes `$.session.id()` or the `session.measure`
shape, the mod adapts its payload. This contract's field names stay fixed. Re-run the
spike in [`mods-format.md`](mods-format.md) before a mod release that targets a new
Claude Code minor.

## Forecast

**Status:** phase 10 contract (mods-swarmery-link, the turn-forecast loop). Additive:
two event kinds on the same `POST /api/link/events` envelope, with the same auth,
the same dedup and the same per-event reject path. Neither kind touches
`link_status`. Implementation: `internal/link/forecast.go` (ingest),
`internal/turnforecast` (validation, scoring, storage), `internal/api/link_forecast.go`
(read side), migration `0113_turn_forecasts.sql`.

The mod sends it only with `options.forecast === true`. On a main-loop `turn.start`
that carries user-typed text, it asks a cheap model what the turn will do. On the
matching `turn.complete` it reports what its own `tool.call` journal saw. The daemon
compares the two deterministically. No LLM runs on the daemon side.

### Event kinds

| `kind` | Sent on | `payload` |
|---|---|---|
| `turn.forecast` | the forecast call resolving (after `turn.start`, never blocking it; it may arrive after the turn completed) | `{ "runId": string, "turnSeq": int ≥ 1, "files": [string], "testsPass": bool \| null, "steps": int ≥ 0 \| null, "confidence": 0..1 \| null, "model": string, "forecastTokens": int ≥ 0 \| null }` |
| `turn.actual` | main-loop `turn.complete` | `{ "runId": string, "turnSeq": int ≥ 1, "files": [string], "testsRan": bool, "testsPass": bool \| null, "steps": int ≥ 0 }` |

| Field | Rule |
|---|---|
| `runId` | required on both: 1 to 64 characters of `[A-Za-z0-9._:-]`. Names one run of the mod's turn counter. The mod mints a new one each time it restarts the counter: at `session.start`, at `session.end` with reason `clear`, and at the `turn.start` resync after `session.end` with reason `resume`. The mod's form is `<ms epoch of the reset>-<6 random base36 chars>`, or the ms alone where the runtime has no random source; the daemon checks only the shape. The two halves of a turn carry the same value |
| `turnSeq` | required on both. The mod's counter of main-loop `turn.start`s within `runId`, starting at 1 at each new run. The two halves of a turn carry the same value |
| `files` (forecast) | at most 50 entries, each at most 300 characters. Paths relative to the session cwd where possible. Absent means `[]` |
| `files` (actual) | files touched by `Edit`/`Write`/`MultiEdit`/`NotebookEdit` calls of the main loop this turn (no `agentId`), relative to the cwd when under it |
| `testsPass` (forecast) | `null` means the model made no claim |
| `testsRan` / `testsPass` (actual) | `testsRan` is required. `testsPass` is `null` when the mod could not observe the outcome |
| `steps` | forecast: the predicted number of main-loop tool calls, or `null`. Actual: required, the number of main-loop `tool.call` events this turn |
| `confidence` | `null` or within 0..1 |
| `model` | at most 80 characters. The model the mod asked |
| `forecastTokens` | input + output tokens of the forecast call, when the reply reported them |

Unknown fields are ignored. Any other violation (a wrong type, a missing required
field, a value out of range, a payload that is absent or not an object) rejects
**that one event** with reason `turn.forecast payload: <why>` or
`turn.actual payload: <why>`, inside the usual `202`. It is never a `500`.

**Privacy.** Paths and numbers only. No prompt text, no tool input and no model
reply text ever go on the wire, and the daemon stores the payloads verbatim, so the
rule is the mod's to keep.

### Storage and scoring

The halves land in `turn_forecasts`, one row per `(session, runId, turnSeq)`
(`UNIQUE(session_id, run_id, turn_seq)`). `runId` is in the key because
`turnSeq` alone is not unique within a session: `session.start` fires again on
`--resume` with the same session id, the mod counts from 1 again, and an
in-process `/resume` can land on a session that already has turns. Keyed on
`(session, turnSeq)`, a resumed run's turn 1 would overwrite the first run's turn
1 and rescore a stale pair (a headless `-p --resume` would put every message on
row 1). With `runId`, a new run is new rows, and the old rows keep their halves
and their score. `started_at` is the forecast's `at`, else the
actual's. A half row is legal and stays unscored: `surprise` and `vector` are `NULL`
until both halves are present. When the second half arrives, the row is scored in the
same transaction. A half that is re-sent (same `runId` and `turnSeq`) with a new
`at` replaces the stored half and the row is rescored. A re-send with the same `at` is a link duplicate and changes
nothing.

The score mirrors the phase-level `internal/surprise`. Each component is a number
in 0..1, or `null` when it cannot be measured:

| Component | Value | `null` when |
|---|---|---|
| `unexpected_files` | share of the actual files the forecast did not name | the actual has no files |
| `missed_files` | share of the forecast files the turn never touched | the forecast named no files |
| `tests_miss` | `1` when the forecast's `testsPass` differs from the observed one, `0` when equal | the forecast made no claim, tests did not run, or the outcome was not observable |
| `steps_miss` | `\|forecast − actual\| / max(forecast, actual, 1)` | the forecast gave no `steps` |
| `overconfidence` | the forecast's `confidence` when any other component is ≥ 0.5 (a major miss), else `0` | the forecast gave no `confidence` |

Paths are normalized before comparison (a `./` prefix, duplicate separators and a
trailing slash are removed). The index is the weighted sum of the measurable
components, clipped to 0..1 and rounded to 4 decimals. The weights are
`unexpected_files .3`, `missed_files .2`, `tests_miss .25`, `steps_miss .15` and
`overconfidence .1`. `top` is the largest weighted contributor, `""` at index 0. A
turn where nothing is measurable stays unscored. "Could not tell" is never "scored
zero". The `vector` column stores
`{index, top, majorMiss, components, weights}` as JSON with sorted keys, so the same
two halves always produce byte-identical output.

When a row gets or changes its score, the daemon publishes `turn_surprise` on the
WS bus after commit (see [`ws-protocol.md`](ws-protocol.md)).

### GET /api/insights/turn-forecasts

`?projectId=<int>&days=<1..90>` (default 14; a value out of range is clamped and an
unparseable one falls back to 14). This is a read-only GET like the other analytics
reads: it is not behind the origin fence, but the Host fence applies. A `projectId`
that is not an integer gets `400`. The example below shortens `bins` and `top`:

```json
{ "days": 14, "n": 13, "tooFew": true,
  "bins": [ { "lo": 0, "hi": 0.2, "n": 2, "meanConfidence": 0.115, "hitRate": 0 },
            { "lo": 0.2, "hi": 0.4, "n": 0, "meanConfidence": null, "hitRate": null } ],
  "top": [ { "id": 12, "sessionId": 1, "sessionUuid": "efaa9b56-…", "projectId": 1,
             "runId": "1791885600000-25r6dv", "turnSeq": 12, "startedAt": "2026-10-14T08:44:30.794Z",
             "surprise": 0.7795, "top": "tests_miss", "model": "haiku" } ],
  "avgForecastTokens": 115.3846 }
```

- `bins` are the forecast confidence buckets `[0,.2) [.2,.4) [.4,.6) [.6,.8) [.8,1]`,
  always five. They cover the window's scored turns whose forecast carried a
  confidence. A hit is `surprise < 0.25`. An empty bin has `null` mean and hit rate.
- `n` is the number of turns in the bins. `tooFew` is `n < 30`: below that the
  curve is noise, and the panel says so.
- `top` lists the 10 most surprising scored turns in the window, highest first
  (ties go to the newest). `sessionUuid` and `projectId` are `null` when the session
  row is gone. `runId` with `turnSeq` names the turn within its session.
- `avgForecastTokens` is the mean `forecastTokens` over every turn in the window that
  reported one, scored or not. It is `null` when none did.

The output is advisory. There are no thresholds and no alerts.

### Retention

`turn_forecasts` ages on the same cutoff as `link_events`, by its own `started_at`
(`internal/prune`, `Stats.TurnForecasts`).

TODO: `internal/lessons` could take `turn_surprise` as a future lesson source, beside
`phase_surprise`. That has not been built. It needs calibrated data first, which means
real turns, not a threshold picked in advance.

## Swarm

**Status:** phase 6 contract (mods-swarmery-link, swarm messaging and the file-claims
registry). Additive: five event kinds on the same `POST /api/link/events` envelope,
with the same auth, the same dedup and the same per-event reject path; one command
kind; three routes. None of it touches `link_status`. Implementation:
`internal/link/swarm.go` (ingest, correlation, claims), `internal/link/commands.go`
(the `message` command), `internal/api/link_swarm.go` (routes), migration
`0116_link_swarm.sql`.

### Event kinds

Every one is also stored in `link_events`, like every other kind (that table is the
dedup ledger), so a resent event is a `duplicate` and is applied once. Only a newly
stored event writes the side tables below.

| `kind` | The event's `sessionId` is | `payload` |
|---|---|---|
| `session.send` | the **sender** | `{ "to": string, "text": string, "truncated"?: bool, "isDelivered"?: bool, "reason"?: string, "origin"?: "model" \| "plugin", "commandId"?: int }` |
| `session.receive` | the **receiver** | `{ "text": string, "truncated"?: bool, "originKind"?: string, "fromPlugin"?: string }` |
| `claim` | the session that started editing | `{ "path": string }` |
| `release` | the session whose turn (or session) ended | `{ "paths": [string] }` or `{ "all": true }` |
| `conflict` | the session that saw the clash | `{ "path": string, "otherSession": string }` |

| Field | Rule |
|---|---|
| `to` | required, 1 to 512 characters: the recipient as the engine spells it (a session uuid, an agent id or a name) |
| `text` | required on both message kinds (may be empty). At most 4096 characters are stored; a longer text is cut and stored with `truncated = 1`. `truncated: true` from the mod is kept |
| `isDelivered` | the engine's `SessionSendResult.isDelivered`. Absent means "unknown" (`delivered` NULL) |
| `reason` | stored only when `isDelivered` is `false` |
| `origin` | `model` (the model called the tool) or `plugin` (a mod called `$.session.send`). Anything else rejects the event |
| `commandId` | a positive integer, set when the send ran a dashboard `message` command (below). Send it as a JSON number (the swarmery-link mod does). A string of decimal digits (`"30"`, what mods before the phase-6 fix sent) is accepted as the same id; zero, a negative, a fraction, an exponent or any other text rejects the event |
| `originKind` | the receive's `e.origin.kind` (`peer`, `coordinator`, `bridge`, …), at most 64 characters. Stored as the row's `origin` |
| `fromPlugin` | accepted and kept in `link_events` only; there is no column for it |
| `path`, `paths[]` | absolute (else the event is rejected), cleaned (`/repo/./a.go` → `/repo/a.go`). At most 500 paths per release: a turn that claimed more ends in `{ "all": true }` instead (the swarmery-link mod does) |
| `otherSession` | required. A `conflict` is an advisory record kept in `link_events` only; the daemon's own conflict detection (below) is what the dashboard shows |

Unknown fields are ignored. A violation (wrong type, missing required field, relative
path, a payload that is absent or not an object) rejects **that one event** with
reason `<kind> payload: <why>`, inside the usual `202`.

**Privacy.** `text` is message text the user's sessions exchanged, which the
transcripts hold anyway. The mod cuts it at 4096 characters; the daemon cuts again.

### Correlation

The engine does not tell the receiver who sent a message, so the daemon pairs the two
sides. On a stored `session.receive` it looks for the most recent unpaired `send` (or
dashboard) row:

1. addressed to the receiver: `to` equals the receiver's session uuid;
2. with the same text (sha-256 of the stored body);
3. whose `at` is within **120 s** of the receive's `at`, either side;
4. not sent by the receiver itself.

The match sets the send's `delivered = 1` and the receive's `from_session` to the
sender. No match leaves `from_session` NULL. When no uuid-addressed send matches, a
send whose address resolved to no session (a name or an agent id) with the same text
in the window is taken instead — only one sent from a session in the **receiver's
project**: a name never pairs across projects. Batches from different sessions arrive
in any order, so a `send` stored after its `receive` pairs the same way (a
name-addressed send then pairs only with a receive in the sender's project, or one
whose session uuid is the address itself). Each row pairs at most once (`matched_id`
names the other side).

### Claims

`claim` upserts `link_claims(session, path)`: a new row, or a re-claim that clears
`released_at` and refreshes `claimed_at`. A `release` stamps `released_at` on the
session's live claims (the listed paths, or all of them) that were claimed at or before
the release's `at`. A claim whose `at` is older than the row's release (a late or
reordered event) changes nothing, so the mod does not need to sort.

A claim is **live** while `released_at` is NULL and `claimed_at` is younger than
**30 min**; an older unreleased claim is treated as left behind by a crashed session.
A **conflict** is a path two or more sessions hold a live claim on. Conflicts are
**advisory**: nothing is blocked, and the mod's own `conflict` events are only a
record.

### The `message` command

The dashboard relays a message by queueing a `message` command for the **sending**
session, because `$.session.send` always sends from the session that calls it.

```json
{"id": 9, "kind": "message", "payload": {"to": "<recipient session uuid>", "text": "rebase on main"}}
```

It follows the prompt lifecycle (queued, delivered, re-delivery after 30 s, expiry
after 10 min) without the `turn in progress` bounce: a send needs no idle session, so
every `failed` ack is final. The mod acks with the usual body:

| Outcome | Ack |
|---|---|
| `isDelivered` is `true` | `{"result":"done"}` |
| otherwise | `{"result":"failed","message":"undeliverable: <engine reason>"}` |

The mod also reports the send as a `session.send` event with `commandId`; it folds into
the command's dashboard row (address, truncation, origin) instead of adding a second
row. The row's `delivered` is the ack's to set: `done` → `1`, `failed` → `0` with the
ack message as `reason`, expired → `0` with reason `expired`. The command's final
state and its row's `delivered` are written in one transaction: an ack (or expiry)
that fails leaves both as they were, and the mod's re-delivered ack settles both.

One exception: a send that settles **after** the ack. The mod gives a send 10 s; past
that it acks `failed` with `undeliverable: no answer in 10s`, but keeps the send's
command id and reports the send, with that `commandId`, once it settles. A
`session.send` folding into a row the ack already settled overrides it with the
engine's verdict: `isDelivered: true` → `delivered = 1`, reason cleared;
`isDelivered: false` → `delivered = 0`, reason `undeliverable: <engine reason>`. The
command itself stays `failed`.

### Routes

| Route | Fence | Answers |
|---|---|---|
| `POST /api/sessions/{id}/link/message` | `requireLocalOrigin`. `{id}` is the **sender's** `sessions.id` | body `{"to": <recipient sessions.id \| session uuid>, "text": string}`. `202 {"commandId": n, "messageId": m}`. `400` bad id or body, empty text, text over 4096 characters, unknown recipient, recipient = sender. `404` unknown sender. `409 {"error":"session has no link"}` sender not controllable. `503` control channel detached |
| `GET /api/swarm/messages?projectId=&since=&limit=` | read-only, no origin fence | `200 [Message]`, newest first. `projectId` absent = every project, else a message is listed when either side's session is in it. `since` is RFC 3339 (lower bound on `at`). `limit` 1–500, default 100, above 500 clamped. `400` for a bad `projectId`, `since` or `limit` |
| `GET /api/swarm/claims?projectId=` | read-only, no origin fence | `200 {"claims": [Claim], "conflicts": [Conflict]}`. With `projectId`, claims of that project's sessions, and conflicts any of whose holders is in it (every holder listed) |

```ts
type SessionRef = { id: number; uuid: string; title: string | null }; // title as the sessions list shows it
type Message = {
  id: number; kind: 'send' | 'receive' | 'dashboard';
  fromSession: SessionRef | null;   // null: receive not correlated, or the session row is gone
  toSession: SessionRef | null;     // null: the address is no session of ours
  toAddress: string | null;         // as the engine spelled it; null on a receive
  body: string; truncated: boolean; origin: string | null;
  at: string;                       // fixed-width ms UTC
  delivered: boolean | null;        // null = unknown
  reason: string | null;
};
type Claim = { sessionId: number; sessionUuid: string; path: string; claimedAt: string };
type Conflict = { path: string; sessions: { sessionId: number; sessionUuid: string }[] };
```

### Live updates

`swarm_message` and `swarm_claim` WS frames ([`ws-protocol.md`](ws-protocol.md)) are
thin hints, published after the batch commits; the client refetches the reads above.

### Storage and retention

`link_messages(id, kind, from_session, to_session, to_address, body, truncated, origin,
at, delivered, reason, command_id, matched_id)` and `link_claims(session_id, path,
claimed_at, released_at)`, primary key `(session_id, path)`. Session columns hold
Claude session ids (`sessions.session_uuid`). There are no foreign keys, for the reason
`0107` gives. `link_messages` ages on the `link_events` cutoff by its own `at`
(`Stats.LinkMessages`); `link_claims` rows go 24 h after their release, and an
unreleased claim 24 h after it was claimed (`Stats.LinkClaims`).

## Plan

**Status:** phase 7 contract (mods-swarmery-link, plan in the terminal). The mod shows
the session's plan phase and its acceptance criteria, ticks a criterion, picks a plan,
starts the next phase run and reports drift. **The daemon owns plan state and the one
markdown parser** (decision D5). The mod never reads or parses a plan doc: it draws the
view below and sends indexes back. Implementation: `internal/api/link_plan.go`,
`internal/link/drift.go`, migration `0117_session_plan.sql`.

Every `/api/link/plan*` route sits behind the bearer token like `/api/link/events`
(`401` without it, `503 {"error":"link disabled"}` with the link off). There is no
origin fence, and the Host fence applies. POST bodies are JSON, at most 16 KiB (`413`
above). A body that is not JSON is `400`. `taskId` and `phaseId` may be sent as a string
or a number.

**Which plan a `taskId` names.** `tasks.external_id` is unique per workspace only, so
two projects can carry the same `yyyy-mm-dd-slug`. Every lookup is therefore scoped to
the session's project, found the way `GET` finds it (the session's `sessions` row,
else the project owning `cwd`); `select`, `tick` and `run` take an optional `cwd` for
a session the daemon has not indexed yet. A body with a `phaseId` resolves the plan
through that phase's row (`epic_phases.id` is unique) and requires `taskId` to name
the same plan (its external id or numeric id), else `404 phase not found`. Only when
neither the session nor `cwd` names a project is the lookup unscoped, and then it is
deterministic: an unarchived plan first, then the newest task.

### The view

`GET /api/link/plan`, every `200` of the POSTs below, has this shape:

```json
{ "taskId": "2026-10-01-alpha", "title": "Alpha plan",
  "url": "http://127.0.0.1:7777/p/proj/plans/2026-10-01-alpha/phase/2",
  "chosenBy": "auto",
  "phase": { "id": "12", "n": 2, "title": "API", "status": "In progress", "done": 2, "total": 4,
             "criteria": [ { "idx": 0, "text": "route registered", "done": true, "class": "" },
                           { "idx": 1, "text": "tick flips the doc", "done": false, "class": "" },
                           { "idx": 2, "text": "push the branch", "done": false, "class": "land" },
                           { "idx": 3, "text": "check the console", "done": true, "class": "manual" } ] },
  "next": { "id": "13", "n": 3, "title": "UI" } }
```

| Field | Meaning |
|---|---|
| `taskId` | the plan's canonical task id (`tasks.external_id`, `yyyy-mm-dd-slug`). A plan without one carries its numeric task id as a string. Every POST below takes it back as is |
| `title` | the plan's title |
| `url` | the daemon's own origin (the request's `Host`) plus the Plans page path of this phase: `/p/<project slug>/plans/<taskId>/phase/<n>` |
| `chosenBy` | `terminal` when the operator picked the plan with `select`, `auto` when the daemon resolved it |
| `phase.id` | the phase id as the epics API names it (`epic_phases.id`, as a string) |
| `phase.n` | the phase number (its `seq`) |
| `phase.title` | the phase name with a leading `Phase N — ` removed |
| `phase.status` | `Pending`, `In progress`, `Blocked`, `Done`, `Unverified` or `Unreported`, derived the way the Plans page derives its phase chip: the completion gate (`internal/phasegate`), then the doc's `Status: In progress`, a running phase run, unfinished dependencies, any ticked criterion |
| `phase.done` / `phase.total` | ticked / all criteria, counted from the doc on disk at request time |
| `phase.criteria[]` | every acceptance criterion in document order. `text` drops the checkbox and the class marker. `class` is `land` for `[LAND]`, `manual` for `[MANUAL]`, `""` otherwise, ticked or not |
| `next` | the first later phase (in sequencing order) that is not `Done`, or `null` |

**The `idx` contract.** `idx` is the criterion's 0-based ordinal among the doc's
criteria, which are every `- [ ]` / `- [x]` line outside a fenced code block
(`wsingest.ListCriteria`, the same walker the checkbox counts use). A checkbox quoted
inside a fenced agent prompt is not a criterion and has no `idx`. `tick` resolves `idx`
over the same walker on the bytes it is about to write. After any doc change, the next
`GET` renumbers, so a tick also carries the criterion's `text` as the mod drew it: if
the doc's `idx`-th criterion no longer reads so (a line inserted above it, the
criterion reworded or removed), the tick is refused `409` and nothing is written. The
mod re-reads the view and the person ticks again.

`done`, `total` and `criteria` come from the file, so a tick shows on the next `GET`
at once. `status` and `next` come from the indexed phase rows, which the workspace
scanner refreshes within about a second of a doc change.

### GET /api/link/plan

```
GET /api/link/plan?session=<Claude session id>&cwd=<absolute path>
```

At least one parameter is required (`400` otherwise). Resolution, first match wins:

1. **The session's pick.** A `session_plan` row for `session` with `chosen_by =
   'terminal'`: that plan, at its stored phase, else at the plan's active phase. A
   stored phase pins the plan through its row; without one (or with a phase that is
   gone) the stored `task_id` is looked up within the session's project. A pick whose
   plan is no longer indexed falls through to step 2.
2. **The project's active plan.** The project is the session's (`sessions.session_uuid`
   → `project_id`). If the daemon has no row for the session yet, or the row sits on
   the `(unknown)` project, the project is the registered project whose path is `cwd`
   or its closest registered ancestor (`ingest.CanonicalProjectPath`, the resolver
   attribution uses). Among that project's non-archived plans (the newest 50), the one
   with a phase `In progress` wins. If several have one, the one whose phase doc changed
   last wins. If none has one, the newest plan the epics list calls `active` wins.

The phase is the pick's phase, else the first `In progress` phase, else the first that
is not `Done`, else the last. A step-2 answer is recorded as a `session_plan` row with
`chosen_by = 'auto'` (this feeds the SPA list below). It never overwrites a `terminal`
row.

| Code | When |
|---|---|
| `200` | the view |
| `204` | no plan: no project for this session or `cwd`, or a project with no plan to show |
| `400` | neither `session` nor `cwd` |

### POST /api/link/plan/select

```json
{ "session": "<uuid>", "cwd": "/work/proj", "taskId": "2026-10-01-alpha", "phaseId": "12" }
```

`cwd` and `phaseId` are optional. Pins the plan, and the phase when `phaseId` is given, to the session (`chosen_by =
'terminal'`, replacing any earlier pick). Without `phaseId` the view follows the plan's
active phase. Answers `200` with the view, `400` for a missing `session` or `taskId`,
`404` for an unknown plan or a phase that is not that plan's.

### POST /api/link/plan/tick

```json
{ "session": "<uuid>", "cwd": "/work/proj", "taskId": "2026-10-01-alpha", "phaseId": "12",
  "idx": 1, "text": "tick flips the doc", "done": true }
```

Flips criterion `idx` of that phase's doc to `done`. `text` (optional) is the
criterion's text as the view gave it: when it is sent and the doc's `idx`-th criterion
no longer reads so (compared with surrounding whitespace trimmed), or the doc has no
criterion at `idx` any more, the tick is refused `409 {"error":"criterion changed"}`
and nothing is written. Without `text` the tick is positional, as it always was. It runs through the same code as
the Criteria tab's `PATCH /api/epics/{taskId}/docs {line, done}`: the doc stays
confined to the plan directory, a timestamped backup lands in `plan/.backups/`, and a
tick that closes the doc's last open criterion hands the phase to the plan branch
review. The daemon then publishes `plan_updated` on the WS bus (the scanner publishes it
again when it re-indexes the doc).

| Code | When |
|---|---|
| `200` | the view of that phase, after the write |
| `400` | `idx` or `done` missing, `idx` negative, `taskId` or `phaseId` missing |
| `404` | unknown plan or phase, no plan directory, doc missing, or (without `text`) no criterion at `idx` |
| `409` | `text` sent and the criterion at `idx` no longer matches it, or is gone: `{"error":"criterion changed"}` |

The `text` check is the version check: a tick names a criterion by position in the doc
as it is now, the same way `PATCH` names a line, and the text proves it is still the
criterion the mod drew.

### POST /api/link/plan/run

```json
{ "session": "<uuid>", "cwd": "/work/proj", "taskId": "2026-10-01-alpha", "phaseId": "13" }
```

Starts a headless run of that phase through the same code as the Plans page's
`POST /api/epics/{taskId}/phases/{phaseId}/run` with no options (the doc's `**Model:**`,
then the daemon default). The status and body are that route's: `202 {"status":
"running", "sessionUuid": …}`, or a `409` with a `code` (`deps-unmet`, `already-running`,
`manual-only`, `doc-model-unknown`, …), or `429` for an account below its quota floor.
It adds `404` for an unknown plan or phase, `400` for a missing field, and
`503 {"error":"phase runs not attached"}` on a daemon without phase runs.

### POST /api/link/plan/drift

```json
{ "session": "<uuid>", "taskId": "2026-10-01-alpha", "phaseId": "12",
  "verdict": "off-phase", "reason": "editing the billing module" }
```

The mod's judgement of whether the session's recent work matches its open phase.
`verdict` is `off-phase`, `adjacent` or `on-phase`. `reason` is cut to 200 characters.
The daemon stores it as a `link_events` row of kind `drift` for that session, with the
body minus `session` as the payload, and answers `202 {"stored": true}`. A second report
in the same millisecond is a duplicate (`"stored": false`). It touches no `link_status`
and never runs through `POST /api/link/events`, which rejects the kind.

| Code | When |
|---|---|
| `202` | stored (or a same-millisecond duplicate) |
| `400` | missing `session`, or a `verdict` outside the set |
| `404` | no `sessions` row carries this session id |

### GET /api/epics/{taskId}/terminal-sessions

A SPA read (no token, no origin fence, like `GET /api/epics`). `{taskId}` is the
numeric task id the epics API uses. It answers which sessions have this plan open in a
terminal, newest first:

```json
[ { "sessionId": 41, "sessionUuid": "efaa9b56-…", "phaseId": "12", "chosenBy": "terminal",
    "at": "2026-10-14T09:12:03.120Z", "live": true } ]
```

The list holds every `terminal` pick of this plan, plus every session whose `GET`
resolved to it automatically in the last 10 minutes. A `session_plan` row belongs to
this plan when its `task_id` is the plan's numeric id, or its external id and either
its `phase_id` is one of this plan's phases or its session is in this plan's project
(and its `phase_id` is not another plan's). Another project's plan with the same
external id never lends its sessions. `phaseId` is `null` for a pick of
the plan without a phase. `live` is the session's link freshness (`ingest.LinkFresh`,
the same answer as the session DTO's `link.live`). A row whose session has no `sessions`
row yet is left out. `[]` when none, `404` for an unknown task, `400` for a non-numeric
id.

### Storage and retention

Table `session_plan` (migration `0117`): `session_id` (the Claude session id, primary
key), `task_id`, `phase_id` (nullable), `chosen_by` (`terminal` | `auto`), `at`
(fixed-width ms UTC), indexed on `task_id`. There is no foreign key, for the reason
`0107` gives. The prune pass drops a row once its `at` is older than the `link_events`
cutoff **and** its session has ended (`ended_at` set, the mod reported `session.end`,
or no `sessions` row is left), `Stats.SessionPlans`. A long-lived terminal keeps its
pick however old it is. Drift reports age with the rest of `link_events`.
