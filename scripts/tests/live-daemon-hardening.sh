#!/bin/bash
# live-daemon-hardening.sh — live check of the daemon's HTTP hardening.
#
# Builds the swarmery binary and starts it on an ISOLATED instance — port 7795,
# a throwaway database, HOME pointed at a scratch dir (exported by a launcher
# script), an otherwise empty environment; ingest watches only the scratch
# HOME, and runs because /api/ws needs its event bus. A stand-in `serena` on the
# launcher's PATH announces a scratch dashboard backend, so the serena proxy is
# exercised end to end without the real tool. Then it asserts over HTTP:
#
#   - serena proxy: a write and an upgrade carrying a foreign Origin get 403 and
#     never reach the backend; an Origin-less write and any GET still proxy; the
#     relayed document carries the sandbox CSP;
#   - a 2 MiB JSON body gets 413 on two bounded routes;
#   - a connector whose command line embeds a key is listed with the key masked;
#   - connection timeouts: a partial request header is dropped within ~15 s,
#     an idle keep-alive connection is closed after ~120 s, and an idle /api/ws
#     websocket is still open after 90 s.
#
# It never touches a daemon on :7777 or anything under ~/.swarmery. Requires go,
# curl, sqlite3, python3 and claude; no built SPA (only API routes are hit).
# Runtime ≈ 2.5 min (the idle checks).
#
# Deliberately NOT named *.test.sh: it builds a binary and binds ports, so it
# stays out of CI's shell-suite loop. Exit 0 = every assertion held.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
APP="$ROOT/tools/swarmery"
PORT=7795
BACKEND_PORT=7796
BASE="http://127.0.0.1:$PORT"
PROJECT_ID=4242
FOREIGN="http://evil.example"
FAKE_KEY="sk-ant-livecheck-NOTAREALKEY0000"

for tool in go curl sqlite3 python3 claude; do
  command -v "$tool" >/dev/null 2>&1 || { echo "live-daemon-hardening: $tool not found" >&2; exit 2; }
done
for p in "$PORT" "$BACKEND_PORT"; do
  if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:$p/"; then
    echo "live-daemon-hardening: something already listens on :$p — refusing to share it" >&2
    exit 2
  fi
done

SCRATCH="$(mktemp -d)"
PID=""
BACKEND_PID=""
cleanup() {
  for p in "$PID" "$BACKEND_PID"; do
    if [ -n "$p" ] && kill -0 "$p" 2>/dev/null; then
      kill "$p" 2>/dev/null || true
      wait "$p" 2>/dev/null || true
    fi
  done
  # The stand-in serena is the daemon's child and goes with it; this is the
  # belt-and-braces sweep for one orphaned by a crash.
  pkill -f "$SCRATCH/bin/serena" 2>/dev/null || true
  rm -rf "$SCRATCH"
}
trap cleanup EXIT

mkdir -p "$SCRATCH/home" "$SCRATCH/tmp" "$SCRATCH/bin" "$SCRATCH/project" "$SCRATCH/backend/dashboard"
BIN="$SCRATCH/swarmery"
DB="$SCRATCH/s.db"
LOG="$SCRATCH/daemon.log"
CLAUDE_DIR="$(dirname "$(command -v claude)")"

# Stand-in serena: prints the dashboard line toolproc parses, then idles.
cat >"$SCRATCH/bin/serena" <<EOF
#!/bin/bash
echo "Serena web dashboard started at http://127.0.0.1:$BACKEND_PORT/dashboard/index.html"
exec sleep 600
EOF
chmod +x "$SCRATCH/bin/serena"
printf '<!doctype html><title>dash</title><p>serena-stand-in</p>\n' >"$SCRATCH/backend/dashboard/index.html"

# The launcher is the only place the daemon's environment is defined: HOME and
# TMPDIR point into the scratch dir, PATH holds the stand-in serena, claude and
# the system dirs, and the onboarding roots are the scratch dir.
cat >"$SCRATCH/launch.sh" <<EOF
#!/bin/bash
export HOME="$SCRATCH/home"
export TMPDIR="$SCRATCH/tmp"
export PATH="$SCRATCH/bin:$CLAUDE_DIR:/usr/bin:/bin:/usr/sbin:/sbin"
export SWARMERY_ONBOARD_ROOTS="$SCRATCH"
export DISABLE_AUTOUPDATER=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
exec "\$@"
EOF
chmod +x "$SCRATCH/launch.sh"

echo "→ building $BIN"
go -C "$APP" build -o "$BIN" ./cmd/swarmery

# A user-scope connector whose command line embeds a key, in the scratch HOME.
env -i "$SCRATCH/launch.sh" claude mcp add -s user keyed -- echo "--api-key=$FAKE_KEY" >/dev/null

echo "→ starting dashboard backend on :$BACKEND_PORT"
python3 -u -m http.server "$BACKEND_PORT" --bind 127.0.0.1 --directory "$SCRATCH/backend" \
  >"$SCRATCH/backend.log" 2>&1 &
BACKEND_PID=$!

echo "→ starting isolated daemon on :$PORT (db $DB, HOME $SCRATCH/home)"
env -i "$SCRATCH/launch.sh" "$BIN" serve --db "$DB" --port "$PORT" >"$LOG" 2>&1 &
PID=$!

up=0
for _ in $(seq 1 150); do
  if curl -s -o /dev/null --max-time 1 "$BASE/api/health"; then up=1; break; fi
  if ! kill -0 "$PID" 2>/dev/null; then break; fi
  sleep 0.2
done
if [ "$up" -ne 1 ]; then
  echo "live-daemon-hardening: daemon did not come up; log:" >&2
  cat "$LOG" >&2
  exit 1
fi

sqlite3 -cmd ".timeout 5000" "$DB" \
  "INSERT INTO projects (id, path, slug, name, first_seen, last_activity, archived)
   VALUES ($PROJECT_ID, '$SCRATCH/project', 'hardening-check', 'Hardening check',
           '2026-10-22T00:00:00Z', '2026-10-22T00:00:00Z', 0);"

failures=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; failures=$((failures + 1)); }
# expect <label> <want-status> <curl args…> — one request, compared by status.
expect() {
  local label="$1" want="$2"; shift 2
  local got
  got="$(curl -s -o "$SCRATCH/body" -D "$SCRATCH/hdr" -w '%{http_code}' --max-time 10 "$@" || true)"
  if [ "$got" = "$want" ]; then pass "$label ($got)"; else fail "$label (got $got, want $want)"; fi
}
backend_posts() { grep -c '"POST ' "$SCRATCH/backend.log" || true; }

# ── slow checks first, in the background: they only need the daemon up ──
python3 -I - "$PORT" >"$SCRATCH/timeouts.out" 2>&1 <<'PY' &
import base64, os, socket, sys, threading, time
port = int(sys.argv[1])
res = {}

def slow_header():
    s = socket.create_connection(("127.0.0.1", port))
    s.sendall(b"GET /api/health HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nX-Slow: " % port)
    s.settimeout(30)
    t0 = time.time()
    try:
        while s.recv(4096):
            pass
    except socket.timeout:
        res["header"] = "still open after 30s"
        return
    res["header"] = "%.1f" % (time.time() - t0)

def idle_keepalive():
    s = socket.create_connection(("127.0.0.1", port))
    s.sendall(b"GET /api/health HTTP/1.1\r\nHost: 127.0.0.1:%d\r\n\r\n" % port)
    s.settimeout(5)
    s.recv(65536)  # the response; the connection is now idle
    s.settimeout(150)
    t0 = time.time()
    try:
        while s.recv(65536):
            pass
    except socket.timeout:
        res["idle"] = "still open after 150s"
        return
    res["idle"] = "%.1f" % (time.time() - t0)

def idle_ws():
    s = socket.create_connection(("127.0.0.1", port))
    key = base64.b64encode(os.urandom(16)).decode()
    s.sendall(("GET /api/ws HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nConnection: Upgrade\r\n"
               "Upgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n"
               % (port, key)).encode())
    s.settimeout(5)
    head = s.recv(4096)
    if not head.startswith(b"HTTP/1.1 101"):
        res["ws"] = "no upgrade: %r" % head[:80]
        return
    s.settimeout(1)
    t0 = time.time()
    while time.time() - t0 < 90:  # idle: read (and discard) whatever the bus pushes
        try:
            if s.recv(65536) == b"":
                res["ws"] = "closed after %.1fs" % (time.time() - t0)
                return
        except socket.timeout:
            pass
    mask = os.urandom(4)
    s.sendall(bytes([0x89, 0x80]) + mask)  # masked, empty ping
    s.settimeout(5)
    buf = b""
    deadline = time.time() + 5
    while time.time() < deadline:
        try:
            chunk = s.recv(65536)
        except socket.timeout:
            break
        if not chunk:
            res["ws"] = "closed when pinged"
            return
        buf += chunk
        if b"\x8a" in buf:
            res["ws"] = "open"
            return
    res["ws"] = "no pong"

threads = [threading.Thread(target=f) for f in (slow_header, idle_keepalive, idle_ws)]
for t in threads:
    t.start()
for t in threads:
    t.join()
for k in ("header", "idle", "ws"):
    print("%s=%s" % (k, res.get(k, "missing")))
PY
TIMEOUTS_PID=$!

# ── serena proxy ──
expect "serena start" 200 -X POST "$BASE/api/projects/$PROJECT_ID/serena/start"
running=0
for _ in $(seq 1 100); do
  if [ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/projects/$PROJECT_ID/serena/dashboard/index.html")" = 200 ]; then
    running=1; break
  fi
  sleep 0.2
done
if [ "$running" -ne 1 ]; then fail "stand-in serena never became reachable through the proxy"; fi

sp="$BASE/api/projects/$PROJECT_ID/serena"
expect "proxy GET with a foreign Origin still proxies" 200 -H "Origin: $FOREIGN" "$sp/dashboard/index.html"
csp="$(tr -d '\r' <"$SCRATCH/hdr" | grep -i '^content-security-policy:' | sed 's/^[^:]*:[[:space:]]*//' || true)"
if [ "$csp" = "sandbox allow-scripts allow-forms; frame-ancestors 'self'" ]; then
  pass "proxied document is sandboxed ($csp)"
else
  fail "proxied document CSP = '$csp'"
fi
if grep -q serena-stand-in "$SCRATCH/body"; then pass "proxied body is the backend's"; else fail "proxied body is not the backend's"; fi

before="$(backend_posts)"
expect "proxy POST with a foreign Origin" 403 -X POST -H "Origin: $FOREIGN" -d '{}' "$sp/clear_logs"
expect "proxy PUT with a foreign Origin" 403 -X PUT -H "Origin: $FOREIGN" "$sp/shutdown"
expect "proxy upgrade with a foreign Origin" 403 -H "Origin: $FOREIGN" -H "Connection: Upgrade" \
  -H "Upgrade: websocket" -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" "$sp/ws"
after="$(backend_posts)"
if [ "$before" = "$after" ]; then pass "refused writes never reached the backend"; else fail "backend saw $((after - before)) refused POST(s)"; fi
# The stand-in backend (python http.server) answers POST with 501: reaching it
# is the point, not its answer.
expect "proxy POST without an Origin reaches the backend" 501 -X POST -d '{}' "$sp/clear_logs"
expect "proxy POST from the daemon's own origin reaches the backend" 501 -X POST -H "Origin: $BASE" -d '{}' "$sp/clear_logs"

# ── body limits ──
{ printf '{"tags":"'; head -c 2097152 /dev/zero | tr '\0' a; printf '"}'; } >"$SCRATCH/big.json"
expect "2 MiB body on PATCH /api/projects/{id}" 413 -X PATCH -H "Content-Type: application/json" \
  --data-binary "@$SCRATCH/big.json" "$BASE/api/projects/$PROJECT_ID"
expect "2 MiB body on POST /api/sessions/{id}/kill" 413 -X POST -H "Content-Type: application/json" \
  --data-binary "@$SCRATCH/big.json" "$BASE/api/sessions/1/kill"

# ── connector redaction ──
expect "GET /api/connectors" 200 --max-time 30 "$BASE/api/connectors"
if grep -q keyed "$SCRATCH/body"; then pass "the keyed connector is listed"; else fail "the keyed connector is missing: $(head -c 300 "$SCRATCH/body")"; fi
if grep -qF "$FAKE_KEY" "$SCRATCH/body"; then fail "connector detail leaks the key"; else pass "connector detail does not carry the key"; fi
if grep -qF '•••' "$SCRATCH/body"; then pass "connector detail carries the redaction mark"; else fail "no redaction mark in: $(head -c 300 "$SCRATCH/body")"; fi

# ── connection timeouts ──
echo "→ waiting for the idle checks (~2 min)"
wait "$TIMEOUTS_PID" || true
cat "$SCRATCH/timeouts.out"
header="$(sed -n 's/^header=//p' "$SCRATCH/timeouts.out")"
idle="$(sed -n 's/^idle=//p' "$SCRATCH/timeouts.out")"
ws="$(sed -n 's/^ws=//p' "$SCRATCH/timeouts.out")"
in_range() { python3 -I -c 'import sys; v=float(sys.argv[1]); sys.exit(0 if float(sys.argv[2]) <= v <= float(sys.argv[3]) else 1)' "$@" 2>/dev/null; }
if in_range "$header" 8 15; then pass "partial header dropped after ${header}s"; else fail "partial header: $header"; fi
if in_range "$idle" 110 135; then pass "idle keep-alive closed after ${idle}s"; else fail "idle keep-alive: $idle"; fi
if [ "$ws" = open ]; then pass "idle /api/ws still open after 90s"; else fail "idle /api/ws: $ws"; fi

if [ "$failures" -ne 0 ]; then
  echo "live-daemon-hardening: $failures assertion(s) failed; daemon log:"
  cat "$LOG"
  exit 1
fi
echo "live-daemon-hardening: all assertions held"
