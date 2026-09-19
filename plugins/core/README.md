# core — hooks

The vendor-neutral `core` plugin's hook layer. `hooks/hooks.json` is the
registry Claude Code reads; this table is the same list with the WHY and the
environment knob for each, so an operator can tell what a hook does and how to
switch it off without reading the script.

Every hook here is best-effort and exits 0 unless the table says otherwise.
Only `pre-model-switch.sh` may block (exit 2) — plus `protect-sensitive-files.sh`
and `architecture-freshness.sh`, whose refusals are the point of the gate.

Agent roster and ownership metadata live in `AGENTS.md`, not here.

## Registered in `hooks.json`

| Event | Hook | What it does | Knob |
|---|---|---|---|
| PreToolUse(Edit\|Write) | `protect-sensitive-files.sh` | Blocks edits to secrets, keys, and other protected paths | — |
| PreToolUse(Bash) | `bash-shape-guard.sh` | Refuses malformed Bash command shapes before the permission classifier sees them | — |
| PreToolUse(Agent) | `architecture-freshness.sh` | Refuses a research-shaped subagent spawn once per session while the architecture map is stale | `SWARMERY_MAP_FRESHNESS=0`, `SWARMERY_MAP_STALE_DAYS` |
| PostToolUse(Edit\|Write) | `code-formatter.sh` | Formats the edited file with the stack's formatter | — |
| PostToolUse(Edit\|Write) | `type-checker.sh` | Type-checks after an edit | — |
| PostToolUse(Bash) | `post_bash_index_check.sh` | Warns when the Graphify graph has drifted from HEAD | — |
| PostToolUse(*) | `post-tool-observe.sh` | Records every tool call and tracks the session's token budget | `SWARMERY_SESSION_FILE`, `SWARMERY_SESSION_BUDGET_TOKENS`, `SWARMERY_PRICING_JSON` |
| SessionStart | `session-start.sh` | The welcome banner: component counts, active branches, in-flight tasks | — |
| SessionStart | `session-context-bridge.sh` | Carries last session's state into a cold one — the newest `NEXT.md` slice (≤40 lines / 2048 B) and the newest daemon handoff brief for this project (≤1024 B), whole block ≤3072 B. Silent when there is nothing to inject or no daemon answers | `SWARMERY_CONTEXT_BRIDGE=0`, `SWARMERY_PORT` |
| SessionStart | `task-session-log.sh` | Links the session uuid to the active task card | — |
| SessionStart | `architecture-digest.sh` | Injects a compact architecture digest when the repo has a map | — |
| SessionEnd | `session-summary.sh` | Aggregates the session's tool calls into a summary, and rotates the append-only logs it feeds — workspace `sessions/*.json`, `metrics/session-*.jsonl` and `logs/trace-*.jsonl` at 30d, the per-day statusline substrate `claude-session-*.jsonl` in `/tmp` at 7d. `working/**` and the single-file audit logs (`bash-shape-guard.jsonl`, `gate-bypasses.jsonl`) are never swept | `CLAUDE_SESSION_TMP` |
| SubagentStart | `subagent-start.sh` | Records an agent spawn | — |
| SubagentStop | `subagent-stop.sh` | Records an agent's completion and duration | — |
| PreCompact | `pre-compact.sh` | Warns that the context window is about to be compacted | — |
| PostCompact | `post-compact.sh` | Confirms the compaction | — |
| Notification | `notify-completion.sh` | Context-rich desktop notification | — |
| PreModelSwitch | `pre-model-switch.sh` | **Blocks (exit 2)** a switch onto a model with no recorded validation | `SWARMERY_ALLOW_UNVALIDATED_MODEL`, `SWARMERY_PORT` |
| PostModelSwitch | `post-model-switch.sh` | Records that the session's model changed | `SWARMERY_SESSION_FILE` |

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
`protect-sensitive-files.test.sh`. `portable-shell.test.sh` scans every script
here for macOS-only spellings (`stat -f`, `shasum`, `date -v`, bash 4 builtins),
because a hook that is green on a dev Mac and dead on Linux is the failure mode
this layer keeps producing.
