#!/bin/bash
# Behavioral tests for scripts/sync-cache.sh.
#
# Framework-free. Each case builds a throwaway HOME with fake Claude config dirs
# (a default ~/.claude and an account ~/.claude-acct), each carrying a plugin
# cache and an installed_plugins.json, plus a fake source plugins/ tree, and
# asserts where the script writes:
#   (a) only into the version dir a config dir has INSTALLED, never into a
#       stray sibling version dir;
#   (b) into every config dir, not just ~/.claude;
#   (c) a version drift between installed and source is said out loud;
#   (d) a recorded-but-missing install path is skipped, not created.
# Run locally with `bash scripts/tests/sync-cache.test.sh`; CI discovers
# scripts/tests/*.test.sh.
set -uo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/scripts/sync-cache.sh"

pass=0
fail=0
fail_case() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }
ok_case() { pass=$((pass + 1)); }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# --- fixture ---------------------------------------------------------------
SRC="$tmp/plugins"
mkdir -p "$SRC/core/.claude-plugin" "$SRC/core/hooks" "$SRC/web-pack/.claude-plugin"
printf '{"name":"core","version":"2.0.0"}\n' > "$SRC/core/.claude-plugin/plugin.json"
printf '{"name":"web-pack","version":"1.0.0"}\n' > "$SRC/web-pack/.claude-plugin/plugin.json"
printf 'echo new\n' > "$SRC/core/hooks/marker.sh"
printf 'x\n' > "$SRC/web-pack/README.md"

HOME_DIR="$tmp/home"
DEF="$HOME_DIR/.claude"
ACCT="$HOME_DIR/.claude-acct"
# default dir: core installed at 1.9.0 (drift vs source 2.0.0); a stray 1.8.0 dir
# that must stay untouched; web-pack installed at 1.0.0 (no drift).
mkdir -p "$DEF/plugins/cache/swarmery/core/1.9.0/.claude-plugin" \
         "$DEF/plugins/cache/swarmery/core/1.8.0" \
         "$DEF/plugins/cache/swarmery/web-pack/1.0.0/.claude-plugin"
printf '{"name":"core","version":"1.9.0"}\n' > "$DEF/plugins/cache/swarmery/core/1.9.0/.claude-plugin/plugin.json"
printf 'old\n' > "$DEF/plugins/cache/swarmery/core/1.8.0/stale.txt"
cat > "$DEF/plugins/installed_plugins.json" <<EOF
{"version":2,"plugins":{
  "core@swarmery":[{"scope":"user","version":"1.9.0","installPath":"$DEF/plugins/cache/swarmery/core/1.9.0"}],
  "web-pack@swarmery":[{"scope":"project","projectPath":"/p","version":"1.0.0","installPath":"$DEF/plugins/cache/swarmery/web-pack/1.0.0"}],
  "other@elsewhere":[{"scope":"user","version":"9.9.9","installPath":"$DEF/plugins/cache/elsewhere/other/9.9.9"}]
}}
EOF
# account dir: core installed at 2.0.0 (matches source); a recorded-but-missing
# install path for web-pack.
mkdir -p "$ACCT/plugins/cache/swarmery/core/2.0.0/.claude-plugin"
printf '{"name":"core","version":"2.0.0"}\n' > "$ACCT/plugins/cache/swarmery/core/2.0.0/.claude-plugin/plugin.json"
cat > "$ACCT/plugins/installed_plugins.json" <<EOF
{"plugins":{
  "core@swarmery":[{"scope":"user","version":"2.0.0","installPath":"$ACCT/plugins/cache/swarmery/core/2.0.0"}],
  "web-pack@swarmery":[{"scope":"user","version":"1.0.0","installPath":"$ACCT/plugins/cache/swarmery/web-pack/1.0.0"}]
}}
EOF
# a third config dir with no swarmery installs at all
mkdir -p "$HOME_DIR/.claude-empty/plugins"

out="$(HOME="$HOME_DIR" SWARMERY_PLUGINS_DIR="$SRC" bash "$SCRIPT" 2>&1)"
rc=$?

# --- assertions ------------------------------------------------------------
[ "$rc" -eq 0 ] && ok_case || fail_case "exit code $rc, output: $out"

[ -f "$DEF/plugins/cache/swarmery/core/1.9.0/hooks/marker.sh" ] && ok_case \
  || fail_case "default dir: installed core/1.9.0 did not receive the source tree"

[ ! -e "$DEF/plugins/cache/swarmery/core/1.8.0/hooks" ] && [ -f "$DEF/plugins/cache/swarmery/core/1.8.0/stale.txt" ] && ok_case \
  || fail_case "default dir: stray core/1.8.0 was written to — only the INSTALLED version dir may change"

grep -q '"version":"1.9.0"' "$DEF/plugins/cache/swarmery/core/1.9.0/.claude-plugin/plugin.json" && ok_case \
  || fail_case "default dir: .claude-plugin/ of the installed copy was overwritten (must be excluded)"

[ -f "$DEF/plugins/cache/swarmery/web-pack/1.0.0/README.md" ] && ok_case \
  || fail_case "default dir: project-scoped web-pack install was not synced"

[ -f "$ACCT/plugins/cache/swarmery/core/2.0.0/hooks/marker.sh" ] && ok_case \
  || fail_case "account dir ~/.claude-acct was not synced — only ~/.claude was"

[ ! -e "$ACCT/plugins/cache/swarmery/web-pack" ] && ok_case \
  || fail_case "account dir: a recorded-but-missing install path was created instead of skipped"

[ ! -e "$DEF/plugins/cache/elsewhere" ] && ok_case \
  || fail_case "a plugin from another marketplace was touched"

printf '%s\n' "$out" | grep -q 'installed 1.9.0, source 2.0.0' && ok_case \
  || fail_case "version drift (installed 1.9.0 vs source 2.0.0) was not reported; output: $out"

printf '%s\n' "$out" | grep -q '3 installed dir(s) updated' && ok_case \
  || fail_case "summary should count 3 synced dirs; output: $out"

printf '%s\n' "$out" | grep -q '1 with a version drift' && ok_case \
  || fail_case "summary should count 1 drift; output: $out"

printf '%s\n' "$out" | grep -q '1 config dir(s) without swarmery installs' && ok_case \
  || fail_case "summary should count the empty config dir; output: $out"

printf 'sync-cache: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
