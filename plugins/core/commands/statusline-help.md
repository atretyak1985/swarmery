---
description: Explain the custom statusline — what every line/field means, data source, colors, and the location knob
allowed-tools:
  - Bash
  - Read
---

# /statusline-help — Decode the custom statusline

Print a clear reference for the custom statusline rendered at the bottom of the
terminal. Script: `agents/statusline/agents-statusline.sh` (wired via the
`statusLine` key in `agents/settings.json`).

## Steps

1. **Render a live sample** so the user sees current colors and values. Run:

   ```bash
   printf '%s' '{"version":"2.1.170","model":{"id":"claude-opus-4-8[1m]","display_name":"Opus 4.8 (1M context)"},"output_style":{"name":"Explanatory"},"workspace":{"current_dir":"'"$CLAUDE_PROJECT_DIR"'","project_dir":"'"$CLAUDE_PROJECT_DIR"'"},"cwd":"'"$CLAUDE_PROJECT_DIR"'","context_window":{"used_percentage":15},"rate_limits":{"five_hour":{"used_percentage":8,"resets_at":2000000000},"seven_day":{"used_percentage":20,"resets_at":2000600000}},"cost":{"total_cost_usd":5.04,"total_duration_ms":2792000,"total_lines_added":299,"total_lines_removed":6}}' | bash "$CLAUDE_PROJECT_DIR/agents/statusline/agents-statusline.sh"
   ```

   (This is a synthetic payload for illustration — the real statusline uses the
   live session's values.)

2. **Present this reference table verbatim:**

| Line | Field | Meaning | Source |
|------|-------|---------|--------|
| **1 Header** | `Model · Style` | Active model + output style | JSON `.model.display_name`, `.output_style.name` |
| **2 LOC** | `City` | Weather location (default from the script) | `AGENTRY_STATUSLINE_LOC` env → wttr.in |
| | `HH:MM` | Local clock | recomputed each render |
| | `+28°C Sunny` | Weather | wttr.in, cached 10m, background refresh |
| **3 ENV** | `CC <ver>` | Claude Code version | JSON `.version` |
| | `AG/SK/CMD/Hooks` | Fleet counts (agents/skills/commands/hooks) | `find` under `.claude/` (matches SessionStart banner) |
| **4 CONTEXT** | `bar + %` | Context window fill | JSON `.context_window.used_percentage` (fallback: transcript). Green <50 · yellow ≥50 · red ≥80 |
| **5 USAGE** | `5H <pct>% ⟳<reset>` | **Official** 5-hour plan limit used + reset countdown | JSON `.rate_limits.five_hour` (same as `/usage`) |
| | `WK <pct>% ⟳<reset>` | Weekly plan limit used + reset | JSON `.rate_limits.seven_day` |
| **6 SESSION** | `$cost` | This session's spend | JSON `.cost.total_cost_usd` |
| | `+N/-N` | Lines added / removed | JSON `.cost.total_lines_added/removed` |
| | `⏱ <dur>` | Session duration | JSON `.cost.total_duration_ms` |
| **7 PWD** | `dir` | Current folder | JSON `.workspace.current_dir` |
| | `Branch/Age/Mod/Sync` | Git branch · time since last commit · dirty count · ahead/behind. **Vanishes outside a git repo.** | `git` against cwd |
| **8 MEMORY** | `Memories/Tasks/Sessions` | Memory files · active task dirs · recorded sessions | filesystem under `memory/`, `.claude-workspace/working/`, the sessions dir |

3. **Mention the knobs:**
   - Change weather city: `export AGENTRY_STATUSLINE_LOC="Kyiv"` (or `""` for auto-by-IP).
   - Reliability tiers: instant-from-JSON (lines 1, 5, 6) · local compute (3, 4, 8, git) · external+cache (weather only). Nothing blocks the render.
   - Fresh session shows `CONTEXT 0%` / `SESSION $0` / no git block at workspace root — all expected.

Keep the output tight: the live sample first, then the table, then the knobs.
