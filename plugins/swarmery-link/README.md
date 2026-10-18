# swarmery-link

A **mod** (a plugin of function hooks) that reports a Claude Code session's live
state to a local swarmery daemon, so the dashboard sees a session's status,
context fill, rate limits and cost the moment they change instead of waiting for
the transcript tail.

By default it is a **sensor only**. It never blocks, rewrites or answers anything:
every hook passes its event on unchanged, and a failure on the link is logged and
swallowed. Only the opt-in `approvals` option ever answers an event, and only
with a decision a person made in the dashboard (see [Approvals](#approvals)).

Version 0.2 adds an opt-in **command channel** (`control`, off by default): the
dashboard's Stop and Send-prompt buttons reach the session through the daemon.
See [Control](#control).

Version 0.3 adds an opt-in **turn forecast** (`forecast`, off by default): one
small model call per turn predicts what the turn will touch, the turn's own tool
calls make the actual, and the daemon scores how surprised the forecast was.
See [Forecast](#forecast).

Version 0.4 adds opt-in **approvals** (`approvals`, off by default): a
permission ask goes to the swarmery approvals queue first, and an
`AskUserQuestion` can be answered from the dashboard. The `PermissionRequest`
hook shim and the native dialog stay as the fallback. See [Approvals](#approvals).

Version 0.5 adds opt-in **swarm coordination** (`swarm`, off by default):
messages between sessions are reported, the files each session starts editing
are claimed in a registry its sessions share, a line beneath the answer flags a
file another live session is editing, and the dashboard can message a session.
See [Swarm](#swarm).

Version 0.6 adds an opt-in **plan band** (`planBand`, off by default): the
session's running plan phase above the prompt, `/plan` for its checklist, a card
for interview-style questions, and a line beneath an answer that drifted off the
phase. See [Plan band](#plan-band).

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
./register.ts hooks: session.start, turn.start, tool.call, tool.check, tool.call{tool=AskUserQuestion}, session.measure, turn.complete, session.end, session.send, session.receive, ui.render{component=AbovePrompt}, ui.render{component=Pane, requestId=?}, ui.render{component=AskUserQuestion}, command.run{command=plan}
./register.ts gating hook with .catch: tool.call
./register.ts gating hook with .catch: tool.check
./register.ts gating hook with .catch: tool.call{tool=AskUserQuestion}
./register.ts gating hook with .catch: session.send
./register.ts gating hook with .catch: session.receive
./register.ts gating hook with .catch: command.run{command=plan}
./register.ts calls: $.clock.after (via arm, beginForecast, bounded, pause, send, startControl, within), $.clock.every (via startPlanTimer), $.clock.now (via askSwarmery, claimFile, newRun, poll, pollDecision, push), $.command.register (via registerPlanCommand), $.env.get (via readToken), $.fs.read (via readToken), $.http.fetch (via cancelRequest, fetchCommand, planFetch, post, request, sendAck), $.model.classify (via checkDrift), $.model.complete (via runForecast), $.prompt.submit (via submitPrompt), $.session.cwd, $.session.id, $.session.send (via sendToSession), $.session.usage, $.store.delete (via releaseClaims), $.store.get (via claimFile), $.store.keys (via claimFile), $.store.set (via claimFile), $.turn.abort (via abortTurn), $.ui.ask (via pressNext, pressTick), $.ui.copy (via pressOpen), $.ui.invalidate (via setView), $.ui.log (via log), $.ui.notice (via showNotice), $.ui.open (via runPlanCommand), $.ui.resolve (via drawBand, drawInterview, drawPane), $.ui.status (via showStatu… [+26 chars]
./register.ts env writes: nothing
./register.ts env reads: HOME
```

The unmatched `tool.call`, `session.send` and `session.receive` are listed as
gating hooks because of where they sit, not what they do: each always calls
`next(e)` with the event unchanged, and its `.catch` does the same (replay-safe:
a send is never made twice). With `forecast` or `planBand` on `tool.call`
waits for the call's result to note how a main-loop call ended, and returns
that result unchanged. `command.run{command=plan}` answers `/plan` itself; its
`.catch` answers with one line if it fails.

`tool.check` and `tool.call{tool=AskUserQuestion}` are the approvals channel's
and are registered only with `approvals` on; validate lists them whatever the
options say (it reads the source, not the options). Their `.catch` hands the
event on (`next(e)`), never refuses.

`$.turn.abort` and `$.prompt.submit` are the command channel's. Validate lists
them whatever the options say; with `control` off they are never called and no
request goes to the command endpoints. `$.model.complete` is the forecast's;
with `forecast` off it is never called. `$.ui.status` and `$.ui.notice` are the
approvals channel's; with `approvals` off they are never called. `$.store.*`
and `$.session.send` are the swarm's; with `swarm` off they are never called,
and `session.send` / `session.receive` are not registered at all (validate
lists every `on` it reads, conditional or not). The `ui.render` and
`command.run` hooks, `$.clock.every`, `$.command.register`, `$.model.classify`
and `$.ui.ask`, `$.ui.copy`, `$.ui.invalidate`, `$.ui.open`, `$.ui.resolve`,
`$.ui.toast` are the plan band's: with `planBand` off none of those hooks is
registered and none of those calls is made.

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

| Option | Default | Effect |
|---|---|---|
| `daemonUrl` | `http://127.0.0.1:7777` | the daemon's origin. Only `http://` on `127.0.0.1`, `[::1]` or `localhost` (any port, path ignored) is accepted; anything else is refused with one log line and the default is used, so the token never leaves the machine |
| `control` | `false` | the command channel: the dashboard can stop the running turn and send prompts ([Control](#control)) |
| `forecast` | `false` | the turn forecast: one haiku call per user turn, a `turn.forecast` and a `turn.actual` per forecast turn ([Forecast](#forecast)) |
| `approvals` | `false` | routes each permission ask to the Swarmery approvals queue first, ahead of the dialog or auto mode's classifier; the shim/dialog takes over if the daemon is down or does not decide in time. An `AskUserQuestion` can be answered from the dashboard ([Approvals](#approvals)) |
| `swarm` | `false` | swarm coordination: message events, the file-claims registry, the conflict line, and the dashboard's `message` command with `control` on ([Swarm](#swarm)) |
| `planBand` | `false` | the plan band, `/plan`, the interview card and the drift line ([Plan band](#plan-band)) |

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
| `message` `{ to, text }` | with `swarm` on, `$.session.send({ to: { sessionId: to }, text })`, whether or not a turn runs ([Swarm](#swarm)) | `done` once queued at the recipient; `failed` + `undeliverable: <reason>` when not; `failed` + `swarm option is off` with `swarm` off |
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

## Approvals

With `approvals` on, a permission the engine would ask the person about goes to
the swarmery approvals queue first, and the dashboard decides it. The
`PermissionRequest` hook shim and the native dialog stay as the fallback: the
mod first, the shim when the mod cannot decide.

### Enable it

```json
{
  "enabledPlugins": { "swarmery-link@swarmery": true },
  "pluginConfigs": { "swarmery-link@swarmery": { "options": { "approvals": true } } }
}
```

or flip **Approvals in Swarmery** in the plugin's config menu. It needs the
token file like the rest of the link. With `approvals` off no `tool.check` hook
and no `AskUserQuestion` hook is registered, and no request goes to the
permission endpoint.

### How a permission is decided

The hook sits on `tool.check`, which fires after the `tool.call` and
`PreToolUse` hooks and before the mode settles an ask.

1. **The engine's verdict comes first.** The hook runs the chain beneath
   (`next(e)`): the settings rules, the mode, the tool's own check, a
   `PreToolUse` decision. An `allow` or a `deny` is returned untouched; only an
   `ask` goes to Swarmery.
2. **Only a real call.** A `$.tool.check` query (no `tool_use_id`) is never
   sent, and neither is a tool that needs the person (`AskUserQuestion`,
   `ExitPlanMode`: a hook's allow cannot dismiss their dialog) or a call the
   organization's ceiling keeps at `ask` or below.
3. `POST /api/link/permission` with the bearer token and
   `{ sessionId, cwd, toolName, toolInput, toolUseId, agentId? }`, bounded at
   5 s on a `$.clock` timer.
4. The daemon answers:

   | Reply | What the mod does |
   |---|---|
   | `200 {"decision":"allow"}` (a person, or a rule: `"via":"rule"`) | `allow`, reason `approved in Swarmery` |
   | `200 {"decision":"deny","message":m}` | `deny`, reason `m` (or `denied in Swarmery`) |
   | `200 {"decision":"local"}` (a production deploy) | the engine's verdict, and the dialog shows `production deploy: confirm it here, not in Swarmery` |
   | `200 {"decision":"pass"}`, `204` (an excluded project) | the engine's verdict |
   | `202 {"id","status":"pending","expiresAt"}` | waits for the dashboard (below) |
   | `401` | re-reads the token file once and retries 1.1 s later; a second `401` leaves the engine's verdict |
   | no answer in 5 s, `429`, `5xx`, anything else | the engine's verdict |

5. **Waiting.** `GET /api/link/permission/{id}?session=<id>&wait=25`; the daemon
   holds each poll up to 25 s and the mod gives up on one at 28 s. A `204` polls
   again (no sooner than 1 s after the last one started), a `200` decides as
   above, a `404` (the request is gone) leaves the engine's verdict. The wait
   ends with the engine's verdict when the person interrupts the call
   (`next.signal`), at `expiresAt` plus 5 s, or after 3 failed polls in a row.
   Only the interrupt gives the request up: the mod sends
   `DELETE /api/link/permission/{id}?session=<id>` fire-and-forget (bounded at
   2 s, never awaited), so a later dashboard decision cannot land on a call no
   hook reads. On every other end the request stays pending, and the shim the
   engine fires next attaches to it.
   The time spent inside a long poll does not count against the hook's 10 s
   budget (`tools/swarmery/docs/mods-format.md`, M3), so the mod can wait out
   the daemon's whole approval window. While it waits, the status line reads
   `waiting for Swarmery approval…`; it is cleared when the wait ends, however
   it ends.

**It fails open to the shim and the dialog.** Every failure returns the
engine's own verdict, which for an `ask` means the mode's decider runs as if the
mod were not there: the native dialog in the terminal, the `PermissionRequest`
shim where it is installed. The mod never answers `ask` of its own and never
refuses a call because the daemon failed. Its `.catch` hands the event on.

**A production deploy stays local.** When the daemon marks a call as a
production deploy (`local`), the mod leaves it to the terminal's dialog and
says so there; it is never approved from the dashboard.

### Questions from the dashboard

An `AskUserQuestion` call (a matched `tool.call` hook, registered only with
`approvals` on) is put to the daemon the same way, with `toolName:
"AskUserQuestion"` and the tool's input (`{ questions, ... }`) as `toolInput`.
When the dashboard answers (`200 {"decision":"allow","answer":{questions,answers}}`)
the mod returns `{ result: { questions, answers } }` without running the tool:
the engine shows it as the person's answer, no dialog is drawn, and the model
acts on it (`mods-format.md`, M4). `answers` maps each question's text to the
chosen label (a multi-select's labels comma-joined). Anything else (a pass, a
deny, a failure, the deadline, an interrupt) hands the call on, and the
question's own dialog opens. The sensor still reports the call, and the
forecast still counts it.

### Auto mode

`tool.check` runs **before** the mode's decider, so with `approvals` on, an
`ask` that auto mode's classifier would have settled on its own goes to the
dashboard first and waits for a person there. That is why `approvals` is its
own opt-in, separate from `control`. In `-p` (headless) runs the hook fires
too, which gives them dashboard approvals the `PermissionRequest` shim never
had there.

## Swarm

With `swarm` on, sessions working side by side see each other: the messages
they exchange reach the daemon, and two sessions editing the same file hear
about it. It is advisory: nothing is blocked, rewritten or refused.

### Enable it

```json
{
  "enabledPlugins": { "swarmery-link@swarmery": true },
  "pluginConfigs": { "swarmery-link@swarmery": { "options": { "swarm": true, "control": true } } }
}
```

or flip **Swarm coordination** in the plugin's config menu. `control` is needed
only for the dashboard's `message` command. Like the rest of the link it needs
the token file: without it the swarm stays silent and never touches the
registry.

### What is sent

Through the same queue as the sensor:

| Event | When | `payload` |
|---|---|---|
| `session.send` | a message the main conversation sent (the model's SendMessage, or a plugin's `$.session.send`) settled; a subagent's is left out | `{ to, text, truncated, isDelivered, reason?, origin: "model" \| "plugin", commandId? }`: `to` as the engine spells the recipient, `reason` only when not delivered, `commandId` only for the dashboard's `message` command |
| `session.receive` | a delivery reached the main conversation (a subagent's is left out) | `{ text, truncated, originKind, fromPlugin? }`, plus one line on screen: `swarmery-link: from <originKind>[ <plugin>]: <first 80 characters>` |
| `claim` | the first `Edit`, `Write`, `MultiEdit` or `NotebookEdit` of a path since the last release | `{ path }`, absolute (a relative one is resolved against the session's cwd) |
| `conflict` | that claim found another live session holding the same path | `{ path, otherSession }` |
| `release` | the main loop's `turn.complete` after a turn that claimed something; `session.end` | `{ paths: [...] }`, or `{ all: true }` when the turn claimed more than 500 paths (the daemon's limit per release); `{ all: true }` at `session.end` |

- **Text is clipped** to 4096 characters (`truncated: true` when it was), never
  split inside a surrogate pair.
- **The hooks pass every message on unchanged.** `session.send` is reported once
  `next(e)` settles, so `isDelivered` is the engine's answer.

### The claims registry

The registry lives in this plugin's own `$.store` (one JSON file the engine
keeps per plugin, shared by every session on the machine that runs it).

- Keys sit under `swarmery-link/`. A session's claims are one entry,
  `swarmery-link/claims/<sessionId>` =
  `{ sessionId, cwd, updatedAt, paths: [{ path, at }] }` (times in ms), written
  whole with one `$.store.set`. Only that session writes its own key; other
  sessions' keys are read, never written or deleted.
- **Paths and times only.** No text, tool input or file content is stored.
- **Live for 30 minutes.** Another session's entry counts while its `updatedAt`
  is at most 30 min old; an older one is ignored (not deleted).
- **At most 64 KiB** of JSON per entry: past it, the oldest paths drop first.
- **Claims last one turn.** The main loop's `turn.complete` deletes the entry and
  sends `release`; `session.end` deletes it too and sends `release { all: true }`.
  After a `/clear` the new session id claims under a fresh key; the old one went
  at the `session.end` that preceded the clear.
- Claims and releases run one at a time, so parallel tool calls never lose one.

### The conflict line

At the main loop's `turn.complete`, a turn that found a conflict adds one line
beneath the answer:

```
⚠ /work/app/src/a.ts is also being edited by session 7c1d2e3f
```

With several, the first path and `(+N more)`. The line is returned as the
`turn.complete` result's `text`: the engine shows a text other than the main
loop's answer beneath it and never rewrites the answer itself. A text another
hook already put there is kept, the line after it.

### The `message` command

With `control` and `swarm` on, the dashboard can queue `message`
`{ to: <recipient session uuid>, text }` for this session. The command loop sends
it with `$.session.send({ to: { sessionId: to }, text })`, whether or not a turn
is running (it lands in the recipient's queue, not this one), and acks `done`
once it is queued there, or `failed` + `undeliverable: <reason>`. The send is
reported as a `session.send` with the command's `commandId` (a JSON number) once it
settles: a send still unsettled after 10 s is acked `failed` +
`undeliverable: no answer in 10s`, and reported, with its `commandId`, whenever it
settles later, so the daemon takes its verdict over the ack's. With `swarm` off it
is acked `failed` + `swarm option is off` and nothing is sent.

## Plan band

With `planBand` on, the session's swarmery plan travels with it in the terminal.

### Enable it

```json
{
  "enabledPlugins": { "swarmery-link@swarmery": true },
  "pluginConfigs": { "swarmery-link@swarmery": { "options": { "planBand": true } } }
}
```

or flip **Plan band** in the plugin's config menu. It needs the token file like
the rest of the link.

### The band

Above the prompt, drawn from a cache and never from a request:

```
Phase 2 — Line-item CRUD · 1/4
☐ GET lists the line items in insert order
☐ DELETE removes one and answers 204
☐ deployed to staging
1: next phase  2: open in Swarmery  3: tick
```

- **The header** is `Phase <n> — <title> · <done>/<total>`. Up to three criteria
  follow (open ones first, done ones `☑` filling what is left), fewer when the
  band has fewer rows (`maxRows`), each cut to the band's width (`bodyColumns`).
- **`1` next phase** (only when the plan has a next phase) asks
  `Start phase <n> — <title> now?` and, on `Start it`, has the daemon start it.
- **`2` open in Swarmery** copies the plan's dashboard URL (no mod call opens a
  browser) and shows it in a toast.
- **`3` tick** asks which open criterion is done (at most four are offered) and
  has the daemon tick it in the phase doc. The tick carries the criterion's text
  as the band drew it: if that criterion moved or changed in the doc since
  (`409 criterion changed`), nothing is ticked, the band is refreshed and says
  so; tick again.
- The digits press the buttons once the band has the focus (a click, ctrl+x tab),
  or from an empty prompt. The band yields to a survey and draws nothing while
  the session has no plan.

**The cache.** `GET /api/link/plan?session=<id>&cwd=<cwd>` fills it at
`session.start`, after each main-loop `turn.complete`, on `/plan`, after a
button's request, and every 60 s. `200` is the plan; `204`, no token, a daemon
that does not answer in 5 s or any other status empties it, and the band draws
nothing. After a `/clear` the plan is read again under the new session id.

### `/plan [task-id]`

Runs without a model turn. `/plan` reads the plan again and opens the **Plan**
pane with the whole checklist (every criterion `☐`/`☑`, its `[LAND]`/`[MANUAL]`
class, the phase status, the next phase, a link to the dashboard). Where no pane
is placed (a narrow terminal), the checklist is the command's output instead.
`/plan <task-id>` first has the daemon bind that plan to the session
(`POST /api/link/plan/select`). With no plan it answers
`no active plan for this session (pick one with /plan <task-id>)`.

### Interview questions

An AskUserQuestion asked in the dashboard interview's shape (the question's text
or description is a `{"type":"question","data":{question, description,
options:[{label, description, pros, cons, recommended}]}}` JSON, a ```` ```json ````
fence allowed) gets a card above the dialog: the question, each option's
description, its pros (`+`) and cons (`−`), the recommended one highlighted.
Nothing else makes a question an interview: an option labelled `(Recommended)`
or a description with `Pros:`/`Cons:` is what Claude Code's own guidance asks
of ordinary questions, and those draw as they always do. The dialog's props carry no answer callback, and
the engine refuses an AskUserQuestion tree without exactly one engine node, so
the card is drawn **above** the engine's own dialog, which still takes the
answer.

### The drift line

At the end of a main-loop turn that edited files (`Edit`, `Write`, `MultiEdit`,
`NotebookEdit`; a subagent's edits are left out) while the session's plan phase
is `In progress`, or was picked in the terminal with `/plan <task-id>`, one
`$.model.classify` labels the turn `on-phase`, `adjacent` or
`off-phase`. Its text is the phase title and its open criteria, the files the
turn edited and the last 2048 characters of the answer. On `off-phase` the daemon
gets `POST /api/link/plan/drift` and one line is shown beneath the answer:

```
↯ this turn looks outside phase 2: edited src/billing/invoice.ts, docs/notes.md
```

`classify` names a label and nothing else, so the reason is the edited files;
the whole line is cut to 120 characters (`…` marks the cut). A view the daemon
resolved on its own that lands on a `Pending` or `Blocked` phase is never
classified: nothing is off a phase nobody has started. The transcript is never rewritten. With `swarm` on too,
a conflict line and the drift line are joined beneath the same answer.

**What it costs.** One classification (the engine's small fast model) per
main-loop turn that edited files while a phase runs; never on a turn without
edits, never on a subagent's turn, never twice for one turn. It is bounded by a
15 s timer; a failure or a timeout draws no line and leaves the turn as it was.

### Where it shows

The band is the terminal's: `AbovePrompt` is raised on the terminal (and the
desktop), never under `-p`, and the VS Code panel was not observed live (the test
kit validates the tree on `vscode` and `mobile` too). Where there is no band,
`/plan` and the drift line still work: `/plan` is a command's output or a pane,
and the drift line is part of the turn's answer.

## Degradation

The link is optional. The daemon still records every session from its JSONL
transcript.

- **Daemon down or slow**: a request that gets no answer in 5 s (raced against a
  `$.clock` timer, because `$.http.fetch` takes no timeout) is treated as failed.
  The mod logs one line, keeps at most 200 events, and retries every 2 s.
- **No sensor hook waits on the daemon.** A turn boundary starts a send and
  moves on; while the daemon is failing it leaves sending to the 2 s timer. Only
  `session.end` waits, and never more than 1.2 s. The approvals hooks (opt-in)
  are the exception by design: they wait for a person's decision, every wait
  bounded, and fall back to the engine's verdict.
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

`hooks/register.ts` is the one hooks module: it registers every hook. `$` is
handed only to top-level function declarations: its own, `hooks/approvals.ts`'s
(the approvals channel's waits), `hooks/swarm.ts`'s (the registry's `$.store`
calls and the message's `$.session.send`), and the plan band's in
`hooks/plan.ts` (cache, requests, band, pane, `/plan`, the buttons),
`hooks/drift.ts` (the classification) and `hooks/ask-render.ts` (the interview
card). `hooks/lib/*` is pure (`lib/control.ts` holds the command channel's
decisions: parsing a command, choosing the action, the executed-id set, the
backoff, the ack body; `lib/forecast.ts` the forecast's: skip rules, the model
request, the tolerant reply parser, the tool-call journal; `lib/approvals.ts`
the approvals channel's: which verdicts go to the daemon, the request, what
each reply means, the deadline; `lib/swarm.ts` the swarm's: claim paths, the
entry and its cap, liveness, conflicts, payloads, the line beneath the answer;
`lib/plan.ts` the band's: the plan view parser, the band layout, the
checklist, the drift input and line; `lib/interview.ts` reads interview
questions). Tests: `hooks/link.test.ts` (sensor), `hooks/control.test.ts`
(command channel), `hooks/forecast.test.ts` (forecast),
`hooks/approvals.test.ts` (approvals), `hooks/swarm.test.ts` (swarm),
`hooks/plan.test.ts` (band and `/plan`), `hooks/drift.test.ts` (drift line)
and `hooks/ask-render.test.ts` (interview card), all on the shared world in
`hooks/testkit.ts`. `.claude-plugin/types/` is written by the engine and is
not committed.
