#!/bin/bash
# hook-lib.sh — shared body of the prod-deploy-ask probe's fixture PreToolUse
# hooks (hook-ask.sh, hook-deny.sh). Sourced, never run on its own.
#
# probe_hook <decision> — record the evidence, then answer with <decision>:
#   $PROBE_OUT/$PROBE_LEG.payload.json  the hook's stdin, verbatim
#   $PROBE_OUT/$PROBE_LEG.env           the hook's environment, one NAME=value per
#                                       line; values of names that look like
#                                       credentials (TOKEN|KEY|SECRET|PASSW|AUTH|
#                                       COOKIE) are replaced by <redacted>
# The probe sets PROBE_OUT and PROBE_LEG in the CLI's environment, which the CLI
# passes down to its hooks. Without them the hook still answers, it just keeps
# no evidence.
probe_hook() {
  local decision="$1" payload
  payload="$(cat)"
  if [ -n "${PROBE_OUT:-}" ] && [ -n "${PROBE_LEG:-}" ] && [ -d "$PROBE_OUT" ]; then
    printf '%s\n' "$payload" > "$PROBE_OUT/$PROBE_LEG.payload.json"
    env | LC_ALL=C sort | awk -F= '
      $1 ~ /TOKEN|KEY|SECRET|PASSW|AUTH|COOKIE/ { print $1 "=<redacted>"; next }
      { print }' > "$PROBE_OUT/$PROBE_LEG.env"
  fi
  printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"%s","permissionDecisionReason":"probe"}}\n' "$decision"
}
