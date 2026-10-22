#!/bin/bash
# live-artifact-embed.sh — live check of how a real daemon serves repository
# artifacts (architecture-out/, graphify-out/) and its own shell.
#
# Builds the swarmery binary, starts it on an ISOLATED instance — port 7791, a
# throwaway database, HOME pointed at a scratch dir, an empty environment, no
# ingest — registers a scratch project whose architecture-out/ holds a map plus
# a symlink that leaves the dir, then asserts over HTTP:
#
#   - the artifact is served with `Content-Security-Policy: sandbox allow-scripts; frame-ancestors 'self'`
#     (never allow-same-origin) and `X-Content-Type-Options: nosniff`;
#   - a symlink whose target is outside the artifact dir is refused, and its
#     bytes never appear in the response;
#   - the dashboard shell carries its CSP (object-src 'none', frame-ancestors
#     'self') and an API response carries nosniff + frame-ancestors 'self'.
#
# It never touches a daemon on :7777 or anything under ~/.swarmery. Requires
# go, curl and sqlite3, and a built SPA (tools/swarmery/web/dist — `make
# build-web`), which go:embed bakes into the binary.
#
# Deliberately NOT named *.test.sh: it builds a binary and binds a port, so it
# stays out of CI's shell-suite loop. Exit 0 = every assertion held.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
APP="$ROOT/tools/swarmery"
PORT=7791
BASE="http://127.0.0.1:$PORT"
PROJECT_ID=4242
SECRET="outside-the-artifact-dir-$$"

for tool in go curl sqlite3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "live-artifact-embed: $tool not found" >&2; exit 2; }
done
if [ ! -f "$APP/web/dist/index.html" ]; then
  echo "live-artifact-embed: $APP/web/dist is not built — run make -C $APP build-web first" >&2
  exit 2
fi
if curl -s -o /dev/null --max-time 2 "$BASE/api/health"; then
  echo "live-artifact-embed: something already listens on :$PORT — refusing to share it" >&2
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

mkdir -p "$SCRATCH/home" "$SCRATCH/tmp" "$SCRATCH/outside" "$SCRATCH/project/architecture-out"
BIN="$SCRATCH/swarmery"
DB="$SCRATCH/s.db"
LOG="$SCRATCH/daemon.log"

echo "→ building $BIN"
go -C "$APP" build -o "$BIN" ./cmd/swarmery

echo "→ starting isolated daemon on :$PORT (db $DB, HOME $SCRATCH/home)"
env -i PATH="$PATH" HOME="$SCRATCH/home" TMPDIR="$SCRATCH/tmp" \
  "$BIN" serve --db "$DB" --port "$PORT" --no-ingest >"$LOG" 2>&1 &
PID=$!

up=0
for _ in $(seq 1 150); do
  if curl -s -o /dev/null --max-time 1 "$BASE/api/health"; then up=1; break; fi
  if ! kill -0 "$PID" 2>/dev/null; then break; fi
  sleep 0.2
done
if [ "$up" -ne 1 ]; then
  echo "live-artifact-embed: daemon did not come up; log:" >&2
  cat "$LOG" >&2
  exit 1
fi

# Scratch project: a map, an in-jail alias, and a symlink that leaves the dir.
OUT="$SCRATCH/project/architecture-out"
printf '<!doctype html><title>map</title><p>embed-check-map</p>\n' >"$OUT/architecture-map.html"
printf '%s\n' "$SECRET" >"$SCRATCH/outside/secret.txt"
ln -s "$SCRATCH/outside/secret.txt" "$OUT/leak.html"
ln -s "architecture-map.html" "$OUT/alias.html"
sqlite3 -cmd ".timeout 5000" "$DB" \
  "INSERT INTO projects (id, path, slug, name, first_seen, last_activity, archived)
   VALUES ($PROJECT_ID, '$SCRATCH/project', 'embed-check', 'Embed check',
           '2026-10-22T00:00:00Z', '2026-10-22T00:00:00Z', 0);"

failures=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; failures=$((failures + 1)); }
# check <label> <command...> — PASS when the command succeeds, FAIL otherwise.
check() {
  local label="$1"; shift
  if "$@"; then pass "$label"; else fail "$label"; fi
}
body_has() { printf '%s' "$BODY" | grep -qF -- "$1"; }

# fetch <path> — sets STATUS, HEADERS (CR-stripped, lowercased names) and BODY.
fetch() {
  local hdr="$SCRATCH/h" body="$SCRATCH/b"
  STATUS="$(curl -s -D "$hdr" -o "$body" -w '%{http_code}' "$BASE$1")"
  HEADERS="$(tr -d '\r' <"$hdr")"
  BODY="$(cat "$body")"
}
header() { printf '%s\n' "$HEADERS" | grep -i "^$1:" | sed 's/^[^:]*:[[:space:]]*//' || true; }

art="/api/projects/$PROJECT_ID/architecture"

fetch "$art/architecture-map.html"
csp="$(header content-security-policy)"
echo "  GET $art/architecture-map.html → $STATUS; CSP: $csp; nosniff: $(header x-content-type-options)"
check "map served (status $STATUS)" [ "$STATUS" = 200 ]
check "map body is the map" body_has embed-check-map
check "map CSP is exactly 'sandbox allow-scripts; frame-ancestors 'self'' (got '$csp')" [ "$csp" = "sandbox allow-scripts; frame-ancestors 'self'" ]
case "$csp" in *allow-same-origin*) fail "map CSP grants allow-same-origin" ;; *) pass "no allow-same-origin" ;; esac
check "map nosniff" [ "$(header x-content-type-options)" = nosniff ]

fetch "$art/leak.html"
echo "  GET $art/leak.html → $STATUS"
check "escaping symlink refused (status $STATUS)" [ "$STATUS" != 200 ]
case "$BODY" in *"$SECRET"*) fail "escaping symlink leaked outside bytes" ;; *) pass "no outside bytes in the response" ;; esac

fetch "$art/alias.html"
echo "  GET $art/alias.html → $STATUS"
check "in-jail symlink still served (status $STATUS)" [ "$STATUS" = 200 ]
check "in-jail symlink body is the map" body_has embed-check-map

fetch "/"
csp="$(header content-security-policy)"
echo "  GET / → $STATUS; CSP: $csp"
case "$csp" in *"object-src 'none'"*"frame-ancestors 'self'"*) pass "shell CSP has object-src 'none' and frame-ancestors 'self'" ;; *) fail "shell CSP = '$csp'" ;; esac
check "shell nosniff" [ "$(header x-content-type-options)" = nosniff ]

fetch "/api/projects"
csp="$(header content-security-policy)"
echo "  GET /api/projects → $STATUS; CSP: $csp; nosniff: $(header x-content-type-options)"
check "API CSP is the frame-ancestors baseline (got '$csp')" [ "$csp" = "frame-ancestors 'self'" ]
check "API nosniff" [ "$(header x-content-type-options)" = nosniff ]

if [ "$failures" -ne 0 ]; then
  echo "live-artifact-embed: $failures assertion(s) failed; daemon log:"
  cat "$LOG"
  exit 1
fi
echo "live-artifact-embed: all assertions held"
