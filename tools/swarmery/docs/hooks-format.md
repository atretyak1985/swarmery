# Claude Code `PermissionRequest` / `Stop` hook contract — observed spec

**Status:** spike result (Phase 2, Step 2.1). Everything below is evidence from a live
throwaway harness on this machine — nothing is taken from memory or third-party docs.
Where something was *not* observed, it says so explicitly. This is the phase-2 analogue
of [`jsonl-format.md`](jsonl-format.md); its job is to de-risk decisions **D1**
(PermissionRequest as the approval channel) and **D3** (fail-open) before any code is
written on top of them.

**Verdict up front:** **D1 and D3 both HOLD.** No STOP-fact. See
[Contract for phase 2](#contract-for-phase-2).

## Evidence base

- **Claude Code version:** `2.1.170 (Claude Code)` — macOS 26.3 (arm64).
- **Model in session:** `Fable 5` (`claude-fable-5[1m]`), effort `high` then `low`.
- **Harness** (throwaway, `/tmp/swarmery-spike/`, never committed):
  - `hook.sh` — dumps stdin to `dumps/in-<ts>-<pid>-<event>.json`, then acts on a
    mode-switcher file `mode` with one of: `allow | deny | sleep-N | exit0-silent |
    exit1 | garbage-json`.
  - Test project `/tmp/swarmery-spike/proj/.claude/settings.local.json` registering a
    `PermissionRequest` hook (matcher `"*"`) and a `Stop` hook, both pointing at
    `hook.sh`, plus `permissions.allow: ["Bash(echo *)"]` (for E8).
  - Interactive sessions driven over a PTY with `expect` (no `tmux` available on this
    box); headless probes with `claude -p … --output-format json`.
- **Un-allowlisted trigger:** `curl -sI https://example.com | head -1` (Bash) and an
  `Edit` on `notes.txt`. `echo …` is the allowlisted trigger for E8.

> **Secret gate:** all fixtures below are anonymized (session UUIDs → `<sid>`, home dir
> → `<home>`, username removed). `grep -rinE 'api[_-]?key|sk-ant|ghp_|AKIA|password|secret|token'`
> over the raw dumps returned **0 hits** (token counts in `-p` JSON excluded).

## Headline finding that shapes the whole harness

**`PermissionRequest` does NOT fire in headless `claude -p` (print) mode.** In `-p`
there is no TTY on which a permission dialog could be shown, so Claude Code resolves
permissions without ever emitting the event: an un-allowlisted `curl` is simply
**auto-denied** (surfaced in the `-p` JSON `permission_denials[]`, each entry carrying
`tool_name`, `tool_use_id`, `tool_input`) and only the `Stop` hook fires. **Consequence
for swarmery:** the approval channel is a property of *interactive* sessions only; the
daemon will never receive a `permission-request` from a `-p` batch run. All E-experiments
that need the event therefore ran in a real interactive PTY.

## Experiment → observed behavior

| # | Experiment | Observed | Status |
|---|---|---|---|
| E1 | stdin shape (Bash / Edit) | Full JSON captured. Keys: `session_id`, `transcript_path`, `cwd`, `permission_mode`, `effort`, `hook_event_name`, `tool_name`, `tool_input`, `permission_suggestions`. **No `tool_use_id`** in the hook stdin (it exists only in the `-p` `permission_denials[]`). `permission_suggestions` present for both. | ✅ verified |
| E2 | `allow` → dialog suppressed, tool runs | No terminal dialog; `curl` executed, returned `HTTP/2 200`; TUI annotates the tool call `Allowed by PermissionRequest hook`. | ✅ verified |
| E3 | `deny` → does the reason reach Claude? | No dialog; tool blocked; TUI annotates `Denied by PermissionRequest hook`. The `decision.message` string is delivered to Claude **verbatim** as the tool result (marker `SPIKE-DENY-MARKER-7391` echoed back in the assistant's reply). | ✅ verified |
| E4 | dialog visibility during a running hook / terminal race | While a hook runs (incl. `sleep-30`), **no terminal dialog is shown** — only the normal "working" spinner. The dialog appears *only after* the hook exits without a decision. There is therefore **no way for the terminal to answer "first"**: the hook is authoritative for its whole lifetime. Kill-on-terminal-answer is **N/A** by construction (see E6 for the timeout path). | ✅ verified (race is structurally impossible; see note) |
| E5 | `exit0-silent` (exit 0, no stdout) → native dialog? | **Native `Do you want to proceed?` dialog appears** (options `1. Yes` / `2. Yes, and don't ask again` / `3. No`), no hook annotation. This is the **D3 fail-open path** and it works. | ✅ verified |
| E6 | hook `"timeout": 5` + `sleep-30` → dialog after timeout, session alive? | The `timeout: 5` **killed** the `sleep-30` hook at ~5 s (hook's post-sleep "still-alive" log line never wrote). The native `Do you want to proceed?` dialog then appeared, the tool ran (`HTTP/2 200`) and the turn completed (`Stop` fired) — **session survived**. So Claude Code's per-hook `timeout` is the real bound; on kill it fails open to the dialog. | ✅ verified |
| E7 | `exit1` and `garbage-json` → non-blocking, dialog appears? | Both are **non-blocking**: `exit1` (stderr, code 1) → native dialog appears; invalid JSON on stdout (exit 0) → native dialog appears. Neither bricks the session. | ✅ verified |
| E8 | allowlisted `Bash(echo *)` in `permissions.allow` → hook must NOT fire | **Hook did NOT fire.** An allowlisted `echo` ran with **only** a `Stop` dump — zero `PermissionRequest` events. **This is the load-bearing D1 result and it PASSED.** | ✅ verified (CRITICAL) |
| E9 | `Stop` hook stdin + frequency | stdin captured (fixture below). Carries `last_assistant_message`, `stop_hook_active`, `background_tasks`, `session_crons`. Fires **once at the end of each assistant response turn** (one Stop per completed reply, not one per session). | ✅ verified (frequency: per-turn) |
| E10 | `permission_mode`: `plan` / `acceptEdits` / `bypassPermissions` — when does PR fire? | `stdin.permission_mode` echoes the active mode. **default** → PR fires for un-allowlisted calls. **acceptEdits** → an `Edit` was auto-applied with **no PR** (only `Stop`) — edits are auto-approved, so no human prompt, so no hook. **plan** → PR **still fires**: the model's `curl` attempt during planning fired a PR with `permission_mode: "plan"` (plan mode does not exempt gated tool calls the model tries to run). **bypassPermissions** → session opens with a one-time danger-accept gate (`1. No, exit / 2. Yes, I accept`); once accepted, all checks are bypassed so no per-call dialog → PR suppressed (gate observed; per-call suppression follows from the same "PR = only when a human would be prompted" rule as E8/acceptEdits). | ✅ verified (default/acceptEdits/plan); bypass gate observed |
| E11 | two concurrent dialogs (parallel subagents) — concurrent or serial hooks? | **Concurrent.** Two parallel subagents each running the un-allowlisted `curl` fired **two** `PermissionRequest` hooks **0.24 s apart with distinct pids** (both alive at once). Both stdin payloads carried the **same `session_id`** (the parent's — subagents are NOT given a distinct session id in the hook stdin). | ✅ verified (concurrent) |

### Notes on E4 (terminal race)

The design worried about a race where the human answers the terminal dialog while the
swarmery hook is still long-polling, and whether Claude kills the hook process. Observed
reality removes the race entirely: **Claude shows no answerable dialog while the hook is
alive.** The hook owns the decision for its whole run; the terminal only regains control
*after* the hook exits/ times out. The one remaining terminal-side interrupt is
`Esc`/`Ctrl-C`, which cancels the entire tool turn (not tested here; it aborts the turn
rather than "answering" the pending permission).

## Fixtures (anonymized)

### E1 — `PermissionRequest` stdin, Bash tool (`allow` run)

```json
{
  "session_id": "<sid>",
  "transcript_path": "<home>/.claude/projects/-private-tmp-swarmery-spike-proj/<sid>.jsonl",
  "cwd": "/private/tmp/swarmery-spike/proj",
  "permission_mode": "default",
  "effort": { "level": "high" },
  "hook_event_name": "PermissionRequest",
  "tool_name": "Bash",
  "tool_input": {
    "command": "curl -sI https://example.com | head -1",
    "description": "Fetch HTTP status line from example.com"
  },
  "permission_suggestions": [
    {
      "type": "addRules",
      "rules": [
        { "toolName": "Bash", "ruleContent": "curl -sI https://example.com" }
      ],
      "behavior": "allow",
      "destination": "localSettings"
    }
  ]
}
```

### E1 — `PermissionRequest` stdin, Edit tool

```json
{
  "session_id": "<sid>",
  "transcript_path": "<home>/.claude/projects/-private-tmp-swarmery-spike-proj/<sid>.jsonl",
  "cwd": "/private/tmp/swarmery-spike/proj",
  "permission_mode": "default",
  "effort": { "level": "high" },
  "hook_event_name": "PermissionRequest",
  "tool_name": "Edit",
  "tool_input": {
    "file_path": "/private/tmp/swarmery-spike/proj/notes.txt",
    "old_string": "line two",
    "new_string": "EDITED-BY-SPIKE",
    "replace_all": false
  },
  "permission_suggestions": [
    { "type": "setMode", "mode": "acceptEdits", "destination": "session" }
  ]
}
```

> `permission_suggestions` is tool-shaped: for Bash it proposes an `addRules` allowlist
> entry (`destination: localSettings`); for Edit it proposes `setMode: acceptEdits`
> (`destination: session`). This is exactly the payload Q-B ("always allow") would need,
> and it is safe to persist verbatim in `request_json`.

### Hook stdout — the decision contract (verified working)

Allow (E2):

```json
{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}
```

Deny with reason (E3) — the reason key is **`message`**, and it reaches Claude verbatim:

```json
{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"<reason text>"}}}
```

Fail-open (E5/E6/E7): exit `0` with **no stdout** (or non-zero, or invalid JSON) → Claude
falls back to the native terminal dialog.

### E9 — `Stop` hook stdin

```json
{
  "session_id": "<sid>",
  "transcript_path": "<home>/.claude/projects/-private-tmp-swarmery-spike-proj/<sid>.jsonl",
  "cwd": "/private/tmp/swarmery-spike/proj",
  "permission_mode": "auto",
  "effort": { "level": "high" },
  "hook_event_name": "Stop",
  "stop_hook_active": false,
  "last_assistant_message": "The command ran successfully. Output of ...",
  "background_tasks": [],
  "session_crons": []
}
```

> Phase-2.5 input: `last_assistant_message` is the full final assistant text of the turn,
> and `transcript_path` points at the JSONL — so the Reporter can read either the live
> summary or the whole transcript. `Stop` fires per completed response turn.

## Contract for phase 2

### D1 — `PermissionRequest` (not `PreToolUse`) as the approval channel → **HOLDS**

- **E8 is the decisive test and it passed:** an allowlisted call (`Bash(echo *)`) fires
  **no** `PermissionRequest` — only `Stop`. The hook fires on exactly the set of calls a
  human would otherwise be prompted about, and on nothing else (re-confirmed by E10:
  `acceptEdits` auto-approves an Edit → no PR; `plan` still fires for a gated Bash call).
  This is the whole reason D1 was chosen over `PreToolUse` (which fires on every matching
  call), and it is confirmed on the wire. **No daemon-side allowlist mirror is needed.**
- The event is interactive-only (no `-p` firing) — acceptable, since remote approval only
  makes sense for interactive/long-running sessions anyway. **Backlog note:** batch `-p`
  runs are invisible to the approvals channel by design.
- The decision contract is exactly as documented: `hookSpecificOutput.decision.behavior:
  allow|deny`, and `deny` carries a human-visible reason via **`decision.message`** that
  reaches Claude verbatim (E3) — the daemon can surface the human's deny reason to the
  agent.
- **Request identity (affects D6 dedup + the pending registry):** the PR stdin carries
  **no `tool_use_id`**, and **parallel subagents share the parent's `session_id`** (E11).
  So `session_id` alone is NOT a unique pending key — the daemon MUST mint its own request
  id (`permission_requests.id`, as designed) and the concurrency is real: E11 shows two
  identical `curl` requests arriving ~0.24 s apart under one session. This is exactly the
  D6/`dedup_hash` "identical concurrent requests collapse to one pending row, both callers
  get the one decision" case — it is a live scenario, not a theoretical one, and the
  pending registry must be keyed by `(session_id, dedup_hash)` with a fan-out of waiters.

### D3 — fail-open = "no decision, never allow" → **HOLDS**

- **E5 (exit 0, no stdout) → the native permission dialog appears.** This is the exact
  swarmery fail-open path (daemon down/error/expiry → shim exits 0 silently), and Claude
  falls back to the normal terminal prompt. **No STOP-fact.**
- `exit1` and `garbage-json` (E7) are also non-blocking and land on the same native
  dialog, so a crashing/ misbehaving shim degrades safely to the terminal, never to a
  silent auto-allow and never to a bricked session.

### Q-A — default `approval_timeout` and terminal UX during the wait

- **During the hook's run the terminal shows only a spinner; no dialog and no countdown
  are visible** (E4). The user at the terminal cannot act until the hook exits. So the
  "wait" is invisible unless swarmery itself prints something — the daemon/shim should
  consider emitting a one-line "waiting for remote approval…" notice to stderr so the
  terminal operator isn't staring at a bare spinner.
- **The binding timeout is Claude Code's per-hook `timeout` (E6 verified), not swarmery's
  long-poll.** With `timeout: 5` + a 30 s hook, Claude killed the hook at ~5 s, showed the
  native dialog, and the session stayed alive (the tool then ran and `Stop` fired). So the
  design's long-poll window MUST be shorter than — or the hook config `timeout` MUST be set
  to at least — the intended `approval_timeout`, otherwise Claude kills the shim mid-poll
  and drops to the terminal dialog prematurely. **Recommendation:** set the
  `PermissionRequest` hook `timeout` in the installed config to `approval_timeout + a small
  margin` (e.g. plan's 120 s poll → hook `timeout: 130`), and let the *shim* own expiry
  (exit-0-silent at 120 s) so the fallback is a clean fail-open (E5) rather than a hard
  kill. Claude Code's *documented* default per-hook timeout is 60 s (not re-measured in
  this spike) — so a shim that long-polls 120 s with no explicit `timeout` set would be at
  risk of being cut off; always set the hook `timeout` explicitly.

### Version-fragility

`PermissionRequest` semantics (stdin keys, the `decision.message` deny channel, the
no-`-p` behavior, the per-hook `timeout` kill) are all **undocumented** and were verified
only on `2.1.170`. Add the same format-change watch as the JSONL spike: re-run this
harness on Claude Code minor bumps before trusting the approvals path in production.

## NEEDS HUMAN CONFIRMATION

All eleven E-experiments were verified under automation (see the table). One secondary
behavior was *not* exercised and is left as a 5-minute protocol for the owner:

### E4-interrupt — terminal Esc/Ctrl-C during a pending hook

While a hook is in flight the terminal shows only a spinner (E4), so the sole terminal-side
control is an interrupt. This spike did not press it. Confirm it degrades cleanly:

```
echo sleep-30 > /tmp/swarmery-spike/mode      # (recreate harness from this doc if cleaned)
cd /tmp/swarmery-spike/proj
claude --permission-mode default
> Run this bash command now: curl -sI https://example.com | head -1
# While the spinner shows (hook sleeping 30 s), press Esc once.
# Confirm: the turn aborts cleanly and the session stays alive (expected yes), and the
# hook process is either killed or its late stdout ignored (check /tmp/swarmery-spike/hook.log
# for a "woke ... still-alive" line written AFTER you interrupted).
```

This matters for the D3 `resolved_elsewhere` path: if a terminal interrupt during a
long-poll leaves the shim's HTTP request dangling, the daemon should treat the client
disconnect as `resolved_elsewhere` and stop waiting.
