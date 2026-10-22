---
description: Session dashboard — tool usage, active tasks, agent stats, and metrics overview
allowed-tools:
  - Bash
  - Read
  - Glob
  - Grep
docs:
  status: reviewed
  source_sha: 393094f45f6b
  updated: 2026-08-06
---

# /dashboard — Session & System Dashboard

Show a comprehensive dashboard of the current Claude Code session and the project's agent system status. Combine multiple data sources into a single structured report.

## What to report

Collect ALL of the following data points in parallel, then present them in a single structured dashboard.

### 1. Current Session Stats

Read the session tracking file to get tool usage stats:

```bash
today=$(date +%Y%m%d)
RUN_DIR="${SWARMERY_RUN_DIR:-$HOME/.swarmery/run}"   # private per-user hook state
SESSION_FILE=""
for f in "$RUN_DIR"/claude-session-*-${today}.jsonl "$RUN_DIR/claude-session-${today}.jsonl"; do
  [ -f "$f" ] && [ -s "$f" ] && SESSION_FILE="$f"
done

if [ -n "$SESSION_FILE" ]; then
  total=$(wc -l < "$SESSION_FILE" | tr -d ' ')
  echo "Total tool calls: $total"
  echo "--- By tool ---"
  jq -r '.tool' "$SESSION_FILE" | sort | uniq -c | sort -rn
  echo "--- Unique files ---"
  jq -r 'select(.file != "") | .file' "$SESSION_FILE" | sort -u | wc -l | tr -d ' '
  echo "--- Files list ---"
  jq -r 'select(.file != "") | .file' "$SESSION_FILE" | sort -u | sed "s|${CLAUDE_PROJECT_DIR:-$(pwd)}/||"
  echo "--- Timeline ---"
  first_ts=$(head -1 "$SESSION_FILE" | jq -r '.ts')
  last_ts=$(tail -1 "$SESSION_FILE" | jq -r '.ts')
  echo "Start: $first_ts"
  echo "Last:  $last_ts"
else
  echo "No session data for today"
fi
```

### 2. Active Tasks (agent-work.sh)

```bash
bash .claude/scripts/agent-work.sh list in_progress 2>/dev/null || echo "No active tasks"
```

### 3. Recent Completed Tasks

```bash
bash .claude/scripts/agent-work.sh list completed 2>/dev/null | tail -5 || echo "No completed tasks"
```

### 4. Agent System Health

```bash
# Count agents
agent_count=$(find .claude/agents -name "*.md" -not -name "README.md" | wc -l | tr -d ' ')
command_count=$(find .claude/commands -name "*.md" -not -name "README.md" | wc -l | tr -d ' ')
skill_count=$(find .claude/skills -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')
hook_count=$(find .claude/hooks -name "*.sh" | wc -l | tr -d ' ')
echo "Agents: $agent_count | Commands: $command_count | Skills: $skill_count | Hooks: $hook_count"
```

### 5. Today's Metrics (if available)

```bash
today=$(date +%Y%m%d)
# Resolve the workspace (swarmery model), falling back to legacy project-local .claude-workspace/
WS="${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}/${AGENT_PROJECT}/workspace"
[ -d "$WS" ] || WS="${CLAUDE_PROJECT_DIR:-.}/.claude-workspace"   # legacy layout
metrics_glob="${WS}/metrics/session-${today}-*.jsonl"
metrics_files=$(ls $metrics_glob 2>/dev/null)
if [ -n "$metrics_files" ]; then
  session_count=$(echo "$metrics_files" | wc -l | tr -d ' ')
  total=$(cat $metrics_glob | wc -l | tr -d ' ')
  echo "--- Cumulative across $session_count session(s) today ---"
  echo "Total tool calls: $total"
  echo "--- By tool ---"
  cat $metrics_glob | jq -r '.tool' | sort | uniq -c | sort -rn
  echo "--- By project ---"
  cat $metrics_glob | jq -r 'select(.file != "") | .file' \
    | sed -E "s|^${CLAUDE_PROJECT_DIR:-$(pwd)}/([^/]+).*|\1|" \
    | sort | uniq -c | sort -rn
else
  echo "No session metrics for today (expected: $metrics_glob)"
fi

trace_file="${WS}/logs/trace-${today}.jsonl"
if [ -f "$trace_file" ]; then
  echo "--- Trace events ---"
  wc -l < "$trace_file" | tr -d ' '
  jq -r '.event' "$trace_file" | sort | uniq -c | sort -rn | head -10
else
  echo "No trace events for today (no hook writes to ${WS}/logs/)"
fi
```

## Output format

Present ALL collected data as a structured dashboard inside a **plain** fenced
code block (no language hint, no `ansi`).

**Layout: a fully closed box with a right border aligned by computed width.**
Do NOT eyeball the padding — compute it. After collecting all data, build the
list of content lines, measure the longest one by *visual* width, then pad
every line to that width before adding the closing `│`.

**You MUST generate the box with this script** (paste your real data into the
`out.append(row(...))` calls), so the right border is always exact:

```bash
python3 - <<'EOF'
EMOJI = set("📊🔧📋🤖📈💡")          # each renders as 2 terminal cells
def vwidth(s):
    return sum(2 if ch in EMOJI else 1 for ch in s)

# 1. collect every content line (text shown inside the box)
content = [
    " 📊  Project Dashboard                                   {time}",
    "  🔧  SESSION",
    "  Duration     {duration}",
    "  Tool calls   {total}  ·  Edit {n}  Read {n}  Bash {n}  ...",
    "  Files        {n} unique files touched",
    "  Projects     {list of projects}",
    "  📋  TASKS",
    "  Active       {list or 'none'}",
    "  Completed    {last 5 or 'none'}",
    "  🤖  AGENT SYSTEM",
    "  Agents {n}   ·  Commands {n}  ·  Skills {n}  ·  Hooks {n}",
    "  📈  TODAY'S METRICS   (cumulative, {n} sessions)",
    "  Tool calls   {total}  ·  Bash {n}  Read {n}  Write {n}  Edit {n}",
    "  Hot files    {top files}",
    "  Trace        {trace summary}",
    "  💡  TIPS",
    "  /cost          token usage & cost",
    "  /dashboard     this dashboard",
    "  agent-work.sh  task management CLI",
]
INNER = max(vwidth(c) for c in content)   # 2. longest visual width

def row(t):   return f"│ {t}{' '*(INNER-vwidth(t))} │"
def hr():     return f"│ {'─'*INNER} │"
def blank():  return f"│{' '*(INNER+2)}│"
def rule(l,r):return f"{l}{'─'*(INNER+2)}{r}"

# 3. assemble — order/sections fixed, swap in real values
print(rule("┌","┐"))
print(row(content[0])); print(rule("├","┤")); print(blank())
# ... repeat row()/hr()/blank() for each section ...
print(rule("└","┘"))
EOF
```

Resulting shape (right border lands on the longest line, here `Projects`):

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  📊  Project Dashboard                                   {time}                │
├──────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   🔧  SESSION                                                                │
│ ──────────────────────────────────────────────────────────────────────────── │
│   Duration     {duration}                                                    │
│   Projects     {longest line sets the box width}                             │
│   ...                                                                        │
└──────────────────────────────────────────────────────────────────────────────┘
```

Rendering rules — these prevent the broken/garbled output seen previously:

1. **Right border must be width-computed, never hand-padded.** A closing `│`
   requires every line to share an identical *visual* width. Emoji (📊 🔧 …)
   render as 2 cells but count as 1 `len()` char, so naive padding drifts by
   +1 per emoji. The script above fixes this with `vwidth()` (emoji = 2). If
   you ever add a glyph wider than 1 cell, add it to the `EMOJI` set.
2. **No ANSI color codes.** Raw escape sequences (`\033[...m`) are NOT rendered
   by the Claude Code terminal renderer; they leak as visible text like
   `[0;36m`. Use plain text only.
3. **No `ansi` language hint** on the code block. Long ANSI blocks can also
   duplicate on render. A plain ``` fence is deterministic.
4. Align the label column with plain spaces (labels are pure ASCII, so this is
   safe). Never pad the *end* of a line to a target width.
5. Keep rows narrow — wrap long lists onto an indented continuation line rather
   than producing very wide rows.

# How to use

## What it does

It builds a single readable snapshot of what has happened in your Claude Code session and how the project's agent system is set up. It pulls today's tool-call log, the active and recently completed workspace tasks, counts of agents/commands/skills/hooks, and today's cumulative metrics, then prints them all inside one aligned box.

## When to use it

- You come back to a session and want to see what was touched today before deciding what to do next.
- You want a quick count of files edited, tools used, and how long the session has been running.
- You need to know which workspace tasks are still in progress and which ones closed recently.
- You want to confirm the project's agents, commands, skills, and hooks are actually being discovered.

## When not to use it

- You want token spend and cost — use `/cost` instead.
- You want to create, pause, or complete a task — use the workspace CLI directly.
- You want a health verdict on code quality — reach for a code-quality or audit command.

## How to invoke

```
/dashboard
```

Type it with no arguments. It gathers every data source it can reach and skips the sections whose files do not exist yet, so it is safe to run at any point in a session.

## Inputs

- No arguments — the command takes none.
- `AGENT_WORKSPACE_ROOT` and `AGENT_PROJECT` — environment variables that locate the workspace holding today's metrics — optional; a legacy project-local workspace directory is used as a fallback.
- `CLAUDE_PROJECT_DIR` — used to shorten file paths in the report — optional.

## What you get back

One plain fenced code block containing a closed box with five sections: session stats, tasks, agent system counts, today's cumulative metrics, and a short tips list. Nothing is written to disk and nothing is modified — the command only reads logs, lists tasks, and counts files. Sections with no data say so rather than being dropped.

## Worked example

```
/dashboard

→ reads today's session log, lists in-progress and recently completed
  tasks, counts agents/commands/skills/hooks, and sums today's metrics

┌────────────────────────────────────────────────┐
│  Project Dashboard                      14:20  │
├────────────────────────────────────────────────┤
│   SESSION                                      │
│   Tool calls   184  ·  Edit 41  Read 77        │
│   Files        23 unique files touched         │
│   TASKS                                        │
│   Active       orders/line-items refactor      │
│   AGENT SYSTEM                                 │
│   Agents 12  ·  Commands 9  ·  Skills 7        │
└────────────────────────────────────────────────┘
```

You end up with a single box you can read at a glance or paste into a handoff note.

## Related

- `/cost` — prefer it when the question is token usage and spend rather than activity.
- `/statusline-help` — prefer it when you want the always-visible status line explained instead of a one-off report.
