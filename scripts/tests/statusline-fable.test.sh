#!/bin/bash
# Behavioral tests for the statusline Fable-usage pipeline:
#   plugins/core/statusline/fetch-fable-usage.sh  (helper: API response -> "PCT|RESET" / "none|")
#   plugins/core/statusline/statusline.sh         (render: cache file -> FB segment shown/hidden)
#
# Framework-free (portable, no bats dependency), fully offline:
#   - the helper is pointed at file:// fixtures via SWARMERY_FABLE_USAGE_URL and given a
#     dummy token via SWARMERY_FABLE_TOKEN (so no Keychain and no network are touched;
#     SWARMERY_FABLE_KEYCHAIN_SERVICE is set to a nonexistent item as a belt-and-suspenders
#     guard that the keychain path is never taken);
#   - the statusline is isolated with a private TMPDIR (own fable + weather caches) and a
#     fixed fake CLAUDE_CONFIG_DIR so cache slugs are deterministic.
# Run locally with `bash scripts/tests/statusline-fable.test.sh`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HELPER="$ROOT/plugins/core/statusline/fetch-fable-usage.sh"
STATUSLINE="$ROOT/plugins/core/statusline/statusline.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0

ok()  { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n     expected: %s\n     actual:   %s\n' "$1" "$2" "$3"; }

# ── fixtures ──────────────────────────────────────────────────────
# Org/Team account shape: limits[] has session + weekly_all, NO Fable-scoped window.
cat > "$WORK/org.json" <<'JSON'
{"limits":[
  {"kind":"session","group":"session","percent":26,"resets_at":"2026-07-20T15:29:59+00:00","scope":null},
  {"kind":"weekly_all","group":"weekly","percent":9,"resets_at":"2026-07-20T13:59:59+00:00","scope":null}
]}
JSON
# Personal account shape: includes the Fable weekly_scoped window.
cat > "$WORK/personal.json" <<'JSON'
{"limits":[
  {"kind":"session","group":"session","percent":1,"resets_at":"2026-07-20T16:30:00+00:00","scope":null},
  {"kind":"weekly_all","group":"weekly","percent":22,"resets_at":"2026-07-22T10:00:00+00:00","scope":null},
  {"kind":"weekly_scoped","group":"weekly","percent":32,"resets_at":"2026-07-22T10:00:00+00:00","scope":{"model":{"id":null,"display_name":"Fable"}}}
]}
JSON
printf 'not json at all' > "$WORK/garbage.txt"

# ── helper tests ──────────────────────────────────────────────────
# run_helper <fixture-url> — offline invocation with a dummy token.
run_helper() {
  SWARMERY_FABLE_TOKEN="dummy-test-token" \
  SWARMERY_FABLE_KEYCHAIN_SERVICE="swarmery-test-nonexistent-item" \
  SWARMERY_FABLE_USAGE_URL="$1" \
  bash "$HELPER" 2>/dev/null
}

out="$(run_helper "file://$WORK/org.json")"
if [ "$out" = "none|" ]; then ok; else bad "helper: no Fable window -> none| marker" "none|" "'$out'"; fi

out="$(run_helper "file://$WORK/personal.json")"
case "$out" in
  32\|*) ok ;;
  *) bad "helper: Fable window -> PCT|RESET" "32|<reset>" "'$out'" ;;
esac

out="$(run_helper "file://$WORK/garbage.txt")"
if [ -z "$out" ]; then ok; else bad "helper: non-JSON response -> empty (silent failure)" "''" "'$out'"; fi

out="$(run_helper "file://$WORK/does-not-exist.json")"
if [ -z "$out" ]; then ok; else bad "helper: fetch failure -> empty (silent failure)" "''" "'$out'"; fi

# ── statusline render tests ───────────────────────────────────────
CFG_DIR="$WORK/fake-config-dir"
SLUG="$(printf '%s' "$CFG_DIR" | shasum -a 256 | cut -c1-8)"
TMP="$WORK/tmp"; mkdir -p "$TMP" "$CFG_DIR"
FB_CACHE="$TMP/agents-statusline-fable-${SLUG}.txt"
# Pre-warm the weather cache so the statusline never curls wttr.in during tests.
printf 'TestCity|+20°C|Sunny\n' > "$TMP/agents-statusline-wx-Lviv.txt"

STDIN_JSON="$(printf '{"model":{"display_name":"Fable 5"},"version":"2.1.211","workspace":{"current_dir":"%s","project_dir":"%s"},"rate_limits":{"five_hour":{"used_percentage":26,"resets_at":1784000000},"seven_day":{"used_percentage":9,"resets_at":1784100000}}}' "$WORK" "$WORK")"

# render — run the statusline with the isolated env, ANSI stripped; background
# refresh (if any) is pointed at a missing fixture so it can never write the cache.
render() {
  printf '%s' "$STDIN_JSON" | \
    TMPDIR="$TMP" \
    CLAUDE_CONFIG_DIR="$CFG_DIR" \
    SWARMERY_STATUSLINE_FABLE=1 \
    SWARMERY_FABLE_TOKEN="dummy-test-token" \
    SWARMERY_FABLE_KEYCHAIN_SERVICE="swarmery-test-nonexistent-item" \
    SWARMERY_FABLE_USAGE_URL="file://$WORK/does-not-exist.json" \
    bash "$STATUSLINE" 2>/dev/null | perl -pe 's/\e\[[0-9;]*m//g'
}

printf '32|2026-07-22T10:00:00+00:00\n' > "$FB_CACHE"
out="$(render)"
case "$out" in
  *"FB 32%"*) ok ;;
  *) bad "statusline: fresh numeric cache -> FB shown" "USAGE line containing 'FB 32%'" "$(printf '%s' "$out" | grep USAGE || echo '<no USAGE line>')" ;;
esac

printf 'none|\n' > "$FB_CACHE"
out="$(render)"
if printf '%s' "$out" | grep -q "USAGE" && ! printf '%s' "$out" | grep -q "FB"; then ok
else bad "statusline: none| marker -> FB hidden" "USAGE line without FB" "$(printf '%s' "$out" | grep USAGE || echo '<no USAGE line>')"; fi

printf '32|2026-07-22T10:00:00+00:00\n' > "$FB_CACHE"
touch -t "$(date -v-25H +%Y%m%d%H%M 2>/dev/null || date -d '25 hours ago' +%Y%m%d%H%M)" "$FB_CACHE"
out="$(render)"
if printf '%s' "$out" | grep -q "USAGE" && ! printf '%s' "$out" | grep -q "FB"; then ok
else bad "statusline: cache older than 24h -> FB hidden" "USAGE line without FB" "$(printf '%s' "$out" | grep USAGE || echo '<no USAGE line>')"; fi

printf 'statusline-fable: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
