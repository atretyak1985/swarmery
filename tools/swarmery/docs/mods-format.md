# Claude Code Mods API — observed contract for the swarmery link

**Status:** spike result (mods-swarmery-link, Phase 0, SC-1). Everything below was seen on
this machine, from a prototype mod. Nothing here comes from memory. Anything *not*
observed is labelled as such. This is the mods-era counterpart of
[`hooks-format.md`](hooks-format.md). It answers the five design questions that phases 1, 2, 5
and 7 depend on, and it carries the fixtures phase 9 tests against.

## Evidence base

- **Claude Code:** `2.1.296 (Claude Code)` (`$.session.version()` →
  `{ version: "2.1.296", base: "2.1.296", builtAt: "2026-10-09T01:31:52Z" }`), macOS (arm64).
- **Date:** 2026-10-13.
- **Model in the probe sessions:** `claude-haiku-5-5` (`--model haiku`). The hooks do not
  depend on the model.
- **API reference:** the declaration file the engine writes when the plugin-authoring skill
  loads (`types/claude-code.d.ts`, 21 463 lines, header `Written by Claude Code 2.1.296`).
  There is no `claude plugin types` subcommand. The types are written for you, as the
  create guide says.
- **Harness** (lives in the private workspace task dir, never committed):
  `…/mods-swarmery-link/spike/`
  - `link-spike/`: the prototype mod. It has `.claude-plugin/plugin.json`,
    `hooks/hooks.json` (`{ "modules": ["./register.tsx"] }`), `hooks/register.tsx` and
    `hooks/band.test.ts`. Every hook writes a `$.ui.log` line and POSTs
    `{ kind, at, sessionId, data }` to the stub. It hooks `session.start`,
    `session.measure`, `prompt.submit`, `turn.complete`, `tool.call` (all tools, plus one
    on `AskUserQuestion`), `tool.check`, `ui.render{AbovePrompt}` and `session.end`.
  - `stub.py`: a `ThreadingHTTPServer` on `127.0.0.1:8765`. `POST /event` appends the
    body to `stub.log`. `GET /approval?id=` long-polls `STUB_HOLD` = 25 s and answers
    `204` for the first `STUB_N` = 25 calls per id, then `200 {"decision":"allow"}`.
  - `interactive.exp` / `interactive-long.exp`: drive a real interactive
    `claude --plugin-dir link-spike` over a PTY with `expect` (this box has no `tmux`).
  - Headless probes: `claude -p --plugin-dir link-spike …` with `--output-format json` or
    `stream-json`.
- **Type-check:** `tsc -p` against the header's tsconfig, kept outside the mod folder → exit 0.
  `claude plugin test link-spike` → 1 pass.

> **Nested-session gotcha.** A `claude` started from inside another Claude Code session
> inherits `CLAUDE_CODE_CHILD_SESSION=1`. In an *interactive* child this turns transcript
> saving off ("Transcript saving is off — inherited CLAUDE_CODE_CHILD_SESSION marker"), so
> there is no JSONL to compare against. A `-p` child still saved its transcript. The
> interactive M1 run therefore used `env -u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_SESSION_ID
> -u CLAUDE_CODE_MESSAGING_* -u CLAUDE_CODE_ENTRYPOINT -u CLAUDE_CODE_SESSION_ATTENDED`.

> **Secret gate:** the fixtures carry no credentials. Session ids are random UUIDs and the
> home path does not appear. The token file used in M7 is a dummy
> (`spike-dummy-token-not-a-secret`). Only its length was ever sent to the stub.
> `grep -rinE 'api[_-]?key|sk-ant|ghp_|AKIA|password|secret'` over
> `testdata/fixtures/mods/` returns 0 hits.

## Experiment → verdict

| # | Experiment | Verdict |
|---|---|---|
| M1 | `$.session.id()` vs JSONL `sessionId` | ✅ **M1 verified:** equal in `-p`, interactive and `--resume` |
| M2 | `session.measure` / `$.session.usage()` shape | ✅ **M2 verified:** shape matches the types; `cost` is `{ usd: number }`, counted per process |
| M3 | long poll loop inside `tool.check` | ✅ **M3 verified:** the loop lived **652 s** (> 600 s `approvals.DefaultTimeout`) in `-p` *and* in the terminal, then returned `allow` and the tool ran. `e.tool_use_id` is present |
| M4 | `{ result }` for `AskUserQuestion` from `tool.call` | ✅ **M4 verified:** accepted. The dialog is never shown and the model reads the answer |
| M5 | hooks × surface | ✅ terminal and `-p` observed; ⚠ **VS Code not observed live** (no VS Code on this host). The test kit validates the band's tree on `vscode` |
| M6 | `claude plugin validate --strict --json` | ✅ **M6 verified:** real output in fixture. The shape is `notes` strings plus `gatingHooks`, with **no** `events`/`uses`/`env`/`state` keys |
| M7 | `$.fs.read` of the token file | ✅ **M7 verified:** `~` is **not** expanded. Use `${await $.env.get('HOME')}/…`. Validate does not flag reads outside the project |

## M1 — session id parity

**Command.** Headless: two turns through `--input-format stream-json` in
`spike/run/`. Then `claude -p --resume <id> "…three"`. Interactive: `interactive.exp`
with the child-session env unset.

**Observed** (`session.start` hook → stub):

| Run | `$.session.id()` | JSONL file / every row's `sessionId` | other id source |
|---|---|---|---|
| `-p`, 2 turns | `efaa9b56-306f-48b7-bba8-51c5ffa08f79` | `efaa9b56-….jsonl`, 32/32 rows equal | stream-json `session_id`, 18/18 equal |
| `-p --resume efaa9b56…` | `efaa9b56-306f-48b7-bba8-51c5ffa08f79` | same file, appended | `--output-format json` `session_id` equal |
| interactive (terminal) | `8828c80c-09c9-4e83-86d3-de9f216c294a` | `8828c80c-….jsonl`, 40/40 rows with a `sessionId` equal (2 rows carry none) | — |

`session.start` fires again on `--resume`, with the **same** id. `$.session.usage().startedAt`
stays at the first launch (`1791920751978` in both processes). `e` on `session.start` is
`{ cwd, surface, isInteractive }`: `{ surface: null, isInteractive: false }` under `-p`,
`{ surface: "terminal", isInteractive: true }` interactively. `$.session.cwd()` and
`$.session.root()` both equal the launch directory.

**Verdict: M1 verified.** `$.session.id()` *is* the transcript file's name and the JSONL
`sessionId`, so phase 1 can key the link on it with no translation. The exception is
`--fork-session`, which this spike did not run: a fork gets a new id by design.

## M2 — usage shape

**Fixture:** [`testdata/fixtures/mods/session-usage.json`](../testdata/fixtures/mods/session-usage.json)
holds `session.measure`'s `e` and `$.session.usage()` after each of two `-p` turns.

| Field | Type (observed) | Notes |
|---|---|---|
| `startedAt` | `number`, epoch ms (`$.clock.now()` scale) | Only in `usage()`, not in `measure.e`. First launch of the session; survives `--resume` |
| `context.tokens` | `number`, optional | Input tokens of the last response (uncached + cache-write + cache-read). **Absent** before the first response (interactive first measure: `context: { window: 1000000 }`) |
| `context.window` | `number` | `1000000` for haiku-5-5 here |
| `context.percent` | `number` (integer 0–100), optional | Absent with `tokens` |
| `rateLimits[]` | `Array<{ kind: string, percentUsed: number, resetsAt?: string }>` | `kind` ∈ `five_hour`, `seven_day` (subscription). `percentUsed` is a 0–100 number with at most one decimal. `resetsAt` is ISO 8601 with ms (`"2026-10-13T20:20:00.000Z"`). Empty off a subscription |
| `cost` | **object** `{ usd: number }`, optional | Raw float (`0.0043368899999999995`). **Per process**: after `--resume` it restarted from 0 (`0.0094294` = that process's `total_cost_usd`), so it is *not* the session's lifetime cost. `{ usd: 0 }` before the first response |
| `measure.e.changed` | `Array<'context' \| 'rateLimits' \| 'cost'>` | The first measure names every unit; later ones only what moved |

**Event order is not fixed.** On turn 1 `session.measure` arrived *before* `turn.complete`.
On turn 2 it arrived after. An interactive session also raises one measure at start, before
any prompt, with no `tokens` and `cost.usd: 0`.

**Verdict: M2 verified.** The shape matches `SessionUsage` in the types. `cost` is an
object, not a number. A consumer that sums cost across processes must treat each process's
`usd` as its own running total, keyed by (session id, process start).

## M3 — a long poll loop inside `tool.check`

**Plan deviation.** The phase doc sketched `$.http.fetch(url, { timeout: 25000 })`, but
`HttpInit` has **no `timeout` field** in 2.1.296 (`{ method, headers, body, auth,
socketPath }` only) and **no `signal`**. The per-request wait therefore lives in the
server: `stub.py` holds each `GET /approval` for 25 s. The run used `STUB_N=25` (25 × 25 s
of `204`, then `200`), not the doc's 180 s, because the criterion is ≥ `approvals.DefaultTimeout`
= **10 min** (`internal/approvals/approvals.go:87`).

**The hook** (`register.tsx`, abridged):

```ts
on('tool.check', async ($, e, next) => {
  if (!isProbe(e)) return next(e)
  const started = await $.clock.now()
  for (let i = 1; ; i++) {
    const res = await $.http.fetch(`${STUB}/approval?id=${e.tool_use_id}`)   // stub holds 25 s
    // POST { i, elapsedMs, status, remainingMs: next.budget.remainingMs, aborted: next.signal.aborted }
    if (res.status === 200) return { decision: JSON.parse(res.text).decision }
    if (next.signal.aborted || elapsedMs > 20 * 60_000) return next(e)
  }
}).catch(async ($, e, next) => ({ decision: 'deny' }))   // never reached
```

**Commands.**
`claude -p --plugin-dir link-spike --model haiku --output-format stream-json --verbose "Run exactly this bash command once and report its output: echo M3-probe-ok" < /dev/null`
and an interactive `interactive-long.exp` run with "Run exactly this bash command once:
echo M3-probe-interactive …" (expect wait 1200 s).

**Observed** (fixture [`testdata/fixtures/mods/tool-check-event.json`](../testdata/fixtures/mods/tool-check-event.json)):

| | `-p` | terminal |
|---|---|---|
| polls (25 × 204, 1 × 200) | 26 | 26 |
| hook lifetime until it returned | **652 460 ms** | **652 412 ms** |
| `next.budget.ms` / `remainingMs` at entry | 10 000 / 10 000 | 10 000 / 9 999 |
| `remainingMs` after the last poll | 9 954 | 9 952 |
| `next.signal.aborted` on any poll | never | never |
| `.catch` raised | no | no |
| outcome | Bash ran → `tool_result` `M3-probe-ok`; `result.subtype: success`, `duration_ms: 657170`, `permission_denials: []` | Bash ran (`stdout: "M3-probe-interactive"`), answer `SPIKE-ANSWER: Red` |
| what the person saw while waiting | — | the tool row with its spinner (`Running 1 shell command… ⎿ $ echo M3-probe-interactive`). **No permission dialog**, as with E4 in `hooks-format.md` |

**Why it survives.** `HookBudget.ms` = 10 000 counts only the hook's *own* code. "The clock
stops while a `next(e)` call or any `$` call of the hook's is in flight (a `$.clock` wait
excepted)." Over 652 s the loop spent 46–48 ms of its budget. A loop that **slept** with
`$.clock.sleep` between short polls would pay for every sleep and die at 10 s, so the wait
must stay inside `$.http.fetch` (a server-side long-poll). The spike saw no other
wall-clock bound on `tool.check` up to 652 s. A bound longer than that, if one exists, was
not probed.

**`e` fields** (`Object.keys(e)` = `["tool", "input", "tool_use_id"]`):
`{ tool: "Bash", input: { command, description }, tool_use_id: "toolu_01KhJeH7…" }`. The call
id is **`e.tool_use_id`**: there is no `e.toolUseId` or `e.id`. `agentId` appears only inside a
subagent's loop, and `ceiling` only where an organization set one (neither was present here).
`tool.check` fired in **both** `-p` and the terminal. In `-p` this is a channel the
`PermissionRequest` hook never had (it does not fire under `-p`, `hooks-format.md`).

**Not probed:** the person pressing Esc mid-wait. `next.signal` should abort and the hook
gets `lingerMs` = 5 000 to return. But `$.http.fetch` takes no `signal`, so an in-flight
long-poll runs on to its server-side end, and the engine reports the hook as lingering.
The dispatch has already moved on by then.

**Verdict: M3 verified.** A `tool.check` hook can wait past the whole 10-min approval window
by chaining server-held long-polls. Its 10 s budget is not consumed.

## M4 — answering `AskUserQuestion` from a `tool.call` hook

**Command.** `interactive.exp` (terminal; `-p` has no one to ask). The prompt asks the model
to use AskUserQuestion with "Which color do you prefer?" / Red / Blue, then reply
`SPIKE-ANSWER: <choice>`. The hook:

```ts
on('tool.call', { tool: 'AskUserQuestion' }, async ($, e) => {
  const answers: Record<string, string> = {}
  for (const q of e.questions) answers[q.question] = q.options[0]?.label ?? 'yes'
  return { result: { questions: e.questions, answers } }      // no next(e)
})
```

**Transcript (PTY, ANSI stripped):**

```
❯ Use the AskUserQuestion tool to ask me exactly one question, 'Which color do
  you prefer?', with the two options Red and Blue. After you get my answer,
  reply with one line: SPIKE-ANSWER: followed by the option I chose.
⏺ User answered Claude's questions:
  ⎿  · Which color do you prefer? → Red
⏺ SPIKE-ANSWER: Red
✻ Cogitated for 5s
```

`e` as the hook received it:
`{ questions: [{ question, header: "Color", multiSelect: false, options: [{ label, description }] }], tool: "AskUserQuestion", tool_use_id: "toolu_01SiRX…" }`.
No dialog was drawn and `tool.check` did not fire for the call: a `tool.call` answered
without `next` never reaches the permission stage. The `turn.complete` that followed has
`answer: "SPIKE-ANSWER: Red"` (fixture `turn-complete-event.json` → `interactiveWithTool`).

**Verdict: M4 verified: `{ result }` on AskUserQuestion is accepted.** The engine renders
it as the user's own answer and the model acts on it. That is the mechanism SC-7
("answer from the dashboard") needs. The hook can wait for the dashboard's answer before
it returns (same waiting rules as M3), then return `{ result: { questions, answers } }`.
`answers` maps question text to label; multi-select answers are comma-joined.

## M5 — hooks × surface

**Commands.** Terminal: `interactive.exp`. `-p`: `claude -p --plugin-dir link-spike`
(text, json and stream-json). VS Code: **not available on this host** (no `code` CLI,
no VS Code app, no Claude extension in `~/.vscode/extensions`). The VS Code column below
therefore comes from `claude plugin test` (`hooks/band.test.ts`, which mounts the band on
`terminal`, `desktop`, `vscode` and `mobile` → 1 pass). That is the engine's validator for
that surface's element table, not a live panel.

| Hook / capability | terminal (interactive) | `-p` (headless) | VS Code chat panel |
|---|---|---|---|
| `session.start` | ✅ `surface: "terminal"`, `isInteractive: true` | ✅ `surface: null`, `isInteractive: false` | not observed — operator check |
| `prompt.submit` | ✅ `origin.kind: "composer"` | ✅ `origin.kind: "sdk"` | not observed — operator check |
| `session.measure` | ✅ once at start (no tokens) + after each turn | ✅ after each turn | not observed — operator check |
| `turn.complete` | ✅ | ✅ | not observed — operator check |
| `tool.call` | ✅ | ✅ | not observed — operator check |
| `tool.check` | ✅ fires for the model's Bash call, ahead of any dialog (M3 interactive) | ✅ fires for the model's Bash call (see M3) | not observed — operator check |
| `ui.render{AbovePrompt}` | ✅ drawn (`link-spike ● terminal [-]`), `viewport: {columns:80, rows:24, isFullscreen:true}` | ❌ never raised (nothing draws) | not observed — operator check (kit: the tree validates on `vscode`) |
| `session.end` | ✅ `reason: "prompt_input_exit"` (`/exit`) | ✅ `reason: "other"` | not observed — operator check |
| `$.http.fetch` | ✅ (every POST reached the stub) | ✅ | not observed — operator check |
| `$.ui.log` | a transcript row `⏺ link-spike: <text>` | stream-json `{"type":"system","subtype":"ui_log","plugin":"link-spike","text":…,"session_id":…}`; nothing in text/json output | not observed — operator check |

**Verdict: M5 verified for terminal and `-p`. VS Code is still open (needs an operator with
VS Code).** Under `-p` every non-drawing hook fires and `$.http.fetch` works, so a
headless phase run *can* carry the link. Only the band is missing, and `$.ui.log` reaches
the SDK host as `ui_log` messages. To close the VS Code column, an operator runs the
prototype in the VS Code Claude panel with `CLAUDE_CODE_PLUGIN_DIRS=<…>/link-spike` set in
`~/.claude/settings.json` `env`, then checks `stub.log` for `"surface": "vscode"` and looks
for the `link-spike ● vscode` band.

## M6 — `claude plugin validate --strict --json`

**Command.** `claude plugin validate --strict --json <spike>/link-spike` → exit 0.
**Fixture:** [`testdata/fixtures/mods/validate-json.json`](../testdata/fixtures/mods/validate-json.json)
holds the real output, with the spike's absolute path replaced by `<spike>`.

Shape:

```
{ success: bool, strict: bool, target: <plugin.json path>,
  manifest: { file, type: "plugin", errors[], warnings[], notes[], gatingHooks[] },
  contents: [ { file: <hooks.json path>, type: "hooks", errors[], warnings[],
                notes: string[],
                gatingHooks: [ { module, pattern, hook, hasCatch } ] } ],
  advice: [] }
```

What the module hooks and calls is reported **only as prose lines in `notes`**, one per
fact, each prefixed with the module path:

| note prefix | example |
|---|---|
| `<module> hooks:` | `./register.tsx hooks: session.start, session.measure, …, tool.call{tool=AskUserQuestion}, tool.check, ui.render{component=AbovePrompt}, session.end` |
| `<module> gating hook with(out) .catch:` | `./register.tsx gating hook with .catch: tool.check` |
| `<module> calls:` | `./register.tsx calls: $.clock.now, $.env.get, $.fs.read, $.http.fetch, …, $.ui.log (via post), $.ui.resolve` |
| `<module> env reads:` / `env writes:` | `./register.tsx env reads: HOME` · `env writes: nothing` |

There are **no structured `events` / `uses` / `env` / `state` keys**, contrary to what the plan
assumed. Only `gatingHooks` is structured. A module that uses no `$.state` gets no `state`
line. Under `--strict` the one warning seen was a missing `author` in `plugin.json`, and
`success` went `false` until it was added.

**Verdict: M6 verified.** Phase 9 parses `contents[].notes` by prefix (`hooks:`, `calls:`,
`env reads:`) and reads `gatingHooks` as structured data. It must not expect top-level
`events`/`uses`.

## M7 — reading the token file

**Setup.** `~/.swarmery/link-token-spike`, mode `0600`, dummy content. The spike used a
`-spike` suffix so it never collides with the real phase-2 file. On `session.start` the
mod reads it twice:

| Call | Result |
|---|---|
| `$.fs.read('~/.swarmery/link-token-spike')` | ❌ `HooksError: link-spike: $.fs.read(<cwd>/~/.swarmery/link-token-spike) failed: ENOENT`. `~` is a **literal path segment**, resolved against the session cwd |
| `$.fs.read(\`${await $.env.get('HOME')}/.swarmery/link-token-spike\`)` | ✅ read, 30 chars (same in `-p`, `--resume` and interactive) |

Validate only lists `$.fs.read` under `calls:` and `HOME` under `env reads:`. It gives **no**
path, **no** warning, and no flag for reading outside the project. Strict validate stayed
`success: true`. The read crossed the project boundary with no prompt in all three modes.

**Verdict: M7 verified.** The working path is
`` `${await $.env.get('HOME')}/.swarmery/link-token` ``, with `HOME` spelled as a literal so
validate lists it. Do not use `~`.

## Design consequences

**(a) Approvals: design A holds (poll loop in `tool.check`). Plan B is not needed.**
M3 kept a `tool.check` hook alive for 652 s in both `-p` and the terminal, past the 600 s
`approvals.DefaultTimeout`, with 46 ms of its 10 s budget spent. Phase 5 builds the mod as
the first channel. Its constraints:
  1. Wait inside `$.http.fetch`, never in `$.clock.sleep`. The daemon's
     `POST /api/link/permission` must **hold** the request (≤ 25 s per call) and answer
     `204` / `200 {decision}`. `HttpInit` has no `timeout`/`signal`, so the hold length is
     the server's to choose.
  2. Check `next.signal.aborted` after every fetch and return `next(e)` on abort.
  3. Stop polling at the daemon's window (`approvalWindow`) and return `next(e)`, so the shim
     or the native dialog takes over.
  4. Keep `.catch(() => next.called ? next(e) : …)` falling through to `next(e)` (fail open
     to the shim, as D3 says), not to `deny`.
  The shim stays as the fallback. The terminal shows only the tool spinner while the mod
  waits, so phase 5 should add a status line (`$.ui.status`) such as "waiting in Swarmery".
  Bonus: `tool.check` fires under `-p` too, so headless phase runs gain dashboard
  approvals, which the `PermissionRequest` shim never had.

**(b) Dedup key for phase 5: `(session_id, tool_use_id)`.** `tool.check`'s `e.tool_use_id`
is present on every real call (`toolu_…`; absent only on a `$.tool.check` *query*). It is
the same id the transcript's `tool_use` block and the `-p` `permission_denials[]` carry.
`$.session.id()` equals the JSONL `sessionId` (M1). The shim's `PermissionRequest` stdin has
no `tool_use_id` (`hooks-format.md` E1), so for the shim-after-mod case the fallback key
`(session, tool, sha256(input), 5 s window)` stays. Match a shim request to an existing
pending mod row by that hash, then carry the row's `tool_use_id`. One caveat: subagent calls
share the parent's session id in the shim (E11), while the mod sees `e.agentId`. Include
`agentId` in the hash for mod rows.

**(c) Plan band in VS Code: not confirmed live.** On this host the band is observed on the
terminal (`link-spike ● terminal`) and is never raised under `-p`. `claude plugin test`
validates the same `AbovePrompt` tree under the `vscode` element table (and `desktop`,
`mobile`), and `AbovePrompt` is a render site the types declare without a per-surface
exception. Whether the VS Code chat panel *loads a `--plugin-dir`/`CLAUDE_CODE_PLUGIN_DIRS`
mod and draws the band* still needs an operator run (see M5). Phase 7 must therefore keep
its degrade path: `/plan` command plus the drift line when no `ui.render{AbovePrompt}` with
`surface: "vscode"` reaches the daemon. It should key the decision on the `surface` value it
observes, never on an assumption.

**(d) Token read path for phase 2:**
`` const home = await $.env.get('HOME'); const token = (await $.fs.read(`${home}/.swarmery/link-token`)).trim() ``
on `session.start`. `~` is **not** expanded: `$.fs.read('~/…')` resolves against the session
cwd and fails `ENOENT`. Spell `HOME` as a string literal (validate lists it under
`env reads: HOME`). Wrap the read in `try`. A missing file rejects with
`HooksError: … failed: ENOENT`, and that is phase 2's "sensor off" path. Validate does **not**
flag a read outside the project: no warning, and `success: true` under `--strict`. The read
crossed the project boundary without a prompt in `-p`, `--resume` and interactive sessions.

**Also for the later phases:**
- Phase 1/2: `cost.usd` is **per process**, not per session lifetime (it restarted at 0 on
  `--resume`). `startedAt` is the session's first launch.
- Phase 1/2: `session.measure` and `turn.complete` arrive in no fixed order. Interactive
  sessions raise one measure before any prompt, with no `tokens`.
- Phase 2 under `-p`: `$.ui.log` is emitted on stream-json as `system/ui_log`
  (`plugin`, `text`, `session_id`), a channel the daemon could tail without the HTTP link.
- Phase 9: parse `validate --json` `contents[].notes` by prefix. There are no
  `events`/`uses`/`env`/`state` keys.
- Phase 5 / SC-7: `tool.call{AskUserQuestion}` returning `{ result: { questions, answers } }`
  is shown as the user's answer and the model acts on it (M4).
