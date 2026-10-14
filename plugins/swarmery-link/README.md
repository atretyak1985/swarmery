# swarmery-link

A **mod** (a plugin of function hooks) that reports a Claude Code session's live
state to a local swarmery daemon, so the dashboard sees a session's status,
context fill, rate limits and cost the moment they change instead of waiting for
the transcript tail.

Version 0.1 is a **sensor only**. It never blocks, rewrites or answers anything:
every hook passes its event on unchanged, and a failure on the link is logged and
swallowed.

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
./register.ts calls: $.clock.after (via arm, send, within), $.clock.now (via push), $.env.get (via readToken), $.fs.read (via readToken), $.http.fetch (via post), $.session.cwd, $.session.id, $.session.usage, $.ui.log (via log)
./register.ts env writes: nothing
./register.ts env reads: HOME
```

`tool.call` is listed as a gating hook because of where it sits, not what it
does: it always calls `next(e)`, and its `.catch` does the same.

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

| Option | Default | Effect in 0.1 |
|---|---|---|
| `daemonUrl` | `http://127.0.0.1:7777` | the daemon's origin. Only `http://` on `127.0.0.1`, `[::1]` or `localhost` (any port, path ignored) is accepted; anything else is refused with one log line and the default is used, so the token never leaves the machine |
| `control` | `false` | reserved; logs `control: not implemented in 0.1` once, registers no gating hook |
| `planBand` | `false` | reserved; logs once |
| `swarm` | `false` | reserved; logs once |
| `forecast` | `false` | reserved; logs once |

Set them in the plugin's config menu, or under `pluginConfigs` in settings.

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
`hooks/lib/*` is pure. `.claude-plugin/types/` is written by the engine and is
not committed.
