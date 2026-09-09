#!/bin/bash
# Behavioral tests for plugins/core/hooks/architecture-freshness.sh.
#
# The gate refuses a research-shaped subagent spawn when the repo's architecture
# map is stale — once per session, never twice. Two properties matter more than
# the refusal itself and are asserted hardest here: it must NOT gate ordinary
# work, and it must never be able to trap a run.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/core/hooks/architecture-freshness.sh"

TESTDIR=$(mktemp -d)
trap 'rm -rf "$TESTDIR"' EXIT
export TMPDIR="$TESTDIR"   # session markers land in the sandbox, not the real one

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

# repo <age-days> <analyzed-commit-matches-head> — a git repo with a map of the
# given age. Echoes its path.
repo() {
  local age="$1" matches="$2" dir sha
  dir="$TESTDIR/repo-$RANDOM"
  mkdir -p "$dir/architecture-out"
  git -C "$dir" init -q
  git -C "$dir" -c user.email=t@t -c user.name=t commit -q --allow-empty -m init
  sha=$(git -C "$dir" rev-parse HEAD)
  if [ "$matches" = "yes" ]; then
    printf '{"analyzedAtCommit":"%s","modules":[]}' "$sha" > "$dir/architecture-out/architecture-map.json"
  else
    printf '{"analyzedAtCommit":"0123456789abcdef0123456789abcdef01234567","modules":[]}' \
      > "$dir/architecture-out/architecture-map.json"
  fi
  # Backdate the map's mtime; the age measure is the advisor's (mtime), not git's.
  touch -t "$(date -v-"${age}"d +%Y%m%d%H%M 2>/dev/null || date -d "-${age} days" +%Y%m%d%H%M)" \
    "$dir/architecture-out/architecture-map.json"
  printf '%s' "$dir"
}

# decision <repo> <session> <agent-type> [description] — ALLOW or REFUSE.
decision() {
  local dir="$1" sid="$2" atype="$3" desc="${4:-do the thing}" rc
  jq -nc --arg a "$atype" --arg d "$desc" --arg s "$sid" \
    '{session_id:$s,tool_name:"Agent",tool_input:{subagent_type:$a,description:$d}}' |
    CLAUDE_PROJECT_DIR="$dir" "$HOOK" >/dev/null 2>&1
  rc=$?
  case "$rc" in
    0) printf 'ALLOW' ;;
    2) printf 'REFUSE' ;;
    *) printf 'ERROR(rc=%s)' "$rc" ;;
  esac
}

expect() {
  local want="$1" desc="$2" got="$3"
  if [ "$got" = "$want" ]; then ok; else bad "$desc — expected $want, got $got"; fi
}

STALE=$(repo 30 no)
FRESH_COMMIT=$(repo 30 yes)   # old file, but analyzed AT head → current
RECENT=$(repo 1 no)           # behind head, but younger than the stale bar

# ── the gate fires on research-shaped spawns against a stale map ──
expect REFUSE "general-purpose against a stale map" "$(decision "$STALE" s1 general-purpose)"
expect REFUSE "plugin-prefixed researcher"          "$(decision "$STALE" s2 core:researcher)"
expect REFUSE "Explore"                             "$(decision "$STALE" s3 Explore)"
expect REFUSE "code-reviewer"                       "$(decision "$STALE" s4 core:code-reviewer)"
# Pre-consolidation names stay recognised so legacy-session transcripts classify.
expect REFUSE "legacy context-gatherer"             "$(decision "$STALE" s4a core:context-gatherer)"
expect REFUSE "legacy tech-researcher"              "$(decision "$STALE" s4b core:tech-researcher)"
# An unrecognised agent is judged by what it was asked to do.
expect REFUSE "unknown agent asked to explore" "$(decision "$STALE" s5 my-local-agent 'Explore the payments module')"

# ── and NOT on ordinary work ──────────────────────────────────────
# Gating every run would stall the fleet whenever any map ages. These are the
# cases that prove the scope is narrow.
expect ALLOW "implementation-agent"       "$(decision "$STALE" s6 core:implementation-agent)"
expect ALLOW "test-writer"                "$(decision "$STALE" s7 core:test-writer)"
expect ALLOW "debugger"                   "$(decision "$STALE" s8 core:debugger)"
expect ALLOW "unknown agent implementing" "$(decision "$STALE" s9 my-local-agent 'Implement the retry budget')"

# ── and not when the map is current ───────────────────────────────
expect ALLOW "map analyzed at HEAD, however old the file" "$(decision "$FRESH_COMMIT" s10 general-purpose)"
expect ALLOW "map behind HEAD but younger than the bar"   "$(decision "$RECENT" s11 general-purpose)"

# ── it can never trap a run ───────────────────────────────────────
# THE property. One refusal per session, then the gate stands aside whatever the
# agent decided — including when the refresh failed or was judged not worth it.
first=$(decision "$STALE" trap-session general-purpose)
second=$(decision "$STALE" trap-session general-purpose)
third=$(decision "$STALE" trap-session Explore)
if [ "$first" = "REFUSE" ] && [ "$second" = "ALLOW" ] && [ "$third" = "ALLOW" ]; then
  ok
else
  bad "the gate must refuse once per session and then stand aside (got $first/$second/$third)"
fi
# A different session is still gated.
expect REFUSE "a second session is judged on its own" "$(decision "$STALE" other-session general-purpose)"

# ── degenerate inputs and the kill switch ─────────────────────────
expect ALLOW "no session id — cannot count, must not refuse" "$(decision "$STALE" '' general-purpose)"
norepo="$TESTDIR/empty-dir"; mkdir -p "$norepo"
expect ALLOW "no map at all is not this hook's problem" "$(decision "$norepo" s12 general-purpose)"

badmap="$TESTDIR/badmap"; mkdir -p "$badmap/architecture-out"
git -C "$badmap" init -q
printf 'not json at all' > "$badmap/architecture-out/architecture-map.json"
expect ALLOW "an unparseable map is not evidence of staleness" "$(decision "$badmap" s13 general-purpose)"

for payload in '{}' 'not json'; do
  printf '%s' "$payload" | CLAUDE_PROJECT_DIR="$STALE" "$HOOK" >/dev/null 2>&1
  if [ $? -eq 0 ]; then ok; else bad "payload $payload must pass silently"; fi
done

out=$(jq -nc '{session_id:"kill",tool_input:{subagent_type:"general-purpose"}}' |
  CLAUDE_PROJECT_DIR="$STALE" SWARMERY_MAP_FRESHNESS=0 "$HOOK" 2>&1; printf '|%s' "$?")
if [ "${out##*|}" = "0" ]; then ok; else bad "SWARMERY_MAP_FRESHNESS=0 must disable the gate"; fi

# ── the refusal has to be actionable ──────────────────────────────
msg=$(jq -nc '{session_id:"msg",tool_input:{subagent_type:"general-purpose"}}' |
  CLAUDE_PROJECT_DIR="$STALE" "$HOOK" 2>&1 >/dev/null)
for needle in '/architecture-map' 'INCREMENTAL' 'ONCE per session' 'days old'; do
  if printf '%s' "$msg" | grep -qiF "$needle"; then ok; else bad "the refusal never mentions $needle"; fi
done

# ── the gate and the advisor must agree on "stale" ────────────────
# A gate that fires on a map the advisor still calls fresh is a gate the operator
# learns to distrust, so the two constants are pinned to each other in source.
hook_days=$(grep -oE 'SWARMERY_MAP_STALE_DAYS:-[0-9]+' "$HOOK" | grep -oE '[0-9]+$')
r7_days=$(grep -oE 'R7StaleDays = [0-9]+' "$ROOT/tools/swarmery/internal/advisor/rules.go" | grep -oE '[0-9]+$')
if [ -n "$hook_days" ] && [ "$hook_days" = "$r7_days" ]; then
  ok
else
  bad "the gate's stale bar ($hook_days d) does not match the advisor's R7StaleDays ($r7_days d)"
fi

printf 'architecture-freshness: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
