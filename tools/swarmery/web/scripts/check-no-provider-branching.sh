#!/usr/bin/env bash
# check-no-provider-branching.sh — SC-11 ratchet for the landing UI.
#
# Every landing label the web app shows ("Open PR" / "Open MR", "PR #12") comes
# from the API's `terms`, and the sign-in command from the API's `cliLogin`. So
# the landing surfaces must never branch on which provider a project uses: no
# `=== 'github'`, `!== "gitlab"`, `'gitlab' ===` or `case 'github':` in them.
# Type literals (`provider: 'github' | 'gitlab'`) and an icon map keyed by
# provider are allowed — neither is a comparison. The match ignores case, so a
# comparison against a display name (`=== 'GitHub'`) is caught too.
#
# Exits 0 when clean, 1 on a hit (printing every hit), 2 when a scanned path is
# missing or grep itself fails (an unreadable file must not pass as clean).
# Resolves its targets relative to this script, so it runs from any cwd.

set -euo pipefail

web_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
targets=(
  "$web_dir/src/pages/plans"
  "$web_dir/src/pages/Plans.tsx"
  "$web_dir/src/components/VcsAuthBanner.tsx"
  "$web_dir/src/components/VcsProviderAsk.tsx"
  "$web_dir/src/components/VcsSignInDialog.tsx"
  "$web_dir/src/lib/useProjectVcs.ts"
)

for t in "${targets[@]}"; do
  if [[ ! -e "$t" ]]; then
    echo "check-no-provider-branching: missing scan target: $t" >&2
    exit 2
  fi
done

q="['\"]"
provider="(github|gitlab)"
pattern="[!=]==[[:space:]]*${q}${provider}${q}|${q}${provider}${q}[[:space:]]*[!=]==|case[[:space:]]+${q}${provider}${q}"

# grep: 0 = a hit, 1 = no match (the clean case), anything else = grep could
# not read a target — that is a failure, not a pass.
if hits="$(grep -rniE --include='*.ts' --include='*.tsx' "$pattern" "${targets[@]}")"; then
  rc=0
else
  rc=$?
fi

case "$rc" in
  0)
    echo "check-no-provider-branching: provider conditionals found (SC-11) — read the label from terms instead:" >&2
    echo "$hits" >&2
    exit 1
    ;;
  1) ;;
  *)
    echo "check-no-provider-branching: grep failed (exit $rc) — a scan target could not be read" >&2
    if [[ -n "$hits" ]]; then
      echo "and it found provider conditionals (SC-11) in what it could read:" >&2
      echo "$hits" >&2
    fi
    exit 2
    ;;
esac

echo "check-no-provider-branching: clean (${#targets[@]} targets scanned)"
