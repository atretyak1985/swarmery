#!/bin/bash
# model-tier.test.sh — the rule three hooks now share: a fallback is a move to a
# WEAKER model, not a difference between two strings.
#
# The bug this pins is a false POSITIVE, so most of these cases assert silence.
# `opus` → `claude-opus-5-5` is the normal, healthy outcome of every Agent
# dispatch in this fleet, and it used to log a ModelFallback event every time —
# which is how a real fallback became invisible in the noise.
#
# Hooks are EXECUTED, never run as `bash "$HOOK"`: production invokes them by
# path and gets /bin/bash 3.2 from the shebang, and a bash-4 builtin is a
# runtime "command not found" there rather than a syntax error
# (scripts/tests/portable-shell.test.sh explains the incident).
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LIB="$ROOT/plugins/core/hooks/lib/model-tier.sh"
OBSERVE="$ROOT/plugins/core/hooks/post-tool-observe.sh"
PRE="$ROOT/plugins/core/hooks/pre-model-switch.sh"
POST="$ROOT/plugins/core/hooks/post-model-switch.sh"

TESTDIR=$(mktemp -d)
trap 'rm -rf "$TESTDIR"' EXIT
export TMPDIR="$TESTDIR"

pass=0
fail=0
ok()  { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

# ── 1. the comparison itself ──────────────────────────────────────
# shellcheck source=../../plugins/core/hooks/lib/model-tier.sh
. "$LIB"

# downgrade <requested> <observed> <want yes|no>
downgrade() {
  local want="$3" got="no"
  model_is_downgrade "$1" "$2" && got="yes"
  if [ "$got" = "$want" ]; then ok; else
    bad "model_is_downgrade($1, $2) = $got, want $want"
  fi
}

# The happy path that used to log on every dispatch.
downgrade "opus"   "claude-opus-5-5"        no
downgrade "sonnet" "claude-sonnet-5"        no
downgrade "haiku"  "claude-haiku-4-5"       no
# Same model, different context window / SKU — not a change at all.
downgrade "claude-opus-5-5" "claude-opus-5-5[1m]"  no
downgrade "claude-opus-5-5" "claude-opus-5-5-fast" no
# Upgrades are not fallbacks.
downgrade "claude-sonnet-5" "claude-opus-5-5" no
downgrade "claude-opus-5"   "claude-opus-5-5" no
# Unknown on either side: say nothing rather than guess.
downgrade "opus"    "gpt-4o"          no
downgrade "unknown" "claude-opus-5-5" no
downgrade ""        "claude-opus-5-5" no
# The real thing, on both axes.
downgrade "opus"            "claude-opus-4-1"   yes
downgrade "opus"            "claude-sonnet-5"   yes
downgrade "opus"            "sonnet"            yes
downgrade "claude-opus-5-5" "claude-opus-5"     yes
downgrade "claude-opus-5-5" "claude-haiku-4-5"  yes
downgrade "claude-sonnet-5" "claude-haiku-4-5"  yes

# The alias table is overridable, so a machine ahead of this repo is not stuck
# with a stale answer.
if SWARMERY_MODEL_CURRENT_GENS="opus=60" bash -c ". '$LIB'; model_is_downgrade opus claude-opus-5-5"; then
  ok
else
  bad "SWARMERY_MODEL_CURRENT_GENS did not move the alias generation"
fi

# ── 2. the alias table must not go stale ──────────────────────────
# The one number in this file that cannot be derived at runtime is "what does
# the alias `opus` mean today". Pin it against config/pricing.json, which a
# model cutover already forces someone to update — so the table cannot drift
# without a red test.
PRICING="$ROOT/tools/swarmery/config/pricing.json"
if [ -r "$PRICING" ] && command -v jq >/dev/null 2>&1; then
  newest_opus=$(jq -r '
      (.models // {}) | keys
      | map(select(startswith("claude-opus-") and (contains("-fast") | not)))
      | map(sub("^claude-opus-"; "") | split("-") | map(tonumber? // 0)
            | (.[0] * 10 + (.[1] // 0)))
      | max' "$PRICING" 2>/dev/null)
  table_opus=$(model_tier_current_gen opus)
  if [ -n "$newest_opus" ] && [ "$newest_opus" = "$table_opus" ]; then
    ok
  else
    bad "model_tier_current_gen opus = $table_opus but the newest opus row in config/pricing.json scales to $newest_opus — update plugins/core/hooks/lib/model-tier.sh"
  fi
fi

# ── 3. post-tool-observe: the acceptance criterion ────────────────
# "`opus` → `claude-opus-5-5` logs nothing; `opus` → an older generation logs
# ModelFallback."
observe_dispatch() { # observe_dispatch <requested> <observed> -> echoes the log file
  local log="$TESTDIR/log-$RANDOM.jsonl" out rc
  out=$(jq -nc --arg r "$1" --arg o "$2" \
        '{tool_name:"Agent",tool_input:{model:$r},tool_response:{model:$o},session_id:"s-obs"}' |
        SWARMERY_SESSION_FILE="$log" "$OBSERVE" 2>/dev/null)
  rc=$?
  [ "$rc" -eq 0 ] || bad "post-tool-observe exited $rc — it must never fail a tool call"
  printf '%s' "$out" | jq -e '.continue == true' >/dev/null 2>&1 || bad "post-tool-observe did not emit {continue:true}: $out"
  printf '%s' "$log"
}

log=$(observe_dispatch "opus" "claude-opus-5-5")
if jq -e 'select(.tool=="ModelFallback")' "$log" >/dev/null 2>&1; then
  bad "opus → claude-opus-5-5 logged a ModelFallback — that is the bug, not the feature"
else ok; fi
# …and the dispatch itself is still recorded.
if jq -e 'select(.tool=="Agent")' "$log" >/dev/null 2>&1; then ok
else bad "the Agent dispatch line disappeared from the activity log"; fi

log=$(observe_dispatch "opus" "claude-opus-4-1")
if jq -e 'select(.tool=="ModelFallback" and (.cmd | contains("claude-opus-4-1")))' "$log" >/dev/null 2>&1; then
  ok
else
  bad "opus → claude-opus-4-1 did not log a ModelFallback"
fi

log=$(observe_dispatch "opus" "sonnet")
if jq -e 'select(.tool=="ModelFallback")' "$log" >/dev/null 2>&1; then ok
else bad "a family downgrade did not log a ModelFallback"; fi

# Degenerate input must still be silent and successful — this hook runs on every
# tool call, and an error here degrades every session on the machine.
for payload in '{}' 'not json' '{"tool_name":"Agent","tool_input":{"model":"opus"}}'; do
  out=$(printf '%s' "$payload" | SWARMERY_SESSION_FILE="$TESTDIR/degen.jsonl" "$OBSERVE" 2>/dev/null); rc=$?
  if [ "$rc" -eq 0 ]; then ok; else bad "payload $payload → exit $rc"; fi
done

# ── 4. main-session drift ─────────────────────────────────────────
# A downgrade visible only in the transcript tail (no Agent dispatch involved)
# must be logged once, and only once.
TR="$TESTDIR/main.jsonl"
LOG2="$TESTDIR/main-log.jsonl"
printf '{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":1}}}\n' > "$TR"
run_observe() {
  jq -nc --arg t "$TR" '{tool_name:"Bash",tool_input:{command:"ls"},session_id:"s-main",transcript_path:$t}' |
    SWARMERY_SESSION_FILE="$LOG2" "$OBSERVE" >/dev/null 2>&1
}
run_observe   # first sighting: remembers the model, logs nothing
if jq -e 'select(.tool=="ModelFallback")' "$LOG2" >/dev/null 2>&1; then
  bad "the first sighting of a session's model logged a fallback"
else ok; fi

printf '{"type":"assistant","message":{"model":"claude-opus-4-1","usage":{"input_tokens":2}}}\n' >> "$TR"
run_observe
n=$(jq -s '[.[] | select(.tool=="ModelFallback")] | length' "$LOG2" 2>/dev/null || echo 0)
if [ "$n" = "1" ]; then ok; else bad "a main-session downgrade logged $n ModelFallback events, want 1"; fi
run_observe; run_observe
n=$(jq -s '[.[] | select(.tool=="ModelFallback")] | length' "$LOG2" 2>/dev/null || echo 0)
if [ "$n" = "1" ]; then ok; else bad "the main-session downgrade re-logged on later tool calls ($n total)"; fi

# ── 5. pre-model-switch: a downgrade is never blocked ─────────────
# A safeguard fallback IS a downgrade, and blocking one strands the session with
# nowhere to go. This is the fail-open half of the gate.
SHIM="$TESTDIR/bin"; mkdir -p "$SHIM"
cat > "$SHIM/curl" <<'EOS'
#!/bin/sh
exit 22
EOS
chmod +x "$SHIM/curl"   # 22 = 404: "no recorded validation", which BLOCKS an upgrade

pre() { printf '%s' "$1" | PATH="$SHIM:$PATH" "$PRE" >/dev/null 2>&1; }

rc=0; pre '{"from_model":"claude-opus-5-5","to_model":"claude-opus-4-1","session_id":"s"}' || rc=$?
if [ "$rc" -eq 0 ]; then ok; else bad "a downgrade to an older generation was blocked (exit $rc)"; fi

rc=0; pre '{"from_model":"claude-opus-5-5","to_model":"claude-sonnet-4-6","session_id":"s"}' || rc=$?
if [ "$rc" -eq 0 ]; then ok; else bad "a downgrade to a weaker family was blocked (exit $rc)"; fi

# The gate itself must survive: an UPGRADE onto an unvalidated model still blocks.
rc=0; pre '{"from_model":"claude-opus-5","to_model":"claude-opus-6","session_id":"s"}' || rc=$?
if [ "$rc" -eq 2 ]; then ok; else bad "an unvalidated UPGRADE exited $rc, want 2 — the gate was disabled, not narrowed"; fi

msg=$(printf '%s' '{"from_model":"claude-opus-5-5","to_model":"claude-opus-4-1"}' | PATH="$SHIM:$PATH" "$PRE" 2>&1 >/dev/null)
if printf '%s' "$msg" | grep -qi 'safeguard'; then ok
else bad "the downgrade allowance does not say why on stderr: $msg"; fi

# The payload-dump escape hatch — the honest answer to "what does Claude Code
# actually send?", since no field in the payload distinguishes who initiated it.
DUMP="$TESTDIR/payloads.jsonl"
printf '%s' '{"from_model":"a","to_model":"b","session_id":"s"}' |
  PATH="$SHIM:$PATH" SWARMERY_MODEL_SWITCH_PAYLOAD_LOG="$DUMP" "$PRE" >/dev/null 2>&1
if [ -s "$DUMP" ] && grep -q 'to_model' "$DUMP"; then ok
else bad "SWARMERY_MODEL_SWITCH_PAYLOAD_LOG did not capture the payload"; fi

# ── 6. post-model-switch: one line, and only on a downgrade ───────
warn=$(printf '%s' '{"from_model":"claude-opus-5-5","to_model":"claude-opus-4-1","session_id":"s"}' |
       SWARMERY_SESSION_FILE="$TESTDIR/post.jsonl" "$POST" 2>&1 >/dev/null)
if printf '%s' "$warn" | grep -q 'session moved from' \
   && printf '%s' "$warn" | grep -q '/model'; then
  ok
else
  bad "post-model-switch did not warn about a downgrade, or did not name the way back: $warn"
fi
quiet=$(printf '%s' '{"from_model":"claude-sonnet-5","to_model":"claude-opus-5-5","session_id":"s"}' |
        SWARMERY_SESSION_FILE="$TESTDIR/post.jsonl" "$POST" 2>&1 >/dev/null)
if printf '%s' "$quiet" | grep -q 'session moved from'; then
  bad "post-model-switch warned about an UPGRADE"
else ok; fi

printf 'model-tier: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
