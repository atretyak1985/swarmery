---
description: Explain the custom statusline — what every line/field means, data source, colors, and the location knob
allowed-tools:
  - Bash
  - Read
---

# /statusline-help — Decode the custom statusline

Print a clear reference for the custom statusline rendered at the bottom of the
terminal. Script: `.claude/statusline/statusline.sh` in consumer projects
(an **opt-in** deploy — `swarmery onboard/attach --statusline-src …` or a manual
copy, see `docs/ONBOARDING.md` "Statusline"; wired via the `statusLine` key in
`.claude/settings.json`); source of truth lives at `plugins/core/statusline/`.

## Steps

1. **Render a live sample** so the user sees current colors and values. Run:

   ```bash
   SL="$CLAUDE_PROJECT_DIR/.claude/statusline/statusline.sh"
   [ -f "$SL" ] || SL="$CLAUDE_PROJECT_DIR/plugins/core/statusline/statusline.sh"
   printf '%s' '{"version":"2.1.170","model":{"id":"claude-opus-4-8[1m]","display_name":"Opus 4.8 (1M context)"},"output_style":{"name":"Explanatory"},"workspace":{"current_dir":"'"$CLAUDE_PROJECT_DIR"'","project_dir":"'"$CLAUDE_PROJECT_DIR"'"},"cwd":"'"$CLAUDE_PROJECT_DIR"'","context_window":{"used_percentage":15},"rate_limits":{"five_hour":{"used_percentage":8,"resets_at":2000000000},"seven_day":{"used_percentage":20,"resets_at":2000600000}},"cost":{"total_cost_usd":5.04,"total_duration_ms":2792000,"total_lines_added":299,"total_lines_removed":6}}' | bash "$SL"
   ```

   (This is a synthetic payload for illustration — the real statusline uses the
   live session's values.)

2. **Present this reference table verbatim:**

| Line | Field | Meaning | Source |
|------|-------|---------|--------|
| **1 Header** | `AGENTS_STATUSLINE` | Header title. **Opt-in**: with `SWARMERY_STATUSLINE_USER=1` it's replaced by the email of the subscription this session runs under | `.oauthAccount.emailAddress` from the session's `.claude.json` (`$CLAUDE_CONFIG_DIR/.claude.json` if set, else `~/.claude.json`) — no network |
| | `Model · Style` | Active model + output style | JSON `.model.display_name`, `.output_style.name` |
| **2 LOC** | `City` | Weather location (default from the script) | `SWARMERY_STATUSLINE_LOC` env → wttr.in |
| | `HH:MM` | Local clock | recomputed each render |
| | `+28°C Sunny` | Weather | wttr.in, cached 10m, background refresh |
| **3 ENV** | `CC <ver>` | Claude Code version | JSON `.version` |
| | `AG/SK/CMD/Hooks` | Fleet counts (agents/skills/commands/hooks) | `find` under `.claude/` (matches SessionStart banner) |
| **4 CONTEXT** | `bar + %` | Context window fill | JSON `.context_window.used_percentage` (fallback: transcript). Green <50 · yellow ≥50 · red ≥80 |
| **5 USAGE** | `5H <pct>% ⟳<reset>` | **Official** 5-hour plan limit used + reset countdown | JSON `.rate_limits.five_hour` (same as `/usage`) |
| | `WK <pct>% ⟳<reset>` | Weekly plan limit used + reset | JSON `.rate_limits.seven_day` |
| | `FB <pct>% ⟳<reset>` | Fable-5 weekly window (the "Fable" bar on claude.ai settings→usage). **Opt-in, hidden by default** | `fetch-fable-usage.sh` → `GET /api/oauth/usage` (OAuth token from the macOS Keychain item of the session's config dir — `CLAUDE_CONFIG_DIR`-aware, multi-subscription-safe), cached per account, background refresh |
| **6 SESSION** | `$cost` | This session's spend | JSON `.cost.total_cost_usd` |
| | `+N/-N` | Lines added / removed | JSON `.cost.total_lines_added/removed` |
| | `⏱ <dur>` | Session duration | JSON `.cost.total_duration_ms` |
| **7 PWD** | `dir` | Current folder | JSON `.workspace.current_dir` |
| | `Branch/Age/Mod/Sync` | Git branch · time since last commit · dirty count · ahead/behind. **Vanishes outside a git repo.** | `git` against cwd |
| **8 MEMORY** | `Memories/Tasks/Sessions` | Memory files · active task dirs · recorded sessions | filesystem under `memory/`, the workspace `working/` tree (legacy: `.claude-workspace/working/`), the sessions dir |

3. **Mention the knobs:**
   - Change weather city: `export SWARMERY_STATUSLINE_LOC="Kyiv"` (or `""` for auto-by-IP).
   - Header title = active subscription's email: opt-in with `SWARMERY_STATUSLINE_USER=1`.
     Reads `.oauthAccount.emailAddress` from the session's `.claude.json` —
     `$CLAUDE_CONFIG_DIR/.claude.json` when that var is set (how multi-account users
     switch subscriptions per project), else `~/.claude.json`. When `CLAUDE_CONFIG_DIR`
     is set, the `$HOME` file is deliberately never consulted, so the header can't show
     another subscription. Falls back to the `AGENTS_STATUSLINE` literal when logged out
     or the file is missing.
   - Fable-5 usage segment (`FB`): opt-in with `SWARMERY_STATUSLINE_FABLE=1` (reads the local
     Claude Code OAuth token from the macOS Keychain; fails silent — the segment simply
     doesn't render on any error). Multi-subscription-safe: the token comes from the
     Keychain item CC writes per config dir (`Claude Code-credentials-<sha256(configDir)[0:8]>`),
     and the cache file is namespaced the same way, so each `CLAUDE_CONFIG_DIR` account sees
     only its own numbers. Cache TTL: `SWARMERY_STATUSLINE_FABLE_TTL` seconds
     (default 300). The helper `fetch-fable-usage.sh` has its own `SWARMERY_FABLE_*`
     overrides (keychain service, endpoint URL, timeout) — see its header comment.
   - Reliability tiers: instant-from-JSON (lines 1, 5, 6) · local compute (3, 4, 8, git) · external+cache (weather + opt-in Fable). Nothing blocks the render.
   - Fresh session shows `CONTEXT 0%` / `SESSION $0` / no git block at workspace root — all expected.

Keep the output tight: the live sample first, then the table, then the knobs.
