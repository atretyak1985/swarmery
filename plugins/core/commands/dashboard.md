---
description: Session dashboard — tool usage, active tasks, agent stats, and metrics overview
allowed-tools:
  - Bash
  - Read
  - Glob
  - Grep
---

# /dashboard — Session & System Dashboard

Show a comprehensive dashboard of the current Claude Code session and the project's agent system status. Combine multiple data sources into a single structured report.

## What to report

Collect ALL of the following data points in parallel, then present them in a single structured dashboard.

### 1. Current Session Stats

Read the session tracking file to get tool usage stats:

```bash
today=$(date +%Y%m%d)
SESSION_FILE=""
for f in /tmp/claude-session-*-${today}.jsonl /tmp/claude-session-${today}.jsonl; do
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
metrics_glob=".claude-workspace/metrics/session-${today}-*.jsonl"
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

trace_file=".claude-workspace/logs/trace-${today}.jsonl"
if [ -f "$trace_file" ]; then
  echo "--- Trace events ---"
  wc -l < "$trace_file" | tr -d ' '
  jq -r '.event' "$trace_file" | sort | uniq -c | sort -rn | head -10
else
  echo "No trace events for today (no hook writes to .claude-workspace/logs/)"
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
