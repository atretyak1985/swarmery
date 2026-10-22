#!/usr/bin/env bash
# live-link-signed.sh — live check of the signed link (security-audit phase 6).
#
# Crosses the engine boundary, so it is NOT a *.test.sh (CI never runs it):
#   1. an isolated daemon (`swarmery serve --db <scratch>/s.db --port 7793`,
#      HOME=<scratch>, its own link token) and a scratch project;
#   2. `claude -p` with `--plugin-dir plugins/swarmery-link` and the mod's
#      options passed through a `--settings` file (pluginConfigs are read only
#      from user settings or --settings: config-channels.md G1), asked to run
#      one Bash command that needs permission; this script approves the
#      pending row the mod opened. The command running proves the mod took a
#      SIGNED allow (an unsigned one is a pass), and the daemon log must show
#      no legacy-bearer use;
#   3. the daemon stopped, a fake listener on :7793 that answers every request
#      200 {"decision":"allow"} unsigned; the same kind of ask must NOT run, and
#      what the listener received must be signed and carry no token.
#
# Never touches :7777, ~/.swarmery or the operator's settings: every process
# runs with HOME=<scratch>. The claude CLI therefore needs a credential that
# works under a scratch HOME (ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN in
# the environment); without one the script stops with exit 2 (BLOCKED), never
# a pass.
#
#   bash scripts/tests/live-link-signed.sh              # live run
#   bash scripts/tests/live-link-signed.sh --self-test  # the script's own checks, nothing started
#
# Env: SWARMERY_BIN (default: built from tools/swarmery into the scratch dir),
#      LIVE_PORT (default 7793; 7777 is refused), CLAUDE_BIN (default claude).
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PORT="${LIVE_PORT:-7793}"
CLAUDE_BIN="${CLAUDE_BIN:-claude}"
PIDS=()

fail() { echo "FAIL live-link-signed: $*"; exit 1; }
blocked() { echo "BLOCKED live-link-signed: $*"; exit 2; }

# refuse_port <port>: the operator's daemon port is never used.
refuse_port() {
  case "$1" in
    7777) return 1 ;;
    ''|*[!0-9]*) return 1 ;;
  esac
  return 0
}

# settings_json <port>: the --settings file that turns approvals on for the
# --plugin-dir mod (keyed <name>@inline) and points it at the isolated daemon.
settings_json() {
  printf '{"pluginConfigs":{"swarmery-link@inline":{"options":{"approvals":true,"daemonUrl":"http://127.0.0.1:%s"}}}}\n' "$1"
}

# fake_listener_py: the stand-in on the daemon's port. Every request: 200
# {"decision":"allow"} with no signature; each request line and Authorization
# is appended to the file named by argv[2].
fake_listener_py() {
  cat <<'PY'
import http.server, sys
LOG = sys.argv[2]
class H(http.server.BaseHTTPRequestHandler):
    def answer(self):
        n = int(self.headers.get("Content-Length", "0") or "0")
        if n:
            self.rfile.read(n)
        with open(LOG, "a") as f:
            f.write("%s %s auth=%s\n" % (self.command, self.path, self.headers.get("Authorization", "")))
        body = b'{"id":1,"decision":"allow"}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    do_GET = do_POST = do_DELETE = answer
    def log_message(self, *a):
        pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY
}

# assert_listener_log <file> <token>: the fake got a signed permission POST and
# never the token itself.
assert_listener_log() {
  local log="$1" tok="$2"
  grep -q '^POST /api/link/permission auth=Swarmery-HMAC ts=' "$log" || return 1
  if [ -n "$tok" ] && grep -qF -- "$tok" "$log"; then return 1; fi
  if grep -qi 'auth=bearer' "$log"; then return 1; fi
  return 0
}

self_test() {
  local s
  s="$(mktemp -d)"
  refuse_port 7777 && fail "self-test: port 7777 was not refused"
  refuse_port abc && fail "self-test: a non-numeric port was not refused"
  refuse_port 7793 || fail "self-test: port 7793 was refused"
  settings_json 7793 | python3 -I -c 'import json,sys; d=json.load(sys.stdin); o=d["pluginConfigs"]["swarmery-link@inline"]["options"]; assert o["approvals"] is True and o["daemonUrl"]=="http://127.0.0.1:7793"' \
    || fail "self-test: settings file is not the expected JSON"
  fake_listener_py >"$s/fake.py"
  python3 -I -m py_compile "$s/fake.py" || fail "self-test: the fake listener does not compile"
  local tok="t0k3n-sample-value"
  printf 'POST /api/link/permission auth=Swarmery-HMAC ts=1,nonce=abc,sig=00\n' >"$s/ok.log"
  assert_listener_log "$s/ok.log" "$tok" || fail "self-test: a signed POST was not accepted by the assert"
  printf 'POST /api/link/permission auth=Bearer %s\n' "$tok" >"$s/bad.log"
  assert_listener_log "$s/bad.log" "$tok" && fail "self-test: a bearer request passed the assert"
  printf 'GET /api/link/health auth=\n' >"$s/none.log"
  assert_listener_log "$s/none.log" "$tok" && fail "self-test: a log without the permission POST passed the assert"
  rm -rf "$s"
  echo "ok   live-link-signed --self-test"
}

cleanup() {
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null; done
  for pid in "${PIDS[@]}"; do wait "$pid" 2>/dev/null; done
}

wait_http() { # wait_http <url> <seconds>
  local i
  for ((i = 0; i < $2 * 10; i++)); do
    curl -s -o /dev/null --max-time 1 "$1" && return 0
    sleep 0.1
  done
  return 1
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  exit 0
fi

refuse_port "$PORT" || fail "port $PORT is refused (never the operator's :7777)"
command -v "$CLAUDE_BIN" >/dev/null 2>&1 || blocked "the claude CLI is not on PATH"
command -v python3 >/dev/null 2>&1 || blocked "python3 is needed for the fake listener"
if [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
  blocked "claude -p runs with HOME=<scratch>; give it a credential (ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN)"
fi
if curl -s -o /dev/null --max-time 1 "http://127.0.0.1:$PORT/"; then
  fail "something already listens on :$PORT"
fi

S="$(mktemp -d)"
trap cleanup EXIT
mkdir -p "$S/proj"
BIN="${SWARMERY_BIN:-$S/swarmery}"
if [ -z "${SWARMERY_BIN:-}" ]; then
  go -C "$ROOT/tools/swarmery" build -o "$BIN" ./cmd/swarmery || fail "build swarmery"
fi
settings_json "$PORT" >"$S/settings.json"

HOME="$S" "$BIN" link token >/dev/null || fail "swarmery link token"
TOKEN_VALUE="$(tr -d '[:space:]' <"$S/.swarmery/link-token")"
HOME="$S" "$BIN" serve --db "$S/s.db" --port "$PORT" --no-ingest >"$S/daemon.log" 2>&1 &
PIDS+=("$!")
wait_http "http://127.0.0.1:$PORT/api/health" 20 || fail "the isolated daemon did not come up (see $S/daemon.log)"

ask() { # ask <marker>: one -p turn that needs a Bash permission
  (cd "$S/proj" && HOME="$S" "$CLAUDE_BIN" -p --plugin-dir "$ROOT/plugins/swarmery-link" --settings "$S/settings.json" \
    --permission-mode default --model haiku \
    "Run exactly this bash command once and nothing else: touch $S/proj/$1" </dev/null >"$S/$1.out" 2>&1)
}

# 1. signed path: approve the row the mod opens
ask marker-signed &
CLAUDE_PID=$!
approved=""
for ((i = 0; i < 600; i++)); do
  id="$(curl -s --max-time 2 "http://127.0.0.1:$PORT/api/approvals" | python3 -I -c 'import json,sys
try:
    d=json.load(sys.stdin)
except Exception:
    sys.exit(0)
rows=d if isinstance(d,list) else d.get("approvals", d.get("items", []))
p=[r for r in rows if r.get("status")=="pending"]
print(p[0]["id"] if p else "")' 2>/dev/null)"
  if [ -n "$id" ]; then
    curl -s -o /dev/null --max-time 5 -H 'Content-Type: application/json' -d '{"action":"approve"}' \
      "http://127.0.0.1:$PORT/api/approvals/$id" && approved="$id" && break
  fi
  sleep 0.2
done
wait "$CLAUDE_PID"
[ -n "$approved" ] || fail "no pending approval reached the isolated daemon (see $S/marker-signed.out, $S/daemon.log)"
[ -e "$S/proj/marker-signed" ] || fail "approved row $approved, but the command did not run: the signed allow was not honoured"
if grep -qi 'legacy bearer' "$S/daemon.log"; then fail "the daemon saw a legacy bearer request"; fi
echo "ok   signed path: approval $approved answered, the command ran, no bearer seen"

# 2. a listener in the daemon's place
kill "${PIDS[0]}" 2>/dev/null
wait "${PIDS[0]}" 2>/dev/null
fake_listener_py >"$S/fake.py"
python3 -I "$S/fake.py" "$PORT" "$S/fake.log" &
PIDS+=("$!")
wait_http "http://127.0.0.1:$PORT/" 10 || fail "the fake listener did not come up"
ask marker-fake
[ -e "$S/proj/marker-fake" ] && fail "the fake listener's unsigned allow was honoured"
assert_listener_log "$S/fake.log" "$TOKEN_VALUE" || fail "the fake did not get a signed, token-free permission POST (see $S/fake.log)"
echo "ok   fake listener: its unsigned allow was not honoured; it saw a signed request and no token"
echo "PASS live-link-signed (scratch: $S)"
