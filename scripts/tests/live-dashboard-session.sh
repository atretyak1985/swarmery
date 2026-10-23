#!/bin/bash
# live-dashboard-session.sh — live check that the dashboard authenticates its
# writes with a session token.
#
# Builds the swarmery binary and starts it on an ISOLATED instance — port 7798,
# a throwaway database, HOME pointed at a scratch dir with its own link token,
# an otherwise empty environment. Then it asserts over HTTP:
#
#   (i)   a write carrying the daemon's own Origin and no session is 401
#         dashboard_locked;
#   (ii)  a terminal upgrade with that Origin and no session is not 101;
#   (iii) `swarmery open --print` hands out a ticket that POST
#         /api/session/unlock redeems for a session;
#   (iv)  the same write with X-Swarmery-Session passes the gate, and an
#         /api/ws upgrade offering the session selects exactly swarmery.v1;
#   (v)   the ticket does not unlock twice;
#   (vi)  a ticket is bound to the origin it was opened at (127.0.0.1): it is
#         refused from the localhost name and another loopback address;
#   (vii) `swarmery open` against a port no daemon serves mints no ticket.
#
# It never touches a daemon on :7777 or anything under ~/.swarmery. Requires go
# and curl; no built SPA (only API routes are hit). Deliberately NOT named
# *.test.sh: it builds a binary and binds a port, so it stays out of CI's
# shell-suite loop. Exit 0 = every assertion held.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
APP="$ROOT/tools/swarmery"
PORT="${SWARMERY_LIVE_PORT:-7798}"
if [ "$PORT" = "7777" ]; then
  echo "live-dashboard-session: refusing port 7777 (the operator's daemon)" >&2
  exit 2
fi
BASE="http://127.0.0.1:$PORT"
ORIGIN="http://127.0.0.1:$PORT"

for tool in go curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "live-dashboard-session: $tool not found" >&2; exit 2; }
done
if curl -s -o /dev/null --max-time 2 "$BASE/"; then
  echo "live-dashboard-session: something already listens on :$PORT — refusing to share it" >&2
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
DB="$SCRATCH/s.db"
LOG="$SCRATCH/daemon.log"

# The launcher is the only place the daemon's and the CLI's environment is
# defined: HOME and TMPDIR point into the scratch dir, PATH holds only the
# system dirs.
cat >"$SCRATCH/launch.sh" <<EOF
#!/bin/bash
export HOME="$SCRATCH/home"
export TMPDIR="$SCRATCH/tmp"
export PATH="/usr/bin:/bin:/usr/sbin:/sbin"
exec "\$@"
EOF
chmod +x "$SCRATCH/launch.sh"
run() { env -i "$SCRATCH/launch.sh" "$@"; }

echo "→ building $BIN"
go -C "$APP" build -o "$BIN" ./cmd/swarmery

echo "→ minting a link token in the scratch HOME"
run "$BIN" link token >/dev/null
case "$(run "$BIN" link token --print-path)" in
  "$SCRATCH/home/"*) ;;
  *) echo "live-dashboard-session: the link token is not under the scratch HOME" >&2; exit 1 ;;
esac

echo "→ starting isolated daemon on :$PORT (db $DB, HOME $SCRATCH/home)"
run "$BIN" serve --db "$DB" --port "$PORT" >"$LOG" 2>&1 &
PID=$!

up=0
for _ in $(seq 1 150); do
  if curl -s -o /dev/null --max-time 1 "$BASE/api/health"; then up=1; break; fi
  if ! kill -0 "$PID" 2>/dev/null; then break; fi
  sleep 0.2
done
if [ "$up" -ne 1 ]; then
  echo "live-dashboard-session: daemon did not come up; log:" >&2
  cat "$LOG" >&2
  exit 1
fi

failures=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; failures=$((failures + 1)); }
# status <curl args…> — one request; prints the status, keeps the body.
status() {
  curl -s -o "$SCRATCH/body" -D "$SCRATCH/hdr" -w '%{http_code}' --max-time 10 "$@" || true
}
ROUTINE='{"name":"live-session-check","steps":[{"type":"command","name":"c","command":"true"}]}'
WS_HEADERS=(-H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13'
  -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==')

# (i) own Origin, no session → 401 dashboard_locked.
got="$(status -X POST -H "Origin: $ORIGIN" -H 'Content-Type: application/json' -d "$ROUTINE" "$BASE/api/routines")"
if [ "$got" = "401" ] && grep -q '"code":"dashboard_locked"' "$SCRATCH/body"; then
  pass "(i) own-Origin write without a session is 401 dashboard_locked"
else
  fail "(i) own-Origin write without a session: got $got $(cat "$SCRATCH/body")"
fi

# (ii) own Origin terminal upgrade, no session → not 101.
got="$(status "${WS_HEADERS[@]}" -H "Origin: $ORIGIN" "$BASE/api/term/ws?cwd=$SCRATCH")"
if [ "$got" != "101" ] && [ "$got" != "000" ]; then
  pass "(ii) own-Origin terminal upgrade without a session refused ($got)"
else
  fail "(ii) own-Origin terminal upgrade without a session: got $got"
fi

# (iii) swarmery open --print → ticket → unlock → session.
URL="$(run "$BIN" open --print --port "$PORT")"
case "$URL" in
  "http://127.0.0.1:$PORT/#swarmery-unlock="*) pass "(iii) swarmery open --print printed the unlock URL (the bound literal, not localhost)" ;;
  *) fail "(iii) unexpected URL shape: $URL" ;;
esac
TICKET="${URL#*#swarmery-unlock=}"
got="$(status -X POST -H "Origin: $ORIGIN" -H 'Content-Type: application/json' \
  -d "{\"ticket\":\"$TICKET\"}" "$BASE/api/session/unlock")"
SESSION="$(sed -n 's/.*"session":"\([^"]*\)".*/\1/p' "$SCRATCH/body")"
if [ "$got" = "200" ] && [ -n "$SESSION" ]; then
  pass "(iii) unlock redeemed the ticket for a session"
else
  fail "(iii) unlock: got $got $(cat "$SCRATCH/body")"
fi

# (iv) the write with the session passes the gate; /api/ws selects swarmery.v1.
got="$(status -X POST -H "Origin: $ORIGIN" -H "X-Swarmery-Session: $SESSION" \
  -H 'Content-Type: application/json' -d "$ROUTINE" "$BASE/api/routines")"
if [ "$got" != "401" ] && [ "$got" != "403" ] && [ "$got" != "000" ]; then
  pass "(iv) own-Origin write with the session passed the gate ($got)"
else
  fail "(iv) own-Origin write with the session: got $got $(cat "$SCRATCH/body")"
fi
# A 101 keeps the socket open, so curl ends on its timeout; the headers are
# what is read.
curl -s -o /dev/null -D "$SCRATCH/wshdr" --max-time 3 "${WS_HEADERS[@]}" -H "Origin: $ORIGIN" \
  -H "Sec-WebSocket-Protocol: swarmery.v1, swarmery.session.$SESSION" "$BASE/api/ws" || true
ws_status="$(head -n1 "$SCRATCH/wshdr" | tr -d '\r')"
ws_proto="$(tr -d '\r' <"$SCRATCH/wshdr" | sed -n 's/^[Ss]ec-[Ww]eb[Ss]ocket-[Pp]rotocol: *//p')"
if [[ "$ws_status" == *" 101"* ]] && [ "$ws_proto" = "swarmery.v1" ]; then
  pass "(iv) /api/ws upgrade with the session is 101 and selects exactly swarmery.v1"
else
  fail "(iv) /api/ws upgrade with the session: status '$ws_status', protocol '$ws_proto'"
fi

# (v) replaying the ticket → 401.
got="$(status -X POST -H "Origin: $ORIGIN" -H 'Content-Type: application/json' \
  -d "{\"ticket\":\"$TICKET\"}" "$BASE/api/session/unlock")"
if [ "$got" = "401" ] && grep -q '"code":"unlock_invalid"' "$SCRATCH/body"; then
  pass "(v) a replayed ticket is 401 unlock_invalid"
else
  fail "(v) replayed ticket: got $got $(cat "$SCRATCH/body")"
fi

# (vi) the ticket is bound to the origin it was opened at: a fresh one is
# refused from the localhost name (403) and from another loopback address, and
# still redeems at the bound literal afterwards.
URL2="$(run "$BIN" open --print --port "$PORT")"
TICKET2="${URL2#*#swarmery-unlock=}"
got_name="$(status -X POST -H "Origin: http://localhost:$PORT" -H 'Content-Type: application/json' \
  -d "{\"ticket\":\"$TICKET2\"}" "$BASE/api/session/unlock")"
got_sib="$(status -X POST -H "Origin: http://127.0.0.2:$PORT" -H 'Content-Type: application/json' \
  -d "{\"ticket\":\"$TICKET2\"}" "$BASE/api/session/unlock")"
got_own="$(status -X POST -H "Origin: $ORIGIN" -H 'Content-Type: application/json' \
  -d "{\"ticket\":\"$TICKET2\"}" "$BASE/api/session/unlock")"
if [ "$got_name" = "403" ] && [ "$got_sib" = "403" ] && [ "$got_own" = "200" ]; then
  pass "(vi) unlock refused from localhost ($got_name) and 127.0.0.2 ($got_sib), redeemed at $ORIGIN ($got_own)"
else
  fail "(vi) origin binding: localhost $got_name, 127.0.0.2 $got_sib, own $got_own"
fi

# (vii) no daemon, no ticket: `swarmery open` first makes the listener prove it
# holds the link token, so against a port nothing serves it exits 2, mints
# nothing and prints nothing.
DEAD_PORT=$((PORT + 1))
if curl -s -o /dev/null --max-time 1 "http://127.0.0.1:$DEAD_PORT/"; then
  echo "SKIP (vii) something listens on :$DEAD_PORT"
else
  set +e
  dead_out="$(run "$BIN" open --print --port "$DEAD_PORT" 2>"$SCRATCH/dead.err")"
  dead_code=$?
  set -e
  if [ "$dead_code" = "2" ] && [ -z "$dead_out" ] && grep -q 'no unlock ticket was minted' "$SCRATCH/dead.err"; then
    pass "(vii) swarmery open against a dead port exits 2 and mints no ticket"
  else
    fail "(vii) dead port: exit $dead_code, stdout '$dead_out', stderr $(cat "$SCRATCH/dead.err")"
  fi
fi

if [ "$failures" -ne 0 ]; then
  echo "live-dashboard-session: $failures assertion(s) failed; daemon log tail:" >&2
  tail -n 30 "$LOG" >&2
  exit 1
fi
echo "live-dashboard-session: all assertions held"
