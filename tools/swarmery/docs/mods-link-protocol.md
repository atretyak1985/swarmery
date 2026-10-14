# Link HTTP protocol — daemon ↔ swarmery link mod

**Status:** phase 1 contract (mods-swarmery-link). This document defines the HTTP
contract between the daemon and the swarmery link, a Claude Code mod (a plugin
`hooks/register.tsx` module) that reports live session state to the daemon. The mod
side is built against this text in phase 2. Changes are additive. A breaking change
needs a new route, not an edit to this one.

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
`0109_permission_channel.sql`.

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

## POST /api/link/events

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
M3). Both routes sit behind the bearer token like the rest of `/api/link/*`: no origin
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
| `channel` | `shim` or `mod`: which hook channel delivered the row (migration `0109`). The `PermissionRequest` DTO and the needs-you item carry it as `channel` |
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
  shim's client-disconnect leaves. A dashboard decision on such a row is `410`.
- A kept decision is held for 80 s (2 × the lease); after that, or after a daemon
  restart, a GET rebuilds it from the row. An `AskUserQuestion` answer is not stored
  in the row, so a GET that comes that late sees a plain `allow`. A healthy mod polls
  again within milliseconds.
- A daemon restart expires every pending row (`resolved_via = restart`), mod rows
  included; the mod's next GET reads `pass`/`expired`.

The mod should race each GET against a 28 s timer (`$.http.fetch` has no timeout),
check `next.signal.aborted` after every fetch, stop at `expiresAt`, and fall through to
`next(e)` on any network error or `5xx` (fail open to the shim, never to `deny`).

### Shim after mod

When the mod returns `next(e)` after a `pass`, the engine may show its permission
dialog and fire the shim's `PermissionRequest` hook for the **same** call. The shim's
stdin has no `tool_use_id`, so the daemon matches it by dedup hash:

- a **pending** row with that hash (the mod is still asking): the shim attaches to it
  and waits on the same row. One row, and one dashboard decision reaches both;
- a `channel = 'mod'` row with that hash resolved `expired` or `resolved_elsewhere`
  in the **last 5 s** (`approvals.ShimAfterModWindow`): the shim's long-poll answers
  `204` at once, no row and no event are written, and Claude Code shows its native
  dialog. `204` is a response the frozen shim protocol already allows;
- otherwise (no match, or the hand-back is older than 5 s): the shim opens its own
  row as always.

An approved or denied mod row never triggers the window: the engine runs the tool or
reports the denial, and no shim request follows.

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
