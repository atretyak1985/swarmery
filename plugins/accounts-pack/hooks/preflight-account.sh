#!/usr/bin/env bash
# SessionStart hook — credential COVERAGE preflight.
#
# Asks `swarmery account doctor --fast --json` which ${VAR}s the project's
# enabled plugins reference in their MCP configs and which of them are set in
# this session, and says so at turn zero when any is missing — naming the
# variables, never a value. MCP env is read once at process start, so a gap
# cannot be repaired in-session; the message says what to do instead.
#
# Two rules kept from the skeleton, unconditionally:
#   - FAIL-OPEN. No `set -e`; exit 0 on every path, including malformed input,
#     missing jq, a missing/failing/hanging swarmery and unparseable output. A
#     hook that can fail is a hook that can block a session.
#   - DRAIN STDIN. Claude Code writes the hook payload to stdin; leaving it
#     unread risks the writer blocking on a full pipe buffer.
#
# THE ONLY ESCALATION is a non-empty `varsMissing`. A count of zero credentials,
# an estate with no store file, "no estate", or an empty `varsExpected` are all
# HEALTHY and silent: a project may enable a dozen plugins that reference no
# ${VAR} at all, and a hook that spoke on a count would fire at every start.
# One INFORMATIONAL sentence rides along: the doctor's once-per-path
# `first-sight` finding (a directory new under an estate root) is rendered as
# the estate root plus the credential COUNT — never a name, never a value.
#
# Side output: ~/.swarmery/run/preflight/<session_id>.env (dir 0700, file 0600)
# for the statusline — account=, estate=, varsExpected=, varsPresent= (the
# LENGTHS of the Report's two lists, under the Report's own spelling), launch=.
# No variable names and no values ever go there.
set -uo pipefail

INPUT="$(cat 2>/dev/null || true)"

[ "${SWARMERY_SKIP_PREFLIGHT:-}" = "1" ] && exit 0
command -v jq >/dev/null 2>&1 || exit 0

# Intentionally STRICTER than claudeacct.ValidKey (internal/claudeacct/
# claudeacct.go:158-180) for the characters that matter here — this is NOT
# a mirror of it, despite what an earlier version of this comment claimed.
# This only allows [A-Za-z0-9._-]; ValidKey's own rules (reject "", ".",
# "..", a leading dot or dash, "/", "\", ".." as a substring, whitespace, and
# non-printable runes) still leave it accepting "wörk", "a$b", "a;b", a
# bare backtick, or a double quote. Diverging by rejecting MORE than
# ValidKey does is the safe direction: every extra character this refuses
# is exactly the class — whitespace, quotes, backticks, non-ASCII — that
# turns a config-dir name into something that reads like an instruction
# once it lands in additionalContext below. The failure mode is silence,
# not injection: a real key Go would accept can be turned down here, the
# same fail-open outcome as Go's Binding() returning "" for a key that
# fails ValidKey — never the other way around for these characters. Cost:
# an operator whose account key contains a non-ASCII character gets no
# mismatch warning at all.
valid_account_key() {
  case "${1:-}" in
    ''|'.'|'..')          return 1 ;;
    .*|-*)                return 1 ;;
    *[!A-Za-z0-9._-]*)    return 1 ;;
  esac
  return 0
}

# Variable NAMES — the only other thing that reaches additionalContext.
valid_var_name() {
  [[ "${1:-}" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]
}

# cwd comes from the hook's own stdin JSON (verified shape: internal/hookshim/
# shim_test.go:203 — session_id, cwd, hook_event_name; NO transcript_path).
PROJECT_DIR="$(printf '%s' "$INPUT" | jq -r '.cwd // empty' 2>/dev/null)"
[ -z "$PROJECT_DIR" ] && PROJECT_DIR="${CLAUDE_PROJECT_DIR:-}"
[ -z "$PROJECT_DIR" ] && exit 0
SESSION_ID="$(printf '%s' "$INPUT" | jq -r '.session_id // empty' 2>/dev/null)"

SWARMERY="$(command -v swarmery 2>/dev/null || true)"
case "$SWARMERY" in
  /*) ;;
  *)
    # The cached install location is trusted only when its dir is owned by
    # this user and writable by neither group nor other — otherwise the binary
    # there may have been planted by someone else. Fail open: say nothing.
    [ -n "${HOME:-}" ] || exit 0
    FALLBACK_DIR="$(cd "${HOME}/.swarmery/bin" 2>/dev/null && pwd -P)" || exit 0
    [ -n "$(find "$FALLBACK_DIR" -prune -user "$(id -u)" ! -perm -020 ! -perm -002 2>/dev/null)" ] || exit 0
    SWARMERY="$FALLBACK_DIR/swarmery"
    ;;
esac
[ -x "$SWARMERY" ] || exit 0

# ── run the doctor under a portable 3 s watchdog (no GNU `timeout` on macOS) ──
# 3 s, not 2: `doctor --fast` resolves the path, and resolution now runs the
# provenance probe (a `git ls-files`) at every rung that declares a binding.
OUT_FILE="$(mktemp 2>/dev/null)" || exit 0
trap 'rm -f "$OUT_FILE"' EXIT
# --timeout is the doctor's own inner bound, strictly below the watchdog. It is
# checked between arms: the arms still to run are skipped and the report still
# arrives before the kill -9, but an arm already running finishes first — one
# that alone outlasts the watchdog loses the whole report, vars-missing too.
"$SWARMERY" account doctor --fast --json --timeout 2.5s --path "$PROJECT_DIR" \
  </dev/null >"$OUT_FILE" 2>/dev/null &
DOCTOR_PID=$!
# The watchdog's own stdio goes to /dev/null: a sleeper that inherited this
# hook's stdout would hold Claude Code's pipe open for the full timeout.
( sleep 3; kill -9 "$DOCTOR_PID" 2>/dev/null ) </dev/null >/dev/null 2>&1 &
WATCHDOG_PID=$!
wait "$DOCTOR_PID" 2>/dev/null
DOCTOR_RC=$?
kill "$WATCHDOG_PID" 2>/dev/null
[ "$DOCTOR_RC" -eq 0 ] || exit 0

# One jq pass over the Report's camelCase fields — no alias for any of them.
# Fields are joined by \x1f (not a whitespace IFS char, so empty ones survive);
# the missing NAMES inside the last field by \x1e, so a name with a space in it
# stays ONE (invalid) name instead of splitting into two valid-looking ones.
FIELDS="$(jq -r '
  def arr(f): if (f | type) == "array" then f else [] end;
  def str(f): if (f | type) == "string" then f else "" end;
  select(type == "object")
  | [ str(.account), str(.estate),
      (arr(.varsExpected) | length | tostring),
      (arr(.varsPresent) | length | tostring),
      (if .launchedViaSwarmery == true then "1" else "0" end),
      (if .daemon == true then "1" else "0" end),
      (if (arr(.findings) | map(select(type == "object" and .id == "first-sight")) | length) > 0 then "1" else "0" end),
      (str(.estateRoot) | if startswith("/") then @json else "" end),
      (if (.credentials | type) == "number" then (.credentials | floor | tostring) else "" end),
      (arr(.varsMissing) | map(select(type == "string")) | join("\u001e"))
    ] | join("\u001f")' "$OUT_FILE" 2>/dev/null)" || exit 0
[ -n "$FIELDS" ] || exit 0
IFS=$'\x1f' read -r ACCOUNT ESTATE N_EXPECTED N_PRESENT LAUNCHED DAEMON FIRST_SIGHT ESTATE_ROOT N_CREDS MISSING_RAW <<<"$FIELDS" || exit 0

valid_account_key "$ACCOUNT" || exit 0
[ -z "$ESTATE" ] || valid_account_key "$ESTATE" || exit 0
case "$N_EXPECTED$N_PRESENT" in ''|*[!0-9]*) exit 0 ;; esac

# ── statusline cache: lengths and keys only, never a name or a value ──────────
write_cache() {
  local dir file tmp
  case "${SESSION_ID:-}" in ''|*[!A-Za-z0-9_-]*) return 0 ;; esac
  [ -n "${HOME:-}" ] || return 0
  dir="${HOME}/.swarmery/run/preflight"
  (umask 077 && mkdir -p "$dir") 2>/dev/null || return 0
  chmod 700 "$dir" 2>/dev/null || return 0
  file="$dir/${SESSION_ID}.env"
  tmp="$(umask 077 && mktemp "$dir/.${SESSION_ID}.XXXXXX" 2>/dev/null)" || return 0
  printf 'account=%s\nestate=%s\nvarsExpected=%s\nvarsPresent=%s\nlaunch=%s\n' \
    "$ACCOUNT" "$ESTATE" "$N_EXPECTED" "$N_PRESENT" "$LAUNCHED" >"$tmp" 2>/dev/null &&
    chmod 600 "$tmp" 2>/dev/null &&
    mv -f "$tmp" "$file" 2>/dev/null || rm -f "$tmp" 2>/dev/null
  return 0
}
write_cache

# ── the ONE escalation: a non-empty varsMissing ───────────────────────────────
MISSING=()
RAW_NAMES=()
# read -a, not an unquoted expansion: a "*" in the payload must never glob.
IFS=$'\x1e' read -r -a RAW_NAMES <<<"$MISSING_RAW" || true
for name in "${RAW_NAMES[@]+"${RAW_NAMES[@]}"}"; do
  valid_var_name "$name" && MISSING+=("$name")
done
# ── the first-sight clause (D3): a path new under an estate root ──────────────
# One sentence naming the estate root and the credential COUNT — never a name,
# never a value. The doctor reports it once per path (its ledger) and has
# already recorded it, so the sentence must not be dropped for an unusual
# path: the root arrives as a JSON string literal (jq @json above) — quoted,
# with quotes, backslashes and control characters escaped, non-ASCII and
# spaces kept — so nothing in it can break out of the quotes or the one line.
FIRST=""
if [ "$FIRST_SIGHT" = "1" ] && [ -n "$ESTATE_ROOT" ]; then
  case "$N_CREDS" in ''|*[!0-9]*) N_CREDS="" ;; esac
  case "$ESTATE_ROOT" in
    '"/'*'"') [ -n "$N_CREDS" ] && FIRST="First session in this directory: it sits under estate root ${ESTATE_ROOT} and inherits that estate's ${N_CREDS} credential(s). If this checkout is not yours to trust, move it out of the estate." ;;
  esac
fi

[ "${#MISSING[@]}" -gt 0 ] || [ -n "$FIRST" ] || exit 0

CTX=""
if [ "${#MISSING[@]}" -gt 0 ]; then
  NAMES="$(printf '%s, ' "${MISSING[@]}")"
  NAMES="${NAMES%, }"
  WHERE="account '${ACCOUNT}'"
  [ -n "$ESTATE" ] && WHERE="${WHERE}, estate '${ESTATE}'"
  CTX="Credential coverage gap: ${#MISSING[@]} of ${N_EXPECTED} MCP variable(s) referenced by this project's enabled plugins are unset in this session (${WHERE}): ${NAMES}."
  if [ "$DAEMON" = "1" ]; then
    CTX="${CTX} This session runs in a swarmery daemon worktree, so the gap is in the daemon's spawn seam, not in a terminal: check the estate's credential store and the environment the daemon spawns with."
  elif [ "$LAUNCHED" != "1" ]; then
    CTX="${CTX} This session was not launched through \`swarmery account exec\`. MCP env is read once at process start and cannot be repaired in-session: exit and open a new shell, then start claude again (or run \`swarmery account exec -- claude\`)."
  else
    CTX="${CTX} The launch went through swarmery, so nothing supplies these names: add them to the estate's credential store (\`swarmery account estate show\`), then restart the session."
  fi
fi
if [ -n "$FIRST" ]; then
  CTX="${CTX:+$CTX }${FIRST}"
fi
printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":%s}}\n' \
  "$(printf '%s' "$CTX" | jq -Rs .)"
exit 0
