#!/bin/bash
# live-originless-auth.sh — live check: non-browser clients authenticate to the
# daemon.
#
# Builds the swarmery binary and starts it ISOLATED — port 7796, a throwaway
# database, HOME pointed at a scratch dir by a launcher script, --no-ingest —
# then asserts over HTTP:
#   - a raw Origin-less POST /api/routines without auth gets 401 and creates nothing;
#   - the same write through `swarmery api` (signed) gets 201;
#   - the daemon's own browser Origin without a session is refused (401
#     dashboard_locked);
#   - after an unlock (`swarmery open --print` + POST /api/session/unlock) the
#     same own-Origin write carrying X-Swarmery-Session gets 201;
#   - plain GETs need no credential;
#   - the hook shim (`swarmery hook stop`, SWARMERY_PORT=7796) is accepted (http-202);
#   - `serve --bind 0.0.0.0 --port 7797` with an empty HOME refuses to start
#     and mints no token.
# It never touches a daemon on :7777 or anything under ~/.swarmery. Requires go,
# curl. Deliberately NOT named *.test.sh: it builds a binary and binds ports.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PORT=7796
WIDE_PORT=7797
BASE="http://127.0.0.1:$PORT"

for tool in go curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "live-originless-auth: $tool not found" >&2; exit 2; }
done
for p in "$PORT" "$WIDE_PORT"; do
  if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:$p/"; then
    echo "live-originless-auth: something already listens on :$p — refusing to share it" >&2
    exit 2
  fi
done

SCRATCH="$(mktemp -d)"
PID=""
cleanup() {
  if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  rm -rf "$SCRATCH"
}
trap cleanup EXIT

mkdir -p "$SCRATCH/home" "$SCRATCH/empty-home" "$SCRATCH/tmp"
BIN="$SCRATCH/swarmery"
cat >"$SCRATCH/launch.sh" <<EOF
#!/bin/bash
export HOME="\$1"; shift
export TMPDIR="$SCRATCH/tmp"
export PATH="/usr/bin:/bin:/usr/sbin:/sbin"
exec "\$@"
EOF
chmod +x "$SCRATCH/launch.sh"

echo "→ building $BIN"
go -C "$ROOT/tools/swarmery" build -o "$BIN" ./cmd/swarmery

echo "→ starting isolated daemon on :$PORT (HOME $SCRATCH/home)"
env -i "$SCRATCH/launch.sh" "$SCRATCH/home" "$BIN" serve --db "$SCRATCH/s.db" --port "$PORT" --no-ingest \
  >"$SCRATCH/daemon.log" 2>&1 &
PID=$!
up=0
for _ in $(seq 1 150); do
  if curl -s -o /dev/null --max-time 1 "$BASE/api/health"; then up=1; break; fi
  kill -0 "$PID" 2>/dev/null || break
  sleep 0.2
done
if [ "$up" -ne 1 ]; then echo "daemon did not come up:"; cat "$SCRATCH/daemon.log"; exit 1; fi

failures=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; failures=$((failures + 1)); }
ROUTINE='{"name":"live-originless","steps":[{"type":"command","name":"c","command":"true"}]}'
count_routines() { curl -s "$BASE/api/routines" | grep -o '"name":"live-originless"' | wc -l | tr -d ' '; }

[ -s "$SCRATCH/home/.swarmery/link-token" ] && pass "the loopback daemon minted its link token" || fail "no link token minted"

got="$(curl -s -o "$SCRATCH/body" -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d "$ROUTINE" "$BASE/api/routines")"
[ "$got" = 401 ] && pass "raw Origin-less POST without auth ($got)" || fail "raw Origin-less POST = $got, want 401"
[ "$(count_routines)" = 0 ] && pass "the refused POST created nothing" || fail "the refused POST created a routine"

if env -i "$SCRATCH/launch.sh" "$SCRATCH/home" "$BIN" api POST /api/routines --port "$PORT" --data "$ROUTINE" \
  >"$SCRATCH/api.out" 2>"$SCRATCH/api.err"; then
  pass "swarmery api POST /api/routines (signed): $(head -c 60 "$SCRATCH/api.out")…"
else
  fail "swarmery api POST failed: $(cat "$SCRATCH/api.err")"
fi
[ "$(count_routines)" = 1 ] && pass "the signed POST created one routine" || fail "routines = $(count_routines), want 1"

got="$(curl -s -o "$SCRATCH/body" -w '%{http_code}' -X POST -H "Origin: $BASE" -H 'Content-Type: application/json' -d "$ROUTINE" "$BASE/api/routines")"
if [ "$got" = 401 ] && grep -q '"code":"dashboard_locked"' "$SCRATCH/body"; then
  pass "own-Origin POST without a session is locked ($got dashboard_locked)"
else
  fail "own-Origin POST without a session = $got $(head -c 120 "$SCRATCH/body"), want 401 dashboard_locked"
fi

# Unlock the way the dashboard does: `swarmery open --print` mints a ticket in
# the URL fragment; POST /api/session/unlock (Origin = the listener's literal
# address) trades it for a session the browser then sends on every write.
unlock_url=""
if unlock_url="$(env -i "$SCRATCH/launch.sh" "$SCRATCH/home" "$BIN" open --print --port "$PORT" 2>"$SCRATCH/open.err")"; then
  pass "swarmery open --print minted an unlock URL"
else
  fail "swarmery open --print failed: $(cat "$SCRATCH/open.err")"
fi
ticket="${unlock_url#*swarmery-unlock=}"
got="$(curl -s -o "$SCRATCH/unlock" -w '%{http_code}' -X POST -H "Origin: http://127.0.0.1:$PORT" \
  -H 'Content-Type: application/json' -d "{\"ticket\":\"$ticket\"}" "$BASE/api/session/unlock")"
session="$(sed -n 's/.*"session":"\([^"]*\)".*/\1/p' "$SCRATCH/unlock")"
if [ "$got" = 200 ] && [ -n "$session" ]; then
  pass "POST /api/session/unlock traded the ticket for a session ($got)"
else
  fail "unlock = $got $(head -c 120 "$SCRATCH/unlock"), want 200 with a session"
fi
got="$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Origin: $BASE" -H "X-Swarmery-Session: $session" \
  -H 'Content-Type: application/json' -d "$ROUTINE" "$BASE/api/routines")"
[ "$got" = 201 ] && pass "own-Origin POST with the session ($got)" || fail "own-Origin POST with the session = $got, want 201"

got="$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Origin: http://evil.example' -d "$ROUTINE" "$BASE/api/routines")"
[ "$got" = 403 ] && pass "foreign Origin ($got)" || fail "foreign Origin = $got, want 403"
got="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/routines")"
[ "$got" = 200 ] && pass "plain GET needs no credential ($got)" || fail "GET = $got, want 200"

echo '{"session_id":"live-originless","hook_event_name":"Stop"}' |
  env -i SWARMERY_PORT="$PORT" "$SCRATCH/launch.sh" "$SCRATCH/home" "$BIN" hook stop >/dev/null 2>&1 || true
if grep -q 'http-202' "$SCRATCH/home/.swarmery/hook.log" 2>/dev/null; then
  pass "hook shim (SWARMERY_PORT=$PORT) accepted: $(tail -1 "$SCRATCH/home/.swarmery/hook.log")"
else
  fail "hook shim outcome: $(tail -1 "$SCRATCH/home/.swarmery/hook.log" 2>/dev/null || echo 'no hook.log')"
fi

env -i "$SCRATCH/launch.sh" "$SCRATCH/empty-home" "$BIN" serve --bind 0.0.0.0 --port "$WIDE_PORT" \
  --no-ingest --db "$SCRATCH/wide.db" >"$SCRATCH/wide.log" 2>&1 &
WIDE_PID=$!
for _ in $(seq 1 100); do kill -0 "$WIDE_PID" 2>/dev/null || break; sleep 0.2; done
rc=124
if kill -0 "$WIDE_PID" 2>/dev/null; then
  kill "$WIDE_PID" 2>/dev/null || true
  wait "$WIDE_PID" 2>/dev/null || true
else
  set +e; wait "$WIDE_PID"; rc=$?; set -e
fi
if [ "$rc" -ne 0 ] && [ "$rc" -ne 124 ] && grep -q 'link token' "$SCRATCH/wide.log"; then
  pass "serve --bind 0.0.0.0 with an empty HOME refused (exit $rc): $(grep -m1 'link token' "$SCRATCH/wide.log" | cut -c1-140)"
else
  fail "serve --bind 0.0.0.0 exit $rc: $(tail -3 "$SCRATCH/wide.log")"
fi
[ ! -e "$SCRATCH/empty-home/.swarmery/link-token" ] && pass "the refused serve minted no token" || fail "the refused serve minted a token"

if [ "$failures" -ne 0 ]; then
  echo "live-originless-auth: $failures assertion(s) failed; daemon log:"
  cat "$SCRATCH/daemon.log"
  exit 1
fi
echo "live-originless-auth: all assertions held"
