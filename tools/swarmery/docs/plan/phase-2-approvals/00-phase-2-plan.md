# Swarmery Phase 2 — Approvals + Hooks (implementation plan)

> Design doc §3.2 (Approvals UI), §2 `permission_requests` DDL, §4 п.2. Sits between
> the shipped MVP ([../00-plan.md](../00-plan.md)) and Phase 2.5 Reporter
> ([../phase-2.5-reporter-agent-d.md](../phase-2.5-reporter-agent-d.md), which
> depends on the Stop-hook channel installed here). Backlog input:
> [../phase2-backlog.md](../phase2-backlog.md). Same execution rules as the MVP
> plan: follow steps in order, gates MUST pass, agent prompts are copy-paste-ready.

## Scope

Remote approval of Claude Code permission prompts from the Swarmery dashboard:
a hook installed into projects forwards permission dialogs to the daemon, which
records a `permission_requests` row, flips the session to `waiting_approval`,
pushes it live over WS, and **blocks (long-poll)** until a human decision on the
Approvals screen — or falls back to the normal terminal dialog on timeout/daemon-down.
Plus: `swarmery hooks install|uninstall|status`, Stop-hook channel (phase-2.5
readiness), heartbeat in `/api/health`, and the Approvals screen with audit history.

Out of scope: Inject-instruction/Kill session actions (design §3.3), tasks queue,
notification push to mobile, LAN/remote auth (open question Q-D below).

## Key design decisions (made in this plan — verify D1 at step 2.1)

| # | Decision | Why |
|---|---|---|
| D1 | **`PermissionRequest` hook, not `PreToolUse`**, is the approval channel. Verified against code.claude.com/docs/en/hooks (2026-07): it fires **only when a permission dialog is about to be shown** and returns `hookSpecificOutput.decision.behavior: allow\|deny`. | PreToolUse fires on *every* matching tool call — long-polling there would gate allowlisted calls (e.g. `Bash(npm test)`) for minutes and require a daemon-side policy engine mirroring the user's allowlist. PermissionRequest mirrors exactly the set of calls a human would be asked about anyway. Its timeout/fallback semantics are **undocumented** → spike step 2.1 verifies empirically before anything is built. |
| D2 | Hook config placement: **per-project `.claude/settings.local.json`** by default; `--all` iterates the daemon's `projects` table; user-level `~/.claude/settings.json` only behind explicit `--user` + interactive confirmation. | Hook entries carry a machine-local absolute binary path — committing them (`settings.json`) would break teammates without swarmery; `settings.local.json` is the not-shared/gitignored tier by Claude Code convention. Preserves the MVP rule "never touch `~/.claude/settings.json` without explicit consent". |
| D3 | **Fail-open = "no decision", never "allow"**: on daemon-down / error / expiry the `swarmery hook` shim exits 0 with **no stdout** → Claude Code falls back to its normal permission dialog. Connect timeout 500 ms so a dead daemon adds ≤1 s. | Hook must not brick sessions (backlog rule), but silent auto-allow of dangerous ops on failure is worse. "No output = normal flow" is the documented safe fallback for hooks. |
| D4 | Localhost auth: **none** (v1), but bind `127.0.0.1` explicitly (today `serve` binds `:{port}` = all interfaces — hardened in 2.3) and reject browser cross-origin `Origin` on state-changing endpoints. | Same trust boundary as the existing unauthenticated REST/WS API on the same box; the Origin check + localhost bind close the DNS-rebinding/CSRF hole that write endpoints would otherwise open. LAN/mobile exposure → Q-D. |
| D5 | **WS contract change** (frozen at gate 2.2): add `permission_requested` \| `permission_resolved` message types + `PermissionRequest` DTO to `web/src/api/types.ts` and `docs/ws-protocol.md`. The three MVP names stay byte-identical. | Approvals screen and nav badge need push updates; reusing `event_appended` would leak DB event ids as the approval identity. |
| D6 | Migration `0006` is **additive-only**: `permission_requests` already exists (0001, design §2 verbatim); add columns `dedup_hash`, `expires_at`, `reason` + index. `permission_request`/`permission_resolved` event types already exist in the schema enum. | MVP additive-only rule (internal/store/migrations). |

## Phases & steps

| Step | What | Branch | Estimate |
|---|---|---|---|
| [2.1](step-2.1-hooks-spike.md) | Hooks spike — live-verify the PermissionRequest/Stop contract, write `docs/hooks-format.md` | main (docs only) | 0.5 day (MEDIUM — analogous to MVP step 02 JSONL spike) |
| [2.2 GATE](step-2.2-quality-gate-contract-freeze.md) | Human review of spike findings + **contract freeze commit** (types.ts, ws-protocol.md, hooks-protocol.md, migration 0006 text) | main | 2–3 h (MEDIUM) |
| [2.3](step-2.3-agent-a-hooks-backend.md) | Agent A: daemon endpoints (long-poll), `swarmery hook` shim, `swarmery hooks` installer, statuses, WS, heartbeat | feat/swarmery-hooks (worktree) | 1 session, 4–6 h ([LOW-CONFIDENCE] — long-poll concurrency is new territory) |
| [2.4](step-2.4-agent-b-approvals-ui.md) | Agent B: Approvals screen, nav badge, history, Overview wiring — against frozen contract + mocks | feat/swarmery-approvals-ui (worktree) | 1 session, 3–4 h (MEDIUM — analogous to MVP step 07) |
| [2.5 GATE](step-2.5-quality-gate-parallel-wave.md) | Both branches green, screenshots reviewed, contract respected | — | 1–2 h |
| [2.6](step-2.6-integration-live-test.md) | Merge (backend → frontend), end-to-end wiring, **live test protocol** | main | 0.5 day (MEDIUM) |
| [2.7 GATE](step-2.7-quality-gate-ship.md) | Ship & dogfood: live protocol pass, rollback drill (`hooks uninstall`), backlog capture | — | 1–2 h |

**Total: ~2.5–3 working days wall-clock** (basis: measured MVP throughput — the MVP
of 12 steps shipped in ~1 day of parallel agent sessions; this phase is ~½ the MVP
surface but has one [LOW-CONFIDENCE] unknown: hook long-poll semantics).

```
2.1 spike ─► [GATE 2.2: contract freeze] ─► 2.3 A (backend) ┐
                                            2.4 B (frontend) ┴─► [GATE 2.5] ─► 2.6 integrate ─► [GATE 2.7: ship]
```

## Architecture overview

```
claude (terminal)                          swarmery daemon (localhost:7777)
  PermissionRequest hook fires             ┌──────────────────────────────────┐
  → `swarmery hook permission-request`     │ POST /api/hooks/permission-request│
    reads stdin JSON, POSTs ───────────────►  insert permission_requests row   │
    … blocks (long-poll ≤120 s) …          │  + events row (permission_request)│
                                           │  + session → waiting_approval     │
  ◄── {"decision":"allow"} ────────────────│  + WS permission_requested ───────┼──► Approvals screen
    → stdout hookSpecificOutput JSON       │  … waits on decision channel …    │      Approve / Deny
  (or no output → normal dialog)           │ POST /api/approvals/{id} ◄────────┼──── dashboard
                                           │  resolve + events permission_     │
  Stop hook → `swarmery hook stop` ────────►  resolved + WS + status recompute │
             (202, heartbeat; 2.5-ready)   └──────────────────────────────────┘
```

- **Backend** (2.3): `internal/approvals` (store + pending registry + expiry sweeper),
  `internal/api` gets its first write endpoints, `internal/installer` reused pattern
  for `hooks` CLI, `cmd/swarmery` two new subcommands.
- **Status interplay**: the ticker already skips `waiting_approval`
  (`internal/ingest/status.go` selects `status IN ('active','idle')`) — no ticker
  change; the approvals layer sets/clears the status and emits `session_updated`.
- **Frontend** (2.4): `web/src/pages/Approvals.tsx`, nav badge in `App.tsx`,
  new WS types in `lib/ws.ts`, mocks in `mock/`.

## Quality gates

| Gate | Step | What it protects |
|---|---|---|
| Contract freeze (human, critical) | 2.2 | Undocumented hook semantics verified empirically before any code; HTTP/WS/types.ts frozen before parallel work |
| Parallel-wave verification | 2.5 | Both branches green + UI screenshots reviewed before merge |
| Ship & dogfood | 2.7 | Live end-to-end protocol incl. daemon-down fail-open and rollback drill |

## Success criteria (phase done)

- [ ] Un-allowlisted tool call in a hooked project appears as pending on the Approvals screen < 2 s (WS, no reload)
- [ ] Approve on the dashboard → the terminal tool call proceeds without a terminal dialog; Deny → Claude receives the deny reason
- [ ] Pending request → session card shows `waiting_approval` (amber); resolution returns it to the heuristic status
- [ ] Daemon stopped → hooked session's permission dialog appears normally with ≤ 1 s added latency (fail-open, live test #4)
- [ ] Identical concurrent requests dedup to one pending row; both callers get the one decision
- [ ] `swarmery hooks install` is idempotent (second run = no diff); `uninstall` removes only swarmery entries; non-swarmery settings survive byte-for-byte
- [ ] Audit history lists resolved requests with `resolved_via`; `/api/health` shows hooks last-seen heartbeat
- [ ] Repeated migration run idempotent; MVP WS message names byte-identical; `go vet` + `go test -race` + `npm run build` green

## Top risks

| Risk | L | I | Mitigation |
|---|---|---|---|
| PermissionRequest semantics undocumented (dialog visibility during hook, timeout fallback, terminal-race) | High | High | Spike-first (2.1) with a live harness; gate 2.2 blocks until verified; PreToolUse+`ask` documented as fallback plan in 2.1 |
| Hook bricks claude sessions when daemon down/slow | Med | High | D3 fail-open, 500 ms connect timeout, live test #4 is a hard gate criterion |
| Contract drift across the 2 parallel branches | Med | Med | Gate 2.2 freeze, `web/CONTRACT-REQUESTS.md`, merge order backend→frontend |
| Long-poll concurrency (goroutine leaks, lost wakeups, client disconnects) | Med | Med | `httptest` + `-race` tests mandated in 2.3; disconnect → `resolved_elsewhere` path tested |
| Installer corrupts user settings JSON | Low | High | Parse-fail = abort without writing; `.bak` before first write; idempotency round-trip tests |
| CSRF/DNS-rebinding against new write endpoints | Low | Med | D4: localhost bind + Origin check |

## Open questions (owner input)

- **Q-A**: default `approval_timeout` — plan says 120 s (config flag). Longer means more remote-approval wins but a longer wait before the terminal fallback dialog (exact terminal UX during the wait is a 2.1 spike finding).
- **Q-B**: should Approve offer "always allow" (the hook's `permission_suggestions` — write a permissions rule back)? Deferred; plan records suggestions in `request_json` payload so the UI can add it later without schema change.
- **Q-C**: mobile swipe actions (design §3.2) are a stretch item in 2.4 — ship or drop?
- **Q-D**: LAN/mobile access needs a bind flag + token auth — this phase hardens to localhost-only; schedule auth for a later phase?

## How to use

Same as the MVP plan: paste each step's **Agent Prompt** verbatim into a fresh
Claude Code session in the stated directory; humans run the GATE checklists; a red
gate stops the phase; executors append Completion Reports to their step file.

## Progress checklist

- [x] 2.1 [x] 2.2-GATE [ ] 2.3 [ ] 2.4 [ ] 2.5-GATE [ ] 2.6 [ ] 2.7-GATE

## Files Analyzed

- `swarmery-design.md` §1/§2 (`permission_requests` DDL, `events` types, `wait_minutes`), §3.1/§3.2/§3.7, §4
- `docs/plan/00-plan.md` (gates pattern, F1–F8, worktree/branch conventions), `docs/plan/phase-2.5-reporter-agent-d.md` (step format, Stop-hook dependency), `docs/plan/phase2-backlog.md`, `docs/plan/step-06-agent-a-ingest.md` (step template), `docs/jsonl-format.md` (hook attachments vs JSONL; denials absent from JSONL → hooks are the only live source), `docs/ws-protocol.md`
- `internal/api/routes.go` (wave blocks), `ws.go` (bus→WS hydration), `overview.go` (waiting_approval placeholder), `internal/ingest/bus.go`, `status.go` (ticker skips waiting_approval), `internal/store/migrations/0001–0005`, `cmd/swarmery/main.go` (`:{port}` bind → D4), `web/src/api/types.ts` (frozen contract), `web/src/App.tsx` (nav), `web/src/lib/ws.ts`, `web/src/mock/`
- code.claude.com/docs/en/hooks (fetched 2026-07-12): PermissionRequest / PreToolUse / Stop / SessionStart contracts, settings tiers, timeout semantics

Alternatives considered: (a) PreToolUse long-poll on every mutating tool — rejected
(gates allowlisted calls; needs a policy engine; double-prompt risk) → D1;
(b) Notification-hook-only observation (no remote decision) — rejected: design §3.2
requires Approve/Deny, not just visibility; (c) single serial agent session for
backend+frontend — rejected: contract is freezable at 2.2, so the MVP parallel-wave
pattern halves wall-clock at low coordination cost.
