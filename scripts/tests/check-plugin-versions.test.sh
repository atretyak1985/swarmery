#!/usr/bin/env bash
# check-plugin-versions.test.sh — the marketplace/plugin.json version gate.
#
# Runs scripts/check-plugin-versions.sh against the real tree (must pass) and
# against small fixture trees, one defect each (must fail, naming the defect).
#
# Offline and framework-free. Run with `bash scripts/tests/check-plugin-versions.test.sh`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHECK="${ROOT}/scripts/check-plugin-versions.sh"

pass=0
fail=0
ok()  { pass=$((pass + 1)); printf '  ok    %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  FAIL  %s\n        %s\n' "$1" "$2"; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# tree <dir> <metadata.version> <core-version> <pack-version> [<pack marketplace version>]
# A two-plugin marketplace: core and pack.
tree() {
  local dir="$1" meta="$2" corev="$3" packv="$4" packmv="${5:-}"
  local extra=""
  [ -n "$packmv" ] && extra=", \"version\": \"${packmv}\""
  mkdir -p "${dir}/.claude-plugin" "${dir}/plugins/core/.claude-plugin" "${dir}/plugins/pack/.claude-plugin"
  cat >"${dir}/.claude-plugin/marketplace.json" <<JSON
{ "name": "m", "metadata": { "version": "${meta}" },
  "plugins": [
    { "name": "core", "source": "./plugins/core" },
    { "name": "pack", "source": "./plugins/pack"${extra} }
  ] }
JSON
  printf '{ "name": "core", "version": "%s" }\n' "$corev" >"${dir}/plugins/core/.claude-plugin/plugin.json"
  printf '{ "name": "pack", "version": "%s" }\n' "$packv" >"${dir}/plugins/pack/.claude-plugin/plugin.json"
}

# expect <desc> <want: pass|fail> <dir> [<needle in output>]
expect() {
  local desc="$1" want="$2" dir="$3" needle="${4:-}" out rc
  out="$(PLUGIN_VERSIONS_ROOT="$dir" bash "$CHECK" 2>&1)"
  rc=$?
  if [ "$want" = pass ] && [ "$rc" -ne 0 ]; then bad "$desc" "exit $rc: $out"; return; fi
  if [ "$want" = fail ] && [ "$rc" -eq 0 ]; then bad "$desc" "passed, want a failure: $out"; return; fi
  if [ -n "$needle" ]; then
    case "$out" in *"$needle"*) ;; *) bad "$desc" "output lacks [$needle]: $out"; return ;; esac
  fi
  ok "$desc"
}

echo "check-plugin-versions"

if out="$(bash "$CHECK" 2>&1)"; then
  ok "the repository's own marketplace is consistent"
else
  bad "the repository's own marketplace is consistent" "$out"
fi

tree "${TMP}/good" 3.1.0 3.1.0 0.2.0
expect "a consistent tree passes" pass "${TMP}/good" "plugin versions consistent (2 plugins)"

tree "${TMP}/copy-equal" 3.1.0 3.1.0 0.2.0 0.2.0
expect "a marketplace copy equal to plugin.json passes" pass "${TMP}/copy-equal" "marketplace copy equal"

tree "${TMP}/copy-differs" 3.1.0 3.1.0 0.2.0 0.1.0
expect "a marketplace copy that differs fails" fail "${TMP}/copy-differs" 'marketplace version "0.1.0" differs'

tree "${TMP}/meta" 3.0.0 3.1.0 0.2.0
expect "metadata.version off core's fails" fail "${TMP}/meta" "metadata.version"

tree "${TMP}/semver" 3.1.0 3.1.0 0.2
expect "a two-part version fails" fail "${TMP}/semver" "is not valid semver"

tree "${TMP}/leading-zero" 3.1.0 3.1.0 0.02.0
expect "a leading zero fails" fail "${TMP}/leading-zero" "is not valid semver"

tree "${TMP}/prerelease" 3.1.0 3.1.0 1.0.0-rc.1+build.5
expect "a pre-release with build metadata passes" pass "${TMP}/prerelease"

tree "${TMP}/name" 3.1.0 3.1.0 0.2.0
printf '{ "name": "other", "version": "0.2.0" }\n' >"${TMP}/name/plugins/pack/.claude-plugin/plugin.json"
expect "a plugin.json name that differs fails" fail "${TMP}/name" 'name is "other"'

tree "${TMP}/missing" 3.1.0 3.1.0 0.2.0
rm "${TMP}/missing/plugins/pack/.claude-plugin/plugin.json"
expect "a listed plugin without plugin.json fails" fail "${TMP}/missing" "plugin.json missing"

tree "${TMP}/unlisted" 3.1.0 3.1.0 0.2.0
mkdir -p "${TMP}/unlisted/plugins/stray/.claude-plugin"
printf '{ "name": "stray", "version": "0.1.0" }\n' >"${TMP}/unlisted/plugins/stray/.claude-plugin/plugin.json"
expect "a plugin missing from the marketplace fails" fail "${TMP}/unlisted" "plugins/stray has a plugin.json but no marketplace entry"

printf 'check-plugin-versions: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
