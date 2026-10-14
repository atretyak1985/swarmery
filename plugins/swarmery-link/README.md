# swarmery-link

A **mod** (a plugin of function hooks) that reports a Claude Code session's live
state to a local swarmery daemon, so the dashboard sees a session's status,
context fill, rate limits and cost the moment they change instead of waiting for
the transcript tail.

By default it is a **sensor only**. It never blocks, rewrites or answers anything:
every hook passes its event on unchanged, and a failure on the link is logged and
swallowed.

Version 0.2 adds an opt-in **command channel** (`control`, off by default): the
dashboard's Stop and Send-prompt buttons reach the session through the daemon.
See [Control](#control).

Version 0.3 adds an opt-in **turn forecast** (`forecast`, off by default): one
small model call per turn predicts what the turn will touch, the turn's own tool
calls make the actual, and the daemon scores how surprised the forecast was.
See [Forecast](#forecast).

## Opt-in

Nothing is installed by default. Enable it per project in `.claude/settings.json`:

```json
{ "enabledPlugins": { "swarmery-link@swarmery": true } }
```

or try it for one session from a checkout:

```bash
claude --plugin-dir plugins/swarmery-link
```

It also needs the daemon's link token (below). Without it the mod does nothing.

## What it does

| Hook | Sends to `POST /api/link/events` |
|---|---|
| `session.start` | reads the token; `session.start` with `{ surface, isInteractive }` |
| `turn.start` | `turn.start` |
| `tool.call` | `tool.call` with `{ tool, inputHash, agentId? }`, then lets the call run |
| `session.measure` | `session.measure`: `context`, `rateLimits`, `cost`, `changed` |
| `turn.complete` (main loop only) | `turn.complete` with `{ durationMs, stopReason }`, plus a `session.measure` snapshot from `$.session.usage()` |
| `session.end` | `session.end` with `{ reason }`, and a last flush that waits at most 1.2 s, even behind a request already in flight |

- **`/clear` starts a new session in the same process.** The engine ends the old
  one (`session.end` with `reason: "clear"`) and goes on under a new session id
  without a `session.start`. The mod sends that `session.end` under the old id,
  queues nothing more, and at the next `turn.start` reads the id again: if it
  moved, it sends a `session.start` for the new id (the same
  `{ surface, isInteractive }` the real one carried) and reports everything
  after under it. A `session.measure` between the clear and that turn is not
  reported; the turn's `turn.complete` sends a fresh snapshot. A resume inside
  the same process (`reason: "resume"`) is followed the same way. Any other
  `session.end` reason is final.

- **No tool input leaves the session.** A `tool.call` carries the tool's name and
  the first 16 hex digits of a SHA-256 over the input's canonical JSON (FNV-1a
  where SHA-256 is unavailable). The input itself is already in the transcript.
- **Batching.** Events are queued and sent every 2 s, right away at a turn
  boundary, at most 200 per request and under 1 MiB. Two events of the same kind
  in the same millisecond are stamped 1 ms apart, so the daemon's
  `(session, kind, at)` dedup never swallows one. A resend carries the same `at`.
- **Units not reported are left out**, never zeroed: an empty `rateLimits` or a
  missing `context.percent` keeps the daemon's last value.

The wire format is the daemon's link protocol:
`tools/swarmery/docs/mods-link-protocol.md`.

## What it calls

As `claude plugin validate --strict --json plugins/swarmery-link` reports it:

```
./register.ts hooks: session.start, turn.start, tool.call, session.measure, turn.complete, session.end
./register.ts gating hook with .catch: tool.call
./register.ts calls: $.clock.after (via arm, beginForecast, pause, send, startControl, within), $.clock.now (via poll, push), $.env.get (via readToken), $.fs.read (via readToken), $.http.fetch (via fetchCommand, post, sendAck), $.model.complete (via runForecast), $.prompt.submit (via submitPrompt), $.session.cwd, $.session.id, $.session.usage, $.turn.abort (via abortTurn), $.ui.log (via log)
./register.ts env writes: nothing
./register.ts env reads: HOME
```

`tool.call` is listed as a gating hook because of where it sits, not what it
does: it always calls `next(e)`, and its `.catch` does the same. With `forecast`
on it waits for the call's result to note how a main-loop call ended, and
returns that result unchanged.

`$.turn.abort` and `$.prompt.submit` are the command channel's. Validate lists
them whatever the options say; with `control` off they are never called and no
request goes to the command endpoints. `$.model.complete` is the forecast's;
with `forecast` off it is never called.

## The token file

`~/.swarmery/link-token`, mode `0600`, minted by the daemon on start (or by
`swarmery link token`). The mod reads `${HOME}/.swarmery/link-token` on every
`session.start` and sends it as `Authorization: Bearer <token>`.

- **No file**: one line, `swarmery-link: no token, sensor off`, and no request for
  the rest of the session.
- **401**: the mod re-reads the file once and retries once, no sooner than 1 s
  later (past the daemon's re-read throttle). A second 401 turns the link off for
  the session.
- **503 `link disabled`**: the link is off for the session.

## Options

| Option | Default | Effect in 0.3 |
|---|---|---|
| `daemonUrl` | `http://127.0.0.1:7777` | the daemon's origin. Only `http://` on `127.0.0.1`, `[::1]` or `localhost` (any port, path ignored) is accepted; anything else is refused with one log line and the default is used, so the token never leaves the machine |
| `control` | `false` | the command channel: the dashboard can stop the running turn and send prompts ([Control](#control)) |
| `forecast` | `false` | the turn forecast: one haiku call per user turn, a `turn.forecast` and a `turn.actual` per forecast turn ([Forecast](#forecast)) |
| `planBand` | `false` | reserved; logs `planBand: not implemented in 0.3` once |
| `swarm` | `false` | reserved; logs once |

Set them in the plugin's config menu, or under `pluginConfigs` in settings.

## Control

With `control` on, the dashboard's **Stop** and **Send prompt** buttons (and the
bulk "continue phase" action) reach this session.

### Enable it

Per project, in `.claude/settings.json` beside `enabledPlugins`:

```json
{
  "enabledPlugins": { "swarmery-link@swarmery": true },
  "pluginConfigs": { "swarmery-link@swarmery": { "options": { "control": true } } }
}
```

or flip **Control from the dashboard** in the plugin's config menu, which
reloads the mod with the new value. Loaded through `--plugin-dir`, the key is
`swarmery-link` (or `swarmery-link@inline`) instead of `swarmery-link@swarmery`.

### How it works

On `session.start`, once the token is read, the mod starts **one** background
loop for the session. It never runs inside a hook, and no hook waits on it.

1. `GET /api/link/commands?session=<session id>&wait=25` with the bearer token.
   The daemon holds the request up to 25 s; the mod gives up on it at 28 s
   (raced against a `$.clock` timer, since `$.http.fetch` takes no timeout).
2. `200 {id, kind, payload}`: run the command, then
   `POST /api/link/commands/{id}/ack` with `{ result: "done" | "failed", message? }`,
   then poll again at once.
3. `204`: poll again (no sooner than 1 s after the last poll started).
4. No answer, a timeout, a 5xx or any other status: wait 3 s, then poll again.
   While the daemon fails the loop sends at most one request every 3 s.
5. `401`: re-read the token file once and retry 1.1 s later; a second `401`
   stops the loop. `503` (`link disabled`) stops the loop and the sensor.
6. `session.end` stops the loop: the pending wait is cancelled and the reply of
   a poll still open is never acted on. After a `/clear` or an in-process
   resume (`reason: "clear"` or `"resume"`) a
   new loop starts at the next `turn.start`, polling under the session id the
   process now runs as; any other reason stops it for good.

| Command | What the mod does | Ack |
|---|---|---|
| `stop` | `$.turn.abort({ turnId })` on the main loop's running turn (the id its `turn.start` carried) | `done`; `done` + `no turn running` when the session is idle; `failed` + the error when the abort throws |
| `prompt` `{ text, asUser? }` | `$.prompt.submit({ text })`, or `{ text, asUser: true }` when the payload says `asUser: true` (default `false`: the model reads it framed as this plugin's message) | `done` once its turn starts (`done` + `queued` if it has not started within 10 s); `failed` + `turn in progress` while a turn runs; `failed` + a short reason on any other refusal |
| anything else | nothing | `failed` + `unknown command kind: …` (or `prompt without text`) |

- **Each command runs at most once.** The mod remembers the ids it ran (the last
  200) and the ack each got, and records them before the ack goes out. A command
  the daemon delivers again (a lost ack, a daemon restart) is acked again and not
  run again.
- **`turn in progress` is retried by the daemon**, after the session's next
  `turn.complete`, up to 3 attempts. That one failure is not remembered, so the
  re-delivered prompt runs.
- A lost ack is not resent: the daemon re-delivers an unacked command after 30 s.

### Security model

- **Token.** Both endpoints take the same bearer token as the sensor
  (`~/.swarmery/link-token`, mode `0600`). Without the file there is no loop.
- **Loopback only.** The command URLs hang off the same `daemonUrl` origin, which
  is refused unless it is `http://` on `127.0.0.1`, `[::1]` or `localhost`.
- **A session is reachable only while it polls.** The daemon queues a command for
  a session only while that session's mod has polled in the last 60 s; any other
  session answers `409 session has no link`. A session without the mod, or with
  `control` off, cannot be stopped or prompted from the dashboard.
- The dashboard side of the channel (`POST /api/sessions/{id}/link/stop|prompt`)
  accepts local-origin requests only.

## Forecast

With `forecast` on, the mod asks a small model before each turn what the turn
will do, then records what it did. The daemon compares the two and scores the
surprise; the dashboard's Insights shows how well the forecasts are calibrated.
It is advisory: nothing is blocked or changed.

### Enable it

```json
{
  "enabledPlugins": { "swarmery-link@swarmery": true },
  "pluginConfigs": { "swarmery-link@swarmery": { "options": { "forecast": true } } }
}
```

or flip **Turn forecast** in the plugin's config menu. It needs the token file
like the rest of the link: without it no model call is made.

### What it costs

One `$.model.complete` to `haiku` per turn the person typed, with
`maxTokens: 300` and a 20 s time limit. The request is a fixed two-line
instruction, at most 2000 characters of the person's text, and the files the
last 3 turns edited: about 150–700 input tokens and 30–100 output tokens per
forecast. The tokens each call cost ride along as `forecastTokens`.

Skipped, with no model call: turns without the person's text (a continuation, a
slash command, a task notification), a turn that starts while the last
forecast is still running, and every turn while the link has no token. The call
is started from the turn's hook and never waited on, so a turn never waits for
the model.

### What is sent

Two events per forecast turn, through the same queue as the sensor:

| Event | Payload |
|---|---|
| `turn.forecast` | `{ runId, turnSeq, files, testsPass, steps, confidence, model, forecastTokens }`: the model's claims, each `null` where it made none or its reply was unreadable |
| `turn.actual` | `{ runId, turnSeq, files, testsRan, testsPass, steps }`, at the turn's end |

- **Paths and numbers only.** No prompt text, tool input or model reply text is
  sent. A `files` entry that is not one path-like token (spaces, quotes, no `/`
  or `.`) is dropped, so a model that echoes the prompt cannot leak it.
  Paths are made relative to the session's cwd where they are under it; at most
  50 entries of at most 300 characters.
- **`turnSeq`** counts the main loop's turns from 1 within a **`runId`**. A new
  run starts at `session.start` (a `--resume` too, which keeps the session id),
  at a `session.end` with reason `clear`, and at the first turn after an
  in-process resume. Its id is the reset's time in ms and 6 random base36
  characters (`crypto.getRandomValues`), the time alone without a random
  source. The daemon keys a turn on (session, `runId`, `turnSeq`), so a resumed
  session's turn 1 never overwrites an older turn 1.
- **The actual** is the main loop's tool calls this turn (a subagent's are left
  out): `files` that `Edit`, `Write`, `MultiEdit` or `NotebookEdit` wrote
  without error, `steps` the number of calls, `testsRan` when a `Bash` command
  matched `test`, `vitest`, `jest`, `go test`, `pytest`, `cargo test` or
  `npm (run) test`. `testsPass` is the last such run's outcome, read from the
  call's result (an error result, as a non-zero exit is, reads as failing); it
  stays `null` for a run in the background and when no test ran.
- A forecast that answers after its turn ended is still sent, after the actual.
  One still running when the counter starts over (a `/clear`, a resume, a new
  `session.start`) is dropped: its session and run are gone.

## Degradation

The link is optional. The daemon still records every session from its JSONL
transcript.

- **Daemon down or slow**: a request that gets no answer in 5 s (raced against a
  `$.clock` timer, because `$.http.fetch` takes no timeout) is treated as failed.
  The mod logs one line, keeps at most 200 events, and retries every 2 s.
- **No hook waits on the daemon.** A turn boundary starts a send and moves on;
  while the daemon is failing it leaves sending to the 2 s timer. Only
  `session.end` waits, and never more than 1.2 s.
- **A batch the daemon refuses** (400, 403, 413): dropped, one log line.
- **Any error in a hook** is caught. The session never sees one.

## Disable

Turn it off in `/plugin`, or set `"swarmery-link@swarmery": false` under
`enabledPlugins`. Deleting the token file also silences it.

## Develop

```bash
claude plugin validate --strict --json plugins/swarmery-link
claude plugin test plugins/swarmery-link
npx -p typescript@5.6.3 tsc -p plugins/swarmery-link   # once .claude-plugin/types/ is laid
```

`hooks/register.ts` is the one hooks module and spells every `$` call;
`hooks/lib/*` is pure (`lib/control.ts` holds the command channel's decisions:
parsing a command, choosing the action, the executed-id set, the backoff, the
ack body; `lib/forecast.ts` the forecast's: skip rules, the model request,
the tolerant reply parser, the tool-call journal). Tests: `hooks/link.test.ts`
(sensor), `hooks/control.test.ts` (command channel) and `hooks/forecast.test.ts`
(forecast), all on the shared world in `hooks/testkit.ts`. `.claude-plugin/types/` is written by the engine and is
not committed.
