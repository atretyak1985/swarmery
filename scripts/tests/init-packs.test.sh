#!/usr/bin/env bash
# init-packs.test.sh — the bootstrap pack lists must not drift from plugins/.
#
# Four places name the packs a consumer may enable, and nothing regenerates
# them when a plugin is added:
#   - scripts/init.sh, the usage line and the `case` guard;
#   - overlays/_schema/project.schema.json, the `enabledPacks` item enum;
#   - tools/swarmery/internal/onboard/onboard.go, KnownPacks (init.sh hands off
#     to `swarmery onboard` whenever the binary is on PATH).
# Every plugins/*/.claude-plugin/plugin.json name except `core` must appear in
# each of them, and nothing may appear there that is not a plugin dir.
#
# It also runs init.sh itself (with any `swarmery` binary kept off PATH, so the
# bash path runs, in a scratch project and workspace) and checks that the mod
# packs land in both enabledPlugins and enabledPacks.
#
# Offline and framework-free. Needs node. Run with
# `bash scripts/tests/init-packs.test.sh`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
INIT="${ROOT}/scripts/init.sh"
SCHEMA="${ROOT}/overlays/_schema/project.schema.json"
ONBOARD="${ROOT}/tools/swarmery/internal/onboard/onboard.go"

pass=0
fail=0
ok()  { pass=$((pass + 1)); printf '  ok    %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  FAIL  %s\n        %s\n' "$1" "$2"; }

# eq <desc> <expected> <actual>
eq() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "expected [$2], got [$3]"; fi; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "init-packs"

if ! command -v node >/dev/null 2>&1; then
  bad "node on PATH" "this suite reads JSON with node"
  printf 'init-packs: %d passed, %d failed\n' "$pass" "$fail"
  exit 1
fi

# ── the four lists, one name per line, sorted ───────────────────────────────

# plugins: every plugin.json name except core.
node -e '
  const fs = require("fs"), path = require("path");
  const dir = path.join(process.argv[1], "plugins");
  const names = [];
  for (const d of fs.readdirSync(dir)) {
    const f = path.join(dir, d, ".claude-plugin", "plugin.json");
    if (!fs.existsSync(f)) continue;
    const n = JSON.parse(fs.readFileSync(f, "utf8")).name;
    if (n !== "core") names.push(n);
  }
  process.stdout.write(names.sort().join("\n") + "\n");
' "$ROOT" >"$TMP/plugins"

# init.sh case guard: the `case "$p" in a|b|…|"") ;;` line.
# shellcheck disable=SC2016  # a literal `$p` to match, not to expand
grep -m 1 'case "$p" in' "$INIT" |
  sed -e 's/.*case "$p" in //' -e 's/) ;;.*//' |
  tr '|' '\n' | grep -v '^""$' | grep -v '^$' | sort >"$TMP/init-case"

# init.sh usage line: `packs: a | b | …"`.
grep -m 1 'usage: init.sh' "$INIT" |
  sed -e 's/.*packs: //' -e 's/"$//' |
  tr '|' '\n' | sed -e 's/^ *//' -e 's/ *$//' | grep -v '^$' | sort >"$TMP/init-usage"

# schema enum.
node -e '
  const fs = require("fs");
  const s = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  const e = s.properties.enabledPacks.items.enum;
  process.stdout.write([...e].sort().join("\n") + "\n");
' "$SCHEMA" >"$TMP/schema"

# onboard.go KnownPacks.
grep -m 1 '^var KnownPacks' "$ONBOARD" |
  grep -o '"[^"]*"' | tr -d '"' | sort >"$TMP/onboard"

n_plugins="$(grep -c . "$TMP/plugins" || true)"
if [ "$n_plugins" -ge 1 ]; then ok "found $n_plugins non-core plugins"; else bad "found non-core plugins" "none under ${ROOT}/plugins"; fi

for list in init-case init-usage schema onboard; do
  n="$(grep -c . "$TMP/$list" || true)"
  if [ "$n" -ge 1 ]; then ok "$list: read $n names"; else bad "$list: read names" "parsed nothing — did the line's shape change?"; fi
  missing="$(comm -23 "$TMP/plugins" "$TMP/$list" | tr '\n' ' ' | sed 's/ *$//')"
  extra="$(comm -13 "$TMP/plugins" "$TMP/$list" | tr '\n' ' ' | sed 's/ *$//')"
  eq "$list: lists every non-core plugin" "" "$missing"
  eq "$list: lists nothing that is not a plugin dir" "" "$extra"
  dups="$(uniq -d "$TMP/$list" | tr '\n' ' ' | sed 's/ *$//')"
  eq "$list: no duplicates" "" "$dups"
done

# ── init.sh accepts the mod packs ───────────────────────────────────────────
# PATH without any directory that holds a `swarmery` binary, so init.sh takes
# its bash path instead of exec'ing whatever control plane is installed.
clean_path=""
IFS=':' read -r -a path_dirs <<<"$PATH"
for d in "${path_dirs[@]}"; do
  [ -n "$d" ] || continue
  [ -x "$d/swarmery" ] && continue
  clean_path="${clean_path:+${clean_path}:}$d"
done

proj="$TMP/proj"
mkdir -p "$proj"
out="$(cd "$proj" && PATH="$clean_path" SWARMERY_WORKSPACE_ROOT="$TMP/ws" \
  bash "$INIT" demo-proj swarmery-link guard-pack 2>&1)"
rc=$?
eq "init.sh demo-proj swarmery-link guard-pack exits 0" "0" "$rc"
[ "$rc" -eq 0 ] || printf '        %s\n' "$(printf '%s' "$out" | tail -n 3)"

if [ -f "$proj/.claude/settings.json" ] && [ -f "$proj/.claude/project.json" ]; then
  got="$(node -e '
    const fs = require("fs");
    const s = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    const p = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
    const plugins = Object.keys(s.enabledPlugins).filter((k) => s.enabledPlugins[k] === true).sort();
    process.stdout.write(plugins.join(",") + " | " + [...p.enabledPacks].sort().join(","));
  ' "$proj/.claude/settings.json" "$proj/.claude/project.json")"
  eq "enabledPlugins and enabledPacks carry both mods" \
    "core@swarmery,guard-pack@swarmery,swarmery-link@swarmery | guard-pack,swarmery-link" "$got"
else
  bad "init.sh wrote settings.json and project.json" "missing under $proj/.claude"
fi

# An unknown pack is still refused.
proj2="$TMP/proj2"
mkdir -p "$proj2"
(cd "$proj2" && PATH="$clean_path" SWARMERY_WORKSPACE_ROOT="$TMP/ws" \
  bash "$INIT" demo-proj not-a-pack >/dev/null 2>&1)
rc=$?
eq "init.sh refuses an unknown pack" "1" "$rc"

printf 'init-packs: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
