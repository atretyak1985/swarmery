# Hooks HTTP protocol — daemon ↔ `swarmery hook` shim

**Status: FROZEN at gate 2.2** (Phase 2 contract freeze). This document defines the
HTTP contract shared by the `swarmery hook` shim (installed into projects as a
Claude Code `PermissionRequest` / `Stop` hook) and the swarmery daemon. Both sides
are implemented against this text at steps 2.3/2.4; changes go through
`web/CONTRACT-REQUESTS.md`, never directly to this file while the parallel wave runs.

Every behavior below is grounded in the live spike
[`hooks-format.md`](hooks-format.md) (Claude Code `2.1.170`) — experiment numbers
(E1…E11, Q-A) refer to that document. Design decisions D3/D6 come from
[`plan/phase-2-approvals/00-phase-2-plan.md`](plan/phase-2-approvals/00-phase-2-plan.md).

## Transport

- Base URL: the daemon's REST origin (default `http://127.0.0.1:7777`).
- Both endpoints are localhost-trust like the rest of the API (D4): no auth in v1;
  state-changing endpoints reject cross-origin browser `Origin` headers. The shim
  sends no `Origin`.
- Errors follow the existing API convention: non-2xx with `{"error": string}`.
  The shim treats **any** transport or non-contract response as fail-open (below).

## POST /api/hooks/permission-request

The approval channel. The shim is invoked by Claude Code as a `PermissionRequest`
hook, reads the hook stdin, and forwards it while **blocking** (long-poll) until a
human decision or expiry.

### Request

- Body: the `PermissionRequest` hook **stdin, verbatim** — an unmodified
  pass-through of the JSON Claude Code pipes in (E1 fixture: `session_id`,
  `transcript_path`, `cwd`, `permission_mode`, `effort`, `hook_event_name`,
  `tool_name`, `tool_input`, `permission_suggestions`).
- `Content-Type: application/json`.
- **The daemon mints the request identity.** The hook stdin carries **no
  `tool_use_id`** (E1), and parallel subagents share the parent's `session_id`
  (E11) — so nothing in the payload uniquely identifies a pending request. The
  daemon creates the `permission_requests` row and its `id` is the approval
  identity used by the dashboard, the WS messages, and the audit history.

### Response (long-poll)

The daemon holds the request open until a decision, then answers:

| Status | Body | Shim behavior |
|---|---|---|
| `200` | `{"decision": "allow" \| "deny", "message"?: string}` | Map to Claude's hook stdout (below), exit 0. |
| `204` (no body) | — | No decision (expired / resolved elsewhere / daemon declines). Exit 0 **with no stdout**. |
| anything else / timeout / connect failure | — | Exit 0 **with no stdout**. |

Shim stdout mapping for `200` (verified decision contract, E2/E3):

```json
{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}
```

```json
{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"<message>"}}}
```

`message` is only meaningful for `deny`; it reaches Claude **verbatim** as the tool
result (E3) — this is how the human's deny reason gets to the agent.

### Fail-open (D3, verified E5/E6/E7)

**No decision, never allow.** On daemon-down, connect timeout, non-200/204, malformed
body, or long-poll expiry, the shim exits `0` with **no stdout** — Claude Code then
falls back to its native terminal permission dialog (E5). A crashing shim (non-zero
exit, garbage stdout) lands on the same dialog and never bricks the session (E7).

### Timing (Q-A, verified E6)

| Knob | Value | Why |
|---|---|---|
| Shim connect timeout | **500 ms** | A dead daemon adds ≤ 1 s before the native dialog (D3). |
| Shim long-poll / daemon `approval_timeout` | **≤ 120 s** (default 120 s, config flag) | The shim owns expiry: at 120 s it gives up and exits 0 silent → clean fail-open (E5), not a hard kill. |
| Installed hook config `"timeout"` | **130** | Claude Code's per-hook `timeout` **kills** the shim process (E6) and its documented default is 60 s — shorter than the poll. The installer MUST set it explicitly to `approval_timeout` + margin so Claude never cuts the poll short; the fallback path stays the shim's own silent exit. |

While the shim polls, the terminal shows only a spinner — no dialog and no countdown
(E4); there is no terminal-side race, the hook is authoritative for its lifetime.
The shim SHOULD print a one-line `waiting for remote approval…` notice to stderr so
the operator isn't staring at a bare spinner (Q-A recommendation).

### Deduplication (D6, motivated by E11)

Concurrent identical requests are a live scenario: parallel subagents fire concurrent
hooks that share the parent's `session_id` (E11 observed two identical requests
0.24 s apart). Rule, frozen:

```
dedup_hash = hex(SHA-256(
    session_id + "\n" + tool_name + "\n" + canonical_json(tool_input)
))
```

- `session_id` / `tool_name` / `tool_input` come from the hook stdin.
- `canonical_json` = the JSON value re-serialized with object keys sorted
  lexicographically (byte order) at every nesting level, no insignificant
  whitespace, UTF-8. Arrays keep their order.
- Scope: the hash is compared **only against rows with `status = 'pending'`**
  (partial index `idx_pr_dedup`, migration 0007). Resolved/expired rows never
  absorb a new request — a repeat of a previously-denied call opens a fresh row.
- On a match, the incoming caller **attaches to the existing pending row** —
  no new row, no new WS `permission_requested`. When the decision arrives, the
  daemon fans it out to **all** attached long-poll waiters: both callers get the
  one decision, one audit row records it.

### Side effects (daemon)

On a **new** (non-dedup) request the daemon: inserts the `permission_requests`
row (`status = 'pending'`, `expires_at = requested_at + approval_timeout`), inserts
a `permission_request` event, flips the session to `waiting_approval`, and pushes
WS `permission_requested`. On resolution (any outcome, incl. `expired` /
`resolved_elsewhere`): updates the row, inserts a `permission_resolved` event,
recomputes the session status, pushes WS `permission_resolved`. A long-poll client
disconnect (e.g. terminal Esc/Ctrl-C killing the shim) resolves the row as
`resolved_elsewhere` once no waiters remain.

## POST /api/hooks/stop

Heartbeat + phase-2.5 readiness channel (Reporter). Phase 2 does nothing with the
payload beyond recording liveness.

- Body: the `Stop` hook **stdin, verbatim** (E9 fixture: `session_id`,
  `transcript_path`, `cwd`, `hook_event_name`, `stop_hook_active`,
  `last_assistant_message`, `background_tasks`, `session_crons`). Fires once per
  completed assistant turn (E9).
- Response: **always `202`**, empty body, immediately (no long-poll). The shim
  exits 0 with no stdout regardless of the outcome (same fail-open posture).
- Side effect: updates the hooks heartbeat surfaced as `hooks_last_seen` in
  `GET /api/health` (`HealthResponse`, additive optional field). Both hook
  endpoints refresh the heartbeat.

## Known boundary — headless sessions

**`claude -p` (print/headless) sessions never fire the `PermissionRequest` hook**
(spike headline finding): with no TTY there is no dialog to intercept; un-allowlisted
calls are auto-denied and surface only in the `-p` JSON `permission_denials[]`.
Remote approval is therefore a property of **interactive** sessions only — batch
runs are invisible to this channel by design. (The `Stop` hook does fire in `-p`,
so headless sessions still heartbeat.)

## Version fragility

The entire `PermissionRequest` contract is undocumented upstream and verified only
on Claude Code `2.1.170` (see [`hooks-format.md`](hooks-format.md) §Version-fragility).
Re-run the spike harness on Claude Code minor bumps before trusting the approvals
path.
