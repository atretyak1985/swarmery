# core — hooks

The vendor-neutral `core` plugin's hook layer. `hooks/hooks.json` is the
registry Claude Code reads; this table is the same list with the WHY and the
environment knob for each, so an operator can tell what a hook does and how to
switch it off without reading the script.

Every hook here is best-effort and exits 0 unless the table says otherwise.
Only `pre-model-switch.sh` may block (exit 2) — plus `phase-guard.sh`,
`protect-sensitive-files.sh` and `architecture-freshness.sh`, whose refusals are
the point of the gate.

Agent roster and ownership metadata live in `AGENTS.md`, not here.

## Registered in `hooks.json`

| Event | Hook | What it does | Knob |
|---|---|---|---|
| PreToolUse(Edit\|Write\|Bash) | `phase-guard.sh` | Inside a phase run (a `.swarmery/plan/` dir at or above the cwd) **blocks (exit 2)** an Edit/Write on, or a Bash command naming, a file the lent phase doc's `kind: guard` block protects — reads through Bash included — plus the guard's own inputs (`.swarmery/plan/**`, `.swarmery/guard.log`). Logs every block to `.swarmery/guard.log`. Silent no-op outside a run; see "Phase guard" below | — |
| PreToolUse(Edit\|Write) | `protect-sensitive-files.sh` | Blocks edits to secrets, keys, and other protected paths | — |
| PreToolUse(Bash) | `bash-shape-guard.sh` | Refuses malformed Bash command shapes before the permission classifier sees them | — |
| PreToolUse(Bash) | `prod-deploy-guard.sh` | Answers `ask` for a command matching a production-deploy pattern, so the native dialog shows even when an allow rule, auto mode or `bypassPermissions` would skip it; in a headless `-p` run that resolves to a denial. Never emits `allow`; burn-in in `docs/GATE-HARDENING.md` | `approvals.prodDeployPatterns` in `.claude/project.json`, `PROD_DEPLOY_GUARD_LOG` |
| PreToolUse(Agent) | `architecture-freshness.sh` | Refuses a research-shaped subagent spawn once per session while the architecture map is stale | `SWARMERY_MAP_FRESHNESS=0`, `SWARMERY_MAP_STALE_DAYS` |
| PostToolUse(Edit\|Write) | `code-formatter.sh` | Formats the edited file with the stack's formatter | — |
| PostToolUse(Edit\|Write) | `type-checker.sh` | Type-checks after an edit | — |
| PostToolUse(Bash) | `post_bash_index_check.sh` | Warns when the Graphify graph has drifted from HEAD | — |
| PostToolUse(*) | `post-tool-observe.sh` | Records every tool call (commands capped at 500 chars) in the per-day session log `claude-session-YYYYMMDD.jsonl`, kept in the private run dir (`~/.swarmery/run`, 0700; the log 0600), and tracks the session's token budget | `SWARMERY_RUN_DIR`, `SWARMERY_SESSION_FILE`, `SWARMERY_SESSION_BUDGET_TOKENS`, `SWARMERY_PRICING_JSON` |
| SessionStart | `session-start.sh` | The welcome banner: component counts, active branches, in-flight tasks | — |
| SessionStart | `session-context-bridge.sh` | Carries last session's state into a cold one — the newest `NEXT.md` slice (≤40 lines / 2048 B) and the newest daemon handoff brief for this project (≤1024 B), whole block ≤3072 B. Silent when there is nothing to inject or no daemon answers | `SWARMERY_CONTEXT_BRIDGE=0`, `SWARMERY_PORT` |
| SessionStart | `task-session-log.sh` | Links the session uuid to the active task card | — |
| SessionStart | `architecture-digest.sh` | Injects a compact architecture digest when the repo has a map | — |
| SessionEnd | `session-summary.sh` | Aggregates the session's tool calls into a summary, and rotates the append-only logs it feeds — workspace `sessions/*.json`, `metrics/session-*.jsonl` and `logs/trace-*.jsonl` at 30d, the per-day statusline substrate `claude-session-*.jsonl` in the run dir at 7d (legacy copies in `/tmp` are left for the OS). `working/**` and the single-file audit logs (`bash-shape-guard.jsonl`, `gate-bypasses.jsonl`) are never swept | `CLAUDE_SESSION_TMP` |
| SubagentStart | `subagent-start.sh` | Records an agent spawn | — |
| SubagentStop | `subagent-stop.sh` | Records an agent's completion and duration | — |
| PreCompact | `pre-compact.sh` | Warns that the context window is about to be compacted | — |
| PostCompact | `post-compact.sh` | Confirms the compaction | — |
| Notification | `notify-completion.sh` | Context-rich desktop notification | — |
| PreModelSwitch | `pre-model-switch.sh` | **Blocks (exit 2)** a switch UP onto a model with no recorded validation. A switch DOWN (weaker family — fable/mythos › opus › sonnet › haiku — or an older generation of the same family) always passes — that is the shape of a safeguard fallback, and blocking one strands the session | `SWARMERY_ALLOW_UNVALIDATED_MODEL`, `SWARMERY_PORT`, `SWARMERY_MODEL_SWITCH_PAYLOAD_LOG` |
| PostModelSwitch | `post-model-switch.sh` | Records that the session's model changed, and warns once when the change was a downgrade | `SWARMERY_SESSION_FILE` |

## Phase guard

`phase-guard.sh` enforces the `## Guard` block a planner writes into every
phase doc (`skills/workspace-plans/resources/plan-format.md` → "`## Guard`"):

```yaml
kind: guard
protected_paths:
  - "**/*_test.go"
  - ".github/workflows/*.yml"
stop_if: [protected path]
```

- **Where it looks.** The daemon lends the phase doc into the run's worktree
  at `.swarmery/plan/<doc>.md`; the hook walks up from the session cwd (then
  from `$CLAUDE_PROJECT_DIR`, so a session that `cd`'d out still meets it) to
  the first such dir and parses every doc in it with `hooks/lib/guard-parse.sh`
  (the bash twin of the daemon's Go parser, held to the same fixtures). No dir
  → exit 0 at once, so interactive sessions never pay for it.
- **What it blocks.** Edit / Write / MultiEdit / NotebookEdit by target path
  (also where a symlink makes it land); Bash by every path-shaped token, so
  `sed -i`, `tee`, `>`, `chmod`, a heredoc body and `python -c` are refused —
  and so is `cat`. **Read protected files with Read / Grep**, which the hook
  does not watch. `**` matches zero or more directories; matching ignores case.
- **Always protected**, guard block or not: `.swarmery/plan/**` (the doc that
  carries the guard), `.swarmery/guard.log`, and the `.swarmery` /
  `.swarmery/plan` directories themselves.
- **Fail closed.** An unparsable guard block blocks every Edit/Write/Bash with
  the parser's line and reason until the plan is fixed; so do jq missing and a
  payload that is not JSON. A glob naming a directory (`dir/`, `dir/**`) only
  warns, and still protects the files under it.
- **The record.** Each block appends `<RFC3339 ts>\t<tool>\t<path>\t<reason>`
  to `.swarmery/guard.log`; the hook never truncates it.
- **Limits.** A command that reaches a protected file without naming it
  (`rm pkg/*`, `find … -exec`, a variable) is not seen. The hook is the early,
  explained refusal, not a sandbox; the run's diff is what later stages check.

## Shipped but not wired

Deliberately unregistered — enable per project by adding the entry to that
project's hooks config:

| Hook | Why it is opt-in |
|---|---|
| `pre-commit-test-gate.sh` | Adds a full test run to every commit; the right trade only for some projects |
| `memory-drift-check.sh` | Only meaningful for projects that keep `.claude/agent-memory/*.md` |

## Tests

Hook behaviour is pinned by `scripts/tests/*.test.sh` in the marketplace repo —
for example `session-context-bridge.test.sh`, `architecture-freshness.test.sh`,
`protect-sensitive-files.test.sh`, `phase-guard.test.sh`. `portable-shell.test.sh` scans every script
here for macOS-only spellings (`stat -f`, `shasum`, `date -v`, bash 4 builtins),
because a hook that is green on a dev Mac and dead on Linux is the failure mode
this layer keeps producing.
