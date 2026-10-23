#!/bin/bash
# live-exposed-listener.sh — live check: an exposed daemon accepts only signed
# requests.
#
# Builds the swarmery binary, mints a link token in a scratch HOME, and starts
# the daemon ISOLATED on a wildcard bind — `serve --bind 0.0.0.0 --port 7797`,
# a throwaway database, HOME pointed at the scratch dir by a launcher script,
# --no-ingest — then asserts over HTTP:
#   - GET /api/health answers 200 unsigned (it carries no data);
#   - the SPA shell (GET /) is not behind the auth gate;
#   - unsigned GET /api/sessions and GET /api/approvals get 401;
#   - a Bearer request with the REAL link token gets 401 (a POST and a GET),
#     with the Bearer-refusal body, and the POST creates nothing;
#   - `swarmery api GET /api/sessions --port 7797` (signed) exits 0, and a
#     signed write through `swarmery api` creates one routine.
# It never touches a daemon on :7777 or anything under ~/.swarmery. Requires go,
# curl. Deliberately NOT named *.test.sh: it builds a binary and binds a port.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PORT=7797
BASE="http://127.0.0.1:$PORT"
NAME=live-exposed-listener

for tool in go curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$NAME: $tool not found" >&2; exit 2; }
done
if curl -s -o /dev/null --max-time 2 "$BASE/"; then
  echo "$NAME: something already listens on :$PORT — refusing to share it" >&2
  exit 2
fi

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

mkdir -p "$SCRATCH/home" "$SCRATCH/tmp"
BIN="$SCRATCH/swarmery"
cat >"$SCRATCH/launch.sh" <<EOF
#!/bin/bash
export HOME="\$1"; shift
export TMPDIR="$SCRATCH/tmp"
export PATH="/usr/bin:/bin:/usr/sbin:/sbin"
exec "\$@"
EOF
chmod +x "$SCRATCH/launch.sh"
run() { env -i "$SCRATCH/launch.sh" "$SCRATCH/home" "$BIN" "$@"; }

echo "→ building $BIN"
go -C "$ROOT/tools/swarmery" build -o "$BIN" ./cmd/swarmery

echo "→ minting a link token in $SCRATCH/home"
run link token >/dev/null
TOKEN_FILE="$SCRATCH/home/.swarmery/link-token"
[ -s "$TOKEN_FILE" ] || { echo "$NAME: no link token minted" >&2; exit 1; }

echo "→ starting isolated daemon: serve --bind 0.0.0.0 --port $PORT"
# Not through run(): a backgrounded shell function is a subshell, and its PID
# would not be the daemon's — cleanup would kill the subshell and leak serve.
env -i "$SCRATCH/launch.sh" "$SCRATCH/home" "$BIN" serve --bind 0.0.0.0 --db "$SCRATCH/s.db" --port "$PORT" --no-ingest \
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
status() { curl -s -o "$SCRATCH/body" -w '%{http_code}' "$@"; }
ROUTINE='{"name":"live-exposed","steps":[{"type":"command","name":"c","command":"true"}]}'
BEARER_REFUSAL='{"error":"the Bearer scheme is refused on an exposed listener; sign the request (swarmery api)"}'
count_routines() {
  run api GET /api/routines --port "$PORT" 2>/dev/null | grep -o '"name":"live-exposed"' | wc -l | tr -d ' '
}

got="$(status "$BASE/api/health")"
[ "$got" = 200 ] && pass "unsigned GET /api/health ($got)" || fail "GET /api/health = $got, want 200"

got="$(status "$BASE/")"
if [ "$got" != 401 ] && ! grep -q 'link auth\|link token required' "$SCRATCH/body"; then
  pass "GET / is served by the SPA handler, not the auth gate ($got)"
else
  fail "GET / = $got $(head -c 160 "$SCRATCH/body")"
fi

for path in /api/sessions /api/approvals; do
  got="$(status "$BASE$path")"
  [ "$got" = 401 ] && pass "unsigned GET $path ($got): $(head -c 100 "$SCRATCH/body")…" || fail "unsigned GET $path = $got, want 401"
done

AUTH="Authorization: Bearer $(cat "$TOKEN_FILE")"
got="$(status -X POST -H "$AUTH" -H 'Content-Type: application/json' -d "$ROUTINE" "$BASE/api/routines")"
if [ "$got" = 401 ] && [ "$(cat "$SCRATCH/body")" = "$BEARER_REFUSAL" ]; then
  pass "Bearer POST with the real token refused ($got): $(cat "$SCRATCH/body")"
else
  fail "Bearer POST = $got $(cat "$SCRATCH/body"), want 401 $BEARER_REFUSAL"
fi
got="$(status -H "$AUTH" "$BASE/api/sessions")"
[ "$got" = 401 ] && pass "Bearer GET /api/sessions refused ($got)" || fail "Bearer GET /api/sessions = $got, want 401"
[ "$(count_routines)" = 0 ] && pass "the refused Bearer POST created nothing" || fail "the refused Bearer POST created a routine"

if run api GET /api/sessions --port "$PORT" >"$SCRATCH/api.out" 2>"$SCRATCH/api.err"; then
  pass "swarmery api GET /api/sessions --port $PORT exit 0: $(head -c 60 "$SCRATCH/api.out")…"
else
  fail "swarmery api GET /api/sessions exit $?: $(cat "$SCRATCH/api.err")"
fi
if run api POST /api/routines --port "$PORT" --data "$ROUTINE" >"$SCRATCH/api.out" 2>"$SCRATCH/api.err"; then
  pass "swarmery api POST /api/routines (signed) exit 0"
else
  fail "swarmery api POST failed: $(cat "$SCRATCH/api.err")"
fi
[ "$(count_routines)" = 1 ] && pass "the signed POST created one routine" || fail "routines = $(count_routines), want 1"

if [ "$failures" -ne 0 ]; then
  echo "$NAME: $failures assertion(s) failed; daemon log:"
  cat "$SCRATCH/daemon.log"
  exit 1
fi
echo "$NAME: all assertions held"
