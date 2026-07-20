#!/usr/bin/env bash
# AGENTS_STATUSLINE — PAI-inspired multi-line status line for Claude Code.
# Reads the Status-hook JSON on stdin and prints an ANSI-colored block.
#
# Wired via .claude/settings.json -> statusLine.command.
# Data sources:
#   - stdin JSON  : model, version, output_style, workspace, cost,
#                   context_window.used_percentage, rate_limits (plan % + resets)
#   - transcript  : context % fallback for CC < 2.1 (last message.usage)
#   - filesystem  : agents/skills/commands/hooks counts, memory files, tasks, sessions
#   - git (cwd)   : branch, age, dirty count, ahead/behind
#   - wttr.in     : weather + location (cached 10m, refreshed in background)
#
# Defensive by design: every external lookup has a fallback so the line never breaks.
#
# ============================================================================
#  LEGEND — what every rendered line/field means (8 lines)
# ============================================================================
#
#  1) HEADER
#     — AGENTS_STATUSLINE — <Model> · <Style> · ▲<effort> 🧠 ⚡fast  “<session>”
#       Title        : "AGENTS_STATUSLINE" literal by default. OPT-IN: with
#                      SWARMERY_STATUSLINE_USER=1 it is replaced by the email of
#                      the Claude subscription the session runs under —
#                      .oauthAccount.emailAddress from CC's own local config:
#                      $CLAUDE_CONFIG_DIR/.claude.json when the var is set
#                      (multi-account setups switch accounts via that var; the
#                      $HOME file is deliberately NOT used as a fallback then),
#                      else $HOME/.claude.json. Pure local read: no network,
#                      no credentials, no cache needed.
#       Model        : JSON .model.display_name           (e.g. "Opus 4.8")
#       Style        : JSON .output_style.name            (e.g. "Explanatory")
#       ▲<effort>    : JSON .effort.level                 (high / medium / low)
#       🧠           : shown when JSON .thinking.enabled == true
#       ⚡fast       : shown when JSON .fast_mode == true
#       “session”    : JSON .session_name (the auto/explicit session title)
#
#  2) LOC:  <City> │ <HH:MM> │ <Temp> <Condition>
#       City         : SWARMERY_STATUSLINE_LOC env → wttr.in (default "Lviv";
#                      set empty for auto-by-IP)
#       HH:MM        : local clock, recomputed every render
#       Temp/Cond    : wttr.in, cached 10m, refreshed in background
#       Fallback     : if weather not yet cached → "TIME: HH:MM (weather warming up…)"
#
#  3) ENV:  CC <ver> │ AG <n>  SK <n>  CMD <n> │ Hooks <n>
#       CC           : JSON .version (Claude Code version)
#       AG/SK/CMD    : agents / skills / commands counts under <project>/.claude/…
#       Hooks        : *.sh under hooks/  (same find-logic as session-start.sh,
#                      so these always match the SessionStart banner)
#
#  4) CONTEXT:  <30-seg bar> <pct>%
#       Computed, NOT given by CC: read transcript_path → last message.usage →
#       (input + cache_read + cache_creation) / context_window.
#       context_window = 1,000,000 if model id contains "[1m]", else 200,000.
#       Bar color: green <50% · yellow ≥50% · red ≥80% (consider /compact).
#
#  5) USAGE:  5H <pct>% ⟳<reset> │ WK <pct>% ⟳<reset> [ │ FB <pct>% ⟳<reset> ]
#       5H/WK: OFFICIAL plan limits — same numbers as the /usage screen. Native to
#       CC 2.1+, FREE from stdin JSON .rate_limits.* (no ccusage / npx / cache).
#       5H pct       : .rate_limits.five_hour.used_percentage   (5-hour window)
#       WK pct       : .rate_limits.seven_day.used_percentage   (weekly window)
#       ⟳reset       : time until window resets (resets_at). <24h => "3h53m",
#                      else weekday+clock "Sat 16:59".
#       pct color    : green <50% · yellow ≥50% · red ≥80%.
#       Hidden only if the CC version doesn't provide .rate_limits.
#       FB pct       : Fable-5 weekly usage — the "Fable" bar on claude.ai settings→usage.
#                      NOT in the statusline JSON (CC only pipes five_hour/seven_day), so it
#                      is fetched from CC's own endpoint GET /api/oauth/usage by the opt-in
#                      helper fetch-fable-usage.sh (reads CC's OAuth token from the macOS
#                      Keychain item of THIS session's config dir — CLAUDE_CONFIG_DIR-aware,
#                      so multi-subscription setups always see their own account), cached 5m
#                      per account (SWARMERY_STATUSLINE_FABLE_TTL, default 300s)
#                      + refreshed in the background like weather.
#                      OPT-IN: shown only when  export SWARMERY_STATUSLINE_FABLE=1 .
#                      Sourced from .limits[] where .scope.model.display_name=="Fable"
#                      (.percent 0-100; .resets_at is an ISO-8601 string → fmt_reset_any).
#                      Accounts WITHOUT a Fable window (org/Team plans expose only
#                      session + weekly_all) get the helper's "none|" marker cached
#                      instead → the FB segment is hidden, no per-render re-fetching.
#                      A cache older than SWARMERY_STATUSLINE_FABLE_MAX_AGE (default
#                      24h) is never rendered — stale numbers vanish rather than freeze.
#
#  6) SESSION:  <$cost> │ +<added>/-<removed> │ ⏱ <duration>
#       All FREE from stdin JSON .cost.* — instant, no external calls:
#       $cost        : .cost.total_cost_usd      (this session's spend)
#       +added/-removed : .cost.total_lines_added / total_lines_removed
#       ⏱ duration   : .cost.total_duration_ms   (humanized h/m/s)
#
#  7) PWD:  <dir> │ Branch <b> │ Age <t> │ Mod <n> │ Sync <s>
#       dir          : basename of JSON .workspace.current_dir
#       Branch       : git rev-parse --abbrev-ref HEAD
#       Age          : time since last commit, m/h/d (git log -1 %ct)
#       Mod          : dirty file count (git status --porcelain); yellow if >0
#       Sync         : ↑N ahead / ↓N behind upstream; "✓" when in sync
#       The whole git tail VANISHES when cwd is not a git repo (e.g. workspace root).
#
#  8) MEMORY:  📁 <n> Memories │ ◆ <n> Tasks │ ⊕ <n> Sessions
#       Memories     : ~/.claude/projects/<cwd-slug>/memory/*.md (derived from PROJECT_DIR)
#       Tasks        : workspace working/YYYY/MM/DD/<slug>/ dirs (legacy: .claude-workspace/)
#       Sessions     : .swarmery/sessions/*.json (fallback /tmp/claude-session-*.jsonl)
#
#  RELIABILITY TIERS:
#    • Instant from JSON  → lines 1, 6 (+ parts of 2/7). Zero risk.
#    • Local compute      → lines 3, 4, 8, git. Fast, deterministic.
#    • External + cache   → weather, Fable usage (FB). Shown from cache instantly;
#                           refreshed in a detached `&` job so the line never blocks.
#
#  KNOBS: export SWARMERY_STATUSLINE_LOC="Kyiv"   (weather city; "" => auto-by-IP)
#         export SWARMERY_STATUSLINE_USER=1       (header title = active subscription's
#                                                email from the session's .claude.json
#                                                — CLAUDE_CONFIG_DIR-aware — instead
#                                                of the AGENTS_STATUSLINE literal)
#         export SWARMERY_STATUSLINE_FABLE=1      (show FB Fable-5 usage; opt-in, reads
#                                                CC's OAuth token from the macOS Keychain)
#         export SWARMERY_STATUSLINE_FABLE_TTL=300 (FB cache freshness window in seconds;
#                                                default 300 = 5 min)
#         export SWARMERY_STATUSLINE_FABLE_MAX_AGE=86400 (FB display cutoff in seconds —
#                                                a cache older than this is hidden, not
#                                                rendered; default 86400 = 24h)
# ============================================================================

set -uo pipefail

# ----- read stdin JSON -----------------------------------------------------
INPUT="$(cat)"
jqr() { printf '%s' "$INPUT" | jq -r "$1" 2>/dev/null; }

MODEL_NAME="$(jqr '.model.display_name // "Claude"')"
MODEL_ID="$(jqr '.model.id // ""')"
CC_VER="$(jqr '.version // "?"')"
STYLE="$(jqr '.output_style.name // ""')"
SESSION_NAME="$(jqr '.session_name // empty')"
EFFORT="$(jqr '.effort.level // empty')"        # e.g. "high" / "medium" / "low"
THINKING="$(jqr '.thinking.enabled // empty')"  # "true" | "false" | ""
FAST="$(jqr '.fast_mode // empty')"             # "true" | "false" | ""
CWD="$(jqr '.workspace.current_dir // .cwd // empty')"
PROJECT_DIR="$(jqr '.workspace.project_dir // empty')"
TRANSCRIPT="$(jqr '.transcript_path // empty')"
[ -z "$CWD" ] && CWD="$PWD"
[ -z "$PROJECT_DIR" ] && PROJECT_DIR="$CWD"

# ----- palette (256-color) -------------------------------------------------
C_RST=$'\033[0m'; C_B=$'\033[1m'
BLUE=$'\033[38;5;39m'; CYAN=$'\033[38;5;44m'; TEAL=$'\033[38;5;43m'
PURPLE=$'\033[38;5;141m'; GREEN=$'\033[38;5;78m'; YELLOW=$'\033[38;5;221m'
RED=$'\033[38;5;203m'; GREY=$'\033[38;5;245m'; WHITE=$'\033[38;5;255m'
ORANGE=$'\033[38;5;215m'

sep="${GREY}│${C_RST}"

# ----- context window % ----------------------------------------------------
# Prefer CC's own canonical figure (.context_window.used_percentage, CC 2.1+).
# Fall back to parsing the transcript for older CC that lacks the field.
CTX_PCT="$(jqr '(.context_window.used_percentage // empty) | round')"
if ! [[ "$CTX_PCT" =~ ^[0-9]+$ ]]; then
  case "$MODEL_ID" in
    *"[1m]"*|*"1m"*) CTX_WINDOW=1000000 ;;
    *)              CTX_WINDOW=200000 ;;
  esac
  CTX_USED=0
  if [ -n "$TRANSCRIPT" ] && [ -f "$TRANSCRIPT" ]; then
    CTX_USED="$(jq -rs '
      [ .[] | select(.message.usage != null) ] | last
      | (.message.usage.input_tokens // 0)
        + (.message.usage.cache_read_input_tokens // 0)
        + (.message.usage.cache_creation_input_tokens // 0)
    ' "$TRANSCRIPT" 2>/dev/null)"
    [[ "$CTX_USED" =~ ^[0-9]+$ ]] || CTX_USED=0
  fi
  CTX_PCT=$(( CTX_USED * 100 / CTX_WINDOW ))
fi
[ "$CTX_PCT" -gt 100 ] && CTX_PCT=100

# Build a 30-segment bar, colored by fill level.
BAR_LEN=30
FILLED=$(( CTX_PCT * BAR_LEN / 100 ))
if   [ "$CTX_PCT" -ge 80 ]; then PCT_COLOR=$RED
elif [ "$CTX_PCT" -ge 50 ]; then PCT_COLOR=$YELLOW
else PCT_COLOR=$GREEN; fi
BAR=""
for ((i=0; i<BAR_LEN; i++)); do
  if [ "$i" -lt "$FILLED" ]; then BAR="${BAR}${PCT_COLOR}●${C_RST}"
  else BAR="${BAR}${GREY}○${C_RST}"; fi
done

# ----- fleet counts (same logic as session-start.sh) -----------------------
# CLAUDE_DIR resolves the symlinked agents dir from the project.
CLAUDE_DIR="$PROJECT_DIR/.claude"
[ -d "$CLAUDE_DIR" ] || CLAUDE_DIR="$PROJECT_DIR/agents"
AGENTS=$(find "$CLAUDE_DIR/agents" -name "*.md" -not -name "README.md" 2>/dev/null | wc -l | tr -d ' ')
SKILLS=$(find "$CLAUDE_DIR/skills" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l | tr -d ' ')
CMDS=$(find "$CLAUDE_DIR/commands" -name "*.md" -not -name "README.md" 2>/dev/null | wc -l | tr -d ' ')
HOOKS=$(find "$CLAUDE_DIR/hooks" -name "*.sh" 2>/dev/null | wc -l | tr -d ' ')

# ----- memory / tasks / sessions ------------------------------------------
MEM_DIR="$HOME/.claude/projects/$(echo "$PROJECT_DIR" | tr '/' '-')/memory"
MEMORIES=$(find "$MEM_DIR" -name "*.md" -not -name "MEMORY.md" 2>/dev/null | wc -l | tr -d ' ')
# Active task dirs: working/YYYY/MM/DD/<slug> — swarmery workspace first, legacy fallback
if [ -n "${AGENT_PROJECT:-}" ]; then
  WORK_ROOT="${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}/${AGENT_PROJECT}/workspace/working"
else
  WORK_ROOT="$PROJECT_DIR/.claude-workspace/working"
fi
TASKS=$(find "$WORK_ROOT" -mindepth 4 -maxdepth 4 -type d 2>/dev/null | wc -l | tr -d ' ')
# Recorded sessions
SESS=$(find "$PROJECT_DIR/.swarmery/sessions" -name "*.json" 2>/dev/null | wc -l | tr -d ' ')
# shellcheck disable=SC2012  # simple count of glob matches; find not needed here
[ "$SESS" = "0" ] && SESS=$(ls /tmp/claude-session-*.jsonl 2>/dev/null | wc -l | tr -d ' ')

# ----- git (against cwd) ---------------------------------------------------
GIT_BRANCH=""; GIT_AGE=""; GIT_MOD="0"; GIT_SYNC=""
if git -C "$CWD" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  GIT_BRANCH="$(git -C "$CWD" rev-parse --abbrev-ref HEAD 2>/dev/null)"
  GIT_MOD="$(git -C "$CWD" status --porcelain 2>/dev/null | wc -l | tr -d ' ')"
  last_ct="$(git -C "$CWD" log -1 --format=%ct 2>/dev/null)"
  if [ -n "$last_ct" ]; then
    now="$(date +%s)"; diff=$(( now - last_ct ))
    if   [ "$diff" -lt 3600 ];  then GIT_AGE="$(( diff / 60 ))m"
    elif [ "$diff" -lt 86400 ]; then GIT_AGE="$(( diff / 3600 ))h"
    else GIT_AGE="$(( diff / 86400 ))d"; fi
  fi
  ahead="$(git -C "$CWD" rev-list --count '@{u}..HEAD' 2>/dev/null || echo 0)"
  behind="$(git -C "$CWD" rev-list --count 'HEAD..@{u}' 2>/dev/null || echo 0)"
  [ "${ahead:-0}" -gt 0 ] && GIT_SYNC="${GIT_SYNC}↑${ahead}"
  [ "${behind:-0}" -gt 0 ] && GIT_SYNC="${GIT_SYNC}↓${behind}"
  [ -z "$GIT_SYNC" ] && GIT_SYNC="✓"
fi

# ----- weather (cached, background refresh) --------------------------------
# Location: SWARMERY_STATUSLINE_LOC env wins; default Lviv. Empty => auto-by-IP.
WX_LOC_QUERY="${SWARMERY_STATUSLINE_LOC-Lviv}"
WX_SLUG="$(printf '%s' "${WX_LOC_QUERY:-auto}" | tr ' /' '__')"
WX_CACHE="${TMPDIR:-/tmp}/agents-statusline-wx-${WX_SLUG}.txt"
WX_FORMAT='%l|%t|%C'   # location | temp | condition
wx_fresh() { [ -f "$WX_CACHE" ] && [ "$(( $(date +%s) - $(stat -f %m "$WX_CACHE" 2>/dev/null || echo 0) ))" -lt 600 ]; }
if ! wx_fresh; then
  ( curl -fsS --max-time 4 "https://wttr.in/${WX_LOC_QUERY}?format=${WX_FORMAT}" -o "$WX_CACHE.tmp" 2>/dev/null \
      && mv "$WX_CACHE.tmp" "$WX_CACHE" ) >/dev/null 2>&1 &
fi
WX_LOC=""; WX_TEMP=""; WX_COND=""
if [ -f "$WX_CACHE" ]; then
  IFS='|' read -r WX_LOC WX_TEMP WX_COND < "$WX_CACHE"
fi

# ----- plan usage limits (native, free from stdin JSON) --------------------
# CC 2.1+ exposes official rate-limit accounting — the same numbers as /usage.
# No ccusage / npx / cache needed.
U5_PCT="$(jqr '(.rate_limits.five_hour.used_percentage // empty) | round')"
U5_RESET="$(jqr '.rate_limits.five_hour.resets_at // empty')"
UWK_PCT="$(jqr '(.rate_limits.seven_day.used_percentage // empty) | round')"
UWK_RESET="$(jqr '.rate_limits.seven_day.resets_at // empty')"

# ----- Fable-5 usage (OPT-IN; external endpoint, cached + background refresh) -----
# CC pipes ONLY five_hour/seven_day into the statusline JSON — the Fable weekly window
# (shown on claude.ai settings→usage) is NOT included. It lives behind CC's own auth-gated
# endpoint GET /api/oauth/usage. That call is slow + credential-bearing, so it is opt-in
# and never on the render path: we read a cache file here and refresh it in a detached
# job (same idiom as the weather block). The helper is the only file that reads the token.
#   Enable with:  export SWARMERY_STATUSLINE_FABLE=1
FB_PCT=""; FB_RESET=""
if [ "${SWARMERY_STATUSLINE_FABLE:-0}" = "1" ]; then
  # Cache is PER ACCOUNT: multi-subscription setups run sessions under different
  # CLAUDE_CONFIG_DIRs, and one shared file would let accounts overwrite each other's
  # numbers. Slug = the same sha256(configDir) prefix CC uses to namespace its own
  # Keychain credential item (and that fetch-fable-usage.sh uses to pick the token).
  FB_SLUG="$(printf '%s' "${CLAUDE_CONFIG_DIR:-$HOME/.claude}" | shasum -a 256 2>/dev/null | cut -c1-8)"
  FB_CACHE="${TMPDIR:-/tmp}/agents-statusline-fable-${FB_SLUG:-default}.txt"
  FB_TTL="${SWARMERY_STATUSLINE_FABLE_TTL:-300}"        # refresh cadence in seconds (default 5 min)
  FB_MAX_AGE="${SWARMERY_STATUSLINE_FABLE_MAX_AGE:-86400}"  # display cutoff: never render a cache older than this (default 24h)
  FB_AGE=""
  [ -f "$FB_CACHE" ] && FB_AGE=$(( $(date +%s) - $(stat -f %m "$FB_CACHE" 2>/dev/null || echo 0) ))
  if [ -z "$FB_AGE" ] || [ "$FB_AGE" -ge "$FB_TTL" ]; then
    FB_HELPER="$(dirname "${BASH_SOURCE[0]}")/fetch-fable-usage.sh"
    [ -x "$FB_HELPER" ] && ( o="$("$FB_HELPER" 2>/dev/null)"; [ -n "$o" ] && printf '%s\n' "$o" > "$FB_CACHE.tmp" && mv "$FB_CACHE.tmp" "$FB_CACHE" ) >/dev/null 2>&1 &
  fi
  # Display only a cache younger than FB_MAX_AGE: a value the helper can no longer
  # refresh (endpoint drift, revoked token) must eventually disappear, not freeze.
  # The cache may also hold the helper's "none|" marker (account has no Fable window
  # — org/Team plans); the render guard below drops any non-numeric FB_PCT, so the
  # marker hides the segment while still suppressing per-render re-fetches.
  if [ -n "$FB_AGE" ] && [ "$FB_AGE" -lt "$FB_MAX_AGE" ]; then
    IFS='|' read -r FB_PCT FB_RESET < "$FB_CACHE"
  fi
fi

# ----- header title: subscription account email (OPT-IN) -------------------
# With SWARMERY_STATUSLINE_USER=1 the AGENTS_STATUSLINE literal in the header is
# replaced by the email of the Claude subscription THIS session runs under.
# Source is CC's own local config (.oauthAccount in .claude.json) — a plain
# local file read on the render path: no network, no Keychain, no cache.
#
# Multi-subscription correctness: users running several accounts launch CC with
# a different CLAUDE_CONFIG_DIR per account (e.g. ~/.claude-work), and CC then
# keeps .claude.json INSIDE that dir. The statusline inherits the CC process
# env, so the var always identifies the active session's account. When it is
# set we read ONLY $CLAUDE_CONFIG_DIR/.claude.json — never $HOME/.claude.json,
# which would silently show a DIFFERENT subscription. On any miss (logged out,
# CI, no oauthAccount) the literal stays — wrong-account is worse than no name.
HEADER_TITLE="AGENTS_STATUSLINE"
if [ "${SWARMERY_STATUSLINE_USER:-0}" = "1" ]; then
  if [ -n "${CLAUDE_CONFIG_DIR:-}" ]; then
    CC_CFG="$CLAUDE_CONFIG_DIR/.claude.json"
  else
    CC_CFG="$HOME/.claude.json"
  fi
  if [ -f "$CC_CFG" ]; then
    ACCOUNT_EMAIL="$(jq -r '.oauthAccount.emailAddress // .oauthAccount.displayName // empty' "$CC_CFG" 2>/dev/null)"
    [ -n "$ACCOUNT_EMAIL" ] && HEADER_TITLE="$ACCOUNT_EMAIL"
  fi
fi

# ----- session cost (free from stdin JSON) ---------------------------------
SESS_COST="$(jqr '.cost.total_cost_usd // empty')"
DUR_MS="$(jqr '.cost.total_duration_ms // empty')"
ADD="$(jqr '.cost.total_lines_added // 0')"
DEL="$(jqr '.cost.total_lines_removed // 0')"

# ----- small formatters ----------------------------------------------------
# Reset countdown: <24h => "3h53m"; otherwise weekday + clock "Sat 16:59".
fmt_reset() {
  local ts="${1:-}" now diff
  [[ "$ts" =~ ^[0-9]+$ ]] || { printf '?'; return; }
  now="$(date +%s)"; diff=$(( ts - now ))
  [ "$diff" -le 0 ] && { printf 'now'; return; }
  if [ "$diff" -lt 86400 ]; then
    if [ "$diff" -lt 3600 ]; then printf '%dm' $(( diff / 60 ))
    else printf '%dh%02dm' $(( diff / 3600 )) $(( (diff % 3600) / 60 )); fi
  else
    date -r "$ts" "+%a %H:%M" 2>/dev/null || date -d "@$ts" "+%a %H:%M" 2>/dev/null || printf '%dd' $(( diff / 86400 ))
  fi
}
# Like fmt_reset, but also accepts an ISO-8601 UTC string ("2026-07-08T09:59:59.86+00:00",
# as returned by /api/oauth/usage for the Fable window). Unix-seconds pass straight through.
fmt_reset_any() {
  local ts="${1:-}" base secs
  [ -z "$ts" ] && { printf '?'; return; }
  [[ "$ts" =~ ^[0-9]+$ ]] && { fmt_reset "$ts"; return; }
  base="${ts%%.*}"; base="${base%%+*}"; base="${base%Z}"   # strip fractional secs + TZ offset
  secs="$(date -j -u -f "%Y-%m-%dT%H:%M:%S" "$base" +%s 2>/dev/null || date -u -d "$ts" +%s 2>/dev/null)"
  [ -n "$secs" ] && fmt_reset "$secs" || printf '%s' "$ts"
}
# Threshold color for a 0-100 percentage: green <50, yellow <80, red >=80.
pct_color() {
  local p="${1:-0}"; [[ "$p" =~ ^[0-9]+$ ]] || p=0
  if   [ "$p" -ge 80 ]; then printf '%s' "$RED"
  elif [ "$p" -ge 50 ]; then printf '%s' "$YELLOW"
  else printf '%s' "$GREEN"; fi
}
human_dur() {
  local ms="${1:-0}"; [[ "$ms" =~ ^[0-9]+$ ]] || { printf '0s'; return; }
  local s=$((ms/1000))
  if   [ "$s" -ge 3600 ]; then printf '%dh%dm' $((s/3600)) $(((s%3600)/60))
  elif [ "$s" -ge 60 ];   then printf '%dm%ds' $((s/60)) $((s%60))
  else printf '%ds' "$s"; fi
}
fmt_usd() { local v="${1:-}"; [ -z "$v" ] && { printf '?'; return; }; printf '$%.2f' "$v" 2>/dev/null || printf '$%s' "$v"; }

NOW="$(date +%H:%M)"
PWD_SHORT="$(basename "$CWD")"

# ----- render --------------------------------------------------------------
# Header badges: effort level, thinking, fast-mode, then the session name.
BADGES=""
[ -n "$EFFORT" ]       && BADGES="${BADGES} ${GREY}·${C_RST} ${ORANGE}▲${EFFORT}${C_RST}"
[ "$THINKING" = "true" ] && BADGES="${BADGES} ${PURPLE}🧠${C_RST}"
[ "$FAST" = "true" ]     && BADGES="${BADGES} ${YELLOW}⚡fast${C_RST}"
NAME_PART=""
# shellcheck disable=SC1111  # intentional typographic quotes around the session name
[ -n "$SESSION_NAME" ] && NAME_PART="  ${GREY}“${SESSION_NAME}”${C_RST}"
printf '%b\n' "${GREY}—${C_RST} ${BLUE}${C_B}${HEADER_TITLE}${C_RST} ${GREY}—${C_RST} ${PURPLE}${MODEL_NAME}${C_RST}${STYLE:+ ${GREY}·${C_RST} ${TEAL}${STYLE}${C_RST}}${BADGES}${NAME_PART}"

# LOC line only when weather is available
if [ -n "$WX_LOC" ]; then
  printf '%b\n' "${GREEN}${C_B}LOC:${C_RST} ${WHITE}${WX_LOC}${C_RST}  ${sep}  ${CYAN}${NOW}${C_RST}  ${sep}  ${ORANGE}${WX_TEMP}${C_RST} ${GREY}${WX_COND}${C_RST}"
else
  printf '%b\n' "${GREEN}${C_B}TIME:${C_RST} ${CYAN}${NOW}${C_RST}  ${GREY}(weather warming up…)${C_RST}"
fi

printf '%b\n' "${BLUE}${C_B}ENV:${C_RST} ${GREY}CC${C_RST} ${CYAN}${CC_VER}${C_RST}  ${sep}  ${GREEN}AG ${WHITE}${AGENTS}${C_RST}  ${PURPLE}SK ${WHITE}${SKILLS}${C_RST}  ${BLUE}CMD ${WHITE}${CMDS}${C_RST}  ${sep}  ${YELLOW}Hooks ${WHITE}${HOOKS}${C_RST}"

printf '%b\n' "${PURPLE}${C_B}CONTEXT:${C_RST} ${BAR} ${PCT_COLOR}${C_B}${CTX_PCT}%${C_RST}"

# USAGE — official plan limits (% used + reset countdown), native to CC 2.1+.
# FB (Fable-5) is appended only when opt-in fetched (see SWARMERY_STATUSLINE_FABLE above);
# it never resurrects the line on its own — the 5H/WK guard is intentionally unchanged.
if [ -n "$U5_PCT" ] || [ -n "$UWK_PCT" ]; then
  c5="$(pct_color "$U5_PCT")"; c7="$(pct_color "$UWK_PCT")"
  fb_part=""
  if [[ "$FB_PCT" =~ ^[0-9]+$ ]]; then
    cfb="$(pct_color "$FB_PCT")"
    fb_reset_part=""
    [ -n "$FB_RESET" ] && fb_reset_part=" ${GREY}⟳$(fmt_reset_any "$FB_RESET")${C_RST}"
    fb_part="  ${sep}  ${GREY}FB${C_RST} ${cfb}${C_B}${FB_PCT}%${C_RST}${fb_reset_part}"
  fi
  printf '%b\n' "${YELLOW}${C_B}USAGE:${C_RST} ${GREY}5H${C_RST} ${c5}${C_B}${U5_PCT:-?}%${C_RST} ${GREY}⟳$(fmt_reset "$U5_RESET")${C_RST}  ${sep}  ${GREY}WK${C_RST} ${c7}${C_B}${UWK_PCT:-?}%${C_RST} ${GREY}⟳$(fmt_reset "$UWK_RESET")${C_RST}${fb_part}"
fi

# SESSION cost — free from stdin JSON
if [ -n "$SESS_COST" ]; then
  churn=""
  { [ "${ADD:-0}" -gt 0 ] || [ "${DEL:-0}" -gt 0 ]; } && churn="  ${sep}  ${GREEN}+${ADD}${C_RST}${GREY}/${C_RST}${RED}-${DEL}${C_RST}"
  dur_part=""
  [ -n "$DUR_MS" ] && dur_part="  ${sep}  ${GREY}⏱ ${WHITE}$(human_dur "$DUR_MS")${C_RST}"
  printf '%b\n' "${ORANGE}${C_B}SESSION:${C_RST} ${WHITE}$(fmt_usd "$SESS_COST")${C_RST}${churn}${dur_part}"
fi

GIT_PART=""
if [ -n "$GIT_BRANCH" ]; then
  mod_color=$GREEN; [ "${GIT_MOD:-0}" -gt 0 ] && mod_color=$YELLOW
  GIT_PART="  ${sep}  ${CYAN}Branch ${WHITE}${GIT_BRANCH}${C_RST}${GIT_AGE:+  ${sep}  ${CYAN}Age ${WHITE}${GIT_AGE}${C_RST}}  ${sep}  ${CYAN}Mod ${mod_color}${GIT_MOD}${C_RST}  ${sep}  ${CYAN}Sync ${WHITE}${GIT_SYNC}${C_RST}"
fi
printf '%b\n' "${TEAL}${C_B}PWD:${C_RST} ${CYAN}${PWD_SHORT}${C_RST}${GIT_PART}"

printf '%b\n' "${PURPLE}${C_B}MEMORY:${C_RST} ${YELLOW}📁 ${WHITE}${MEMORIES}${C_RST} ${GREY}Memories${C_RST}  ${sep}  ${PURPLE}◆ ${WHITE}${TASKS}${C_RST} ${GREY}Tasks${C_RST}  ${sep}  ${TEAL}⊕ ${WHITE}${SESS}${C_RST} ${GREY}Sessions${C_RST}"
