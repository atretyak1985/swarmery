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

# ── 1b. a DATE is not a minor version ─────────────────────────────
# `claude-opus-4-20250514` parsed as major=4, minor=20250514 and scaled to
# generation 20250554, so it compared as NEWER than claude-opus-5-5 and a
# genuine safeguard fallback onto a dated model logged nothing at all. The rule
# that rejects it lives in tools/swarmery/internal/modelid (scale(): a major is
# at most two digits, a minor is exactly one) and the shell half must agree id
# for id — the values below are the ones modelid.Generation returns.
gen() { # gen <name> <want MODEL_GEN>
  model_rank "$1"
  if [ "$MODEL_GEN" = "$2" ]; then ok; else
    bad "model_rank($1) → MODEL_GEN=$MODEL_GEN, want $2"
  fi
}
gen "claude-opus-5-5"            55
gen "claude-opus-4-1"            41
gen "claude-opus-4-20250514"     40   # the date is dropped, not read as a minor
gen "claude-3-5-sonnet-20241022" 35   # pre-5 word order, AND a trailing date
gen "claude-haiku-4-5-20251001"  45   # a real minor AND a date on one id
gen "claude-opus-5-5-fast"       55
gen "claude-opus-5-5[1m]"        55

# …and the verdict that bug produced. Dated ids on BOTH sides of the compare:
# the observed side is the safeguard-fallback case, the requested side is the
# session that already fell back and then falls further.
downgrade "claude-opus-5-5"            "claude-opus-4-20250514"     yes
downgrade "opus"                       "claude-opus-4-20250514"     yes
downgrade "claude-opus-4-20250514"     "claude-opus-5-5"            no
downgrade "claude-opus-4-20250514"     "claude-opus-4-1"            no
downgrade "claude-opus-5-5"            "claude-3-5-sonnet-20241022" yes
downgrade "claude-3-5-sonnet-20241022" "claude-3-5-sonnet-20241022" no
downgrade "claude-haiku-4-5-20251001"  "claude-haiku-4-5"           no

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

# ── 4b. an Agent dispatch is NOT a session model change ───────────
# The drift check used to scan the raw tail for the LAST `"model":"…"` anywhere
# in it. An assistant line that dispatches a subagent serialises `message.model`
# BEFORE the tool_use block's `input.model`, so on any opus session every
# `Task(model: "haiku")` read as a session-wide fallback to haiku: it logged a
# spurious event, wrote `haiku` into the state file, and left the NEXT real
# fallback comparing against haiku — where a true opus → opus-4-1 move reads as
# an UPGRADE and logs nothing. The "unrecognised names are ignored" guard could
# never catch this one: `haiku` is a recognised tier.
TR3="$TESTDIR/dispatch.jsonl"
LOG3="$TESTDIR/dispatch-log.jsonl"
STATE3="$TESTDIR/swarmery-session-model/s-dispatch"
run_observe3() {
  jq -nc --arg t "$TR3" '{tool_name:"Bash",tool_input:{command:"ls"},session_id:"s-dispatch",transcript_path:$t}' |
    SWARMERY_SESSION_FILE="$LOG3" "$OBSERVE" >/dev/null 2>&1
}

# A plain turn first, so the session's model is already remembered and the next
# call is a CHANGE detector with something to compare against.
printf '%s\n' '{"type":"assistant","message":{"id":"m1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"working"}],"usage":{"input_tokens":1}}}' > "$TR3"
run_observe3

# …then the dispatch line: the session is still on opus 5.5 and hands a subagent
# to haiku. Field order here is the real one — message.model, then the tool_use
# input.model — which is exactly what defeated the greedy regex.
printf '%s\n' '{"type":"assistant","message":{"id":"m2","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"tool_use","name":"Task","input":{"subagent_type":"Explore","model":"haiku"}}],"usage":{"input_tokens":2}}}' >> "$TR3"
run_observe3

n=$(jq -s '[.[] | select(.tool=="ModelFallback")] | length' "$LOG3" 2>/dev/null || echo 0)
if [ "$n" = "0" ]; then ok
else bad "dispatching a haiku subagent logged $n session ModelFallback events, want 0 — a subagent's model was read as the session's"; fi

remembered=""
[ -r "$STATE3" ] && read -r remembered < "$STATE3"
if [ "$remembered" = "claude-opus-5-5" ]; then ok
else bad "the remembered session model is '${remembered:-<unset>}', want claude-opus-5-5 — the dispatched subagent's model overwrote the session's"; fi

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

# THE DEFAULT ON DOUBT IS ALLOW. Each of these three was blocked before: none is
# a provable downgrade, so each fell through to the validation gate and exit 2 —
# and every one of them is the hook not KNOWING, not the hook catching an
# operator. A gate that blocks what it cannot read strands the session just as
# surely as one that blocks a safeguard fallback.

# 1. No from_model at all. The live PreModelSwitch payload shape is unattested
#    (see the header of pre-model-switch.sh), so this spelling is realistic and
#    must not be a block.
rc=0; pre '{"to_model":"claude-opus-4-1","session_id":"s"}' || rc=$?
if [ "$rc" -eq 0 ]; then ok; else bad "a switch with no from_model exited $rc, want 0 — direction unknown must fail OPEN"; fi

# 2. A safeguard fallback onto a DATE-SUFFIXED id. This is the P1-a parser bug
#    reaching the one hook allowed to block: 20250554 > 55 made a downgrade look
#    like an upgrade, and the session was stranded on the way down.
rc=0; pre '{"from_model":"claude-opus-5-5","to_model":"claude-opus-4-20250514","session_id":"s"}' || rc=$?
if [ "$rc" -eq 0 ]; then ok; else bad "a fallback onto a dated id exited $rc, want 0"; fi

# 3. lib/model-tier.sh unreadable. The stub used to mean "nothing is ever a
#    downgrade", which restored the old stranding on any machine with a broken
#    copy of the library. Simulated by running the hook from a directory whose
#    lib/ does not exist.
COPY="$TESTDIR/hookcopy"; mkdir -p "$COPY"
cp "$PRE" "$COPY/pre-model-switch.sh"
[ -e "$COPY/lib/model-tier.sh" ] && bad "the no-library fixture is not actually missing its library"
rc=0; printf '%s' '{"from_model":"claude-opus-5","to_model":"claude-opus-6","session_id":"s"}' |
  PATH="$SHIM:$PATH" "$COPY/pre-model-switch.sh" >/dev/null 2>&1 || rc=$?
if [ "$rc" -eq 0 ]; then ok; else bad "with lib/model-tier.sh unreadable the hook exited $rc, want 0 — a missing library must fail OPEN, not block every switch"; fi

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
