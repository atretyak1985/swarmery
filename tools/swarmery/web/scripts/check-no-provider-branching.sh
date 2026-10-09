#!/usr/bin/env bash
# check-no-provider-branching.sh — SC-11 ratchet for the landing UI.
#
# Every landing label the web app shows ("Open PR" / "Open MR", "PR #12") comes
# from the API's `terms`, and the sign-in command from the API's `cliLogin`. So
# the landing surfaces must never branch on which provider a project uses: no
# `=== 'github'`, `!== "gitlab"`, `'gitlab' ===` or `case 'github':` in them.
# Type literals (`provider: 'github' | 'gitlab'`) and an icon map keyed by
# provider are allowed — neither is a comparison.
#
# Exits 0 when clean, 1 on a hit (printing every hit), 2 when a scanned path is
# missing. Resolves its targets relative to this script, so it runs from any cwd.

set -euo pipefail

web_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
targets=(
  "$web_dir/src/pages/plans"
  "$web_dir/src/components/VcsAuthBanner.tsx"
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

# grep exits 1 on no match; that is the clean case, not an error.
hits="$(grep -rnE --include='*.ts' --include='*.tsx' "$pattern" "${targets[@]}" || true)"

if [[ -n "$hits" ]]; then
  echo "check-no-provider-branching: provider conditionals found (SC-11) — read the label from terms instead:" >&2
  echo "$hits" >&2
  exit 1
fi

echo "check-no-provider-branching: clean (${#targets[@]} targets scanned)"
