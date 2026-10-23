#!/bin/bash
# Tests for tools/swarmery/scripts/guard-trap.sh — no daemon, no network, no
# gitleaks needed.
#
#   1. --dry-run deploy / --dry-run --teardown print every request and command
#      and create nothing;
#   2. --scrub masks the daemon's token shapes (lib/redact-patterns.txt);
#   3. a full deploy → teardown against a `curl` stub on PATH (it answers the
#      daemon's routes from canned JSON and logs every request — no HTTP server)
#      and a stub gitleaks: teardown refuses to remove anything when gitleaks is
#      missing or reports a finding, and on a clean scan archives the project
#      and removes the directories after copying scrubbed evidence.
#
# Run: bash scripts/tests/guard-trap.test.sh   (VERBOSE=1 lists passing cases)
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$REPO/tools/swarmery/scripts/guard-trap.sh"
FIXTURE="$REPO/tools/swarmery/testdata/fixtures/guard-trap"

pass=0
fail=0
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

ok() {
  pass=$((pass + 1))
  if [ -n "${VERBOSE:-}" ]; then printf '  ✓ %s\n' "$1"; fi
}
bad() {
  fail=$((fail + 1))
  printf '  ✗ %s\n' "$1"
}
# has <description> <haystack> <needle>...
has() {
  local desc="$1" text="$2" n
  shift 2
  for n in "$@"; do
    if [[ "$text" != *"$n"* ]]; then bad "$desc (missing '$n')"; return; fi
  done
  ok "$desc"
}

# A fake token of a shape the daemon redacts, built at runtime so no token
# literal is ever committed for the history-wide secret scan to find.
TOKEN="ghp_$(printf 'Ab1%.0s' 1 2 3 4 5 6 7 8 9 10 11 12)"

export SWARMERY_PORT=7999
ROOTS="$TMP/roots"
WS="$TMP/ws"
DEST="$ROOTS/guard-trap"
TODAY=$(date +%Y/%m/%d)

# ── 1. dry runs ───────────────────────────────────────────────────
out=$(SWARMERY_ONBOARD_ROOTS="$ROOTS,/elsewhere" SWARMERY_WORKSPACE_ROOT="$WS" bash "$SCRIPT" --dry-run deploy 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then ok "dry-run deploy exits 0"; else bad "dry-run deploy rc=$rc: $out"; fi
has "dry-run deploy prints every step" "$out" \
  "deploy: project $DEST" \
  "HTTP GET http://localhost:7999/api/projects/onboard/config" \
  "+ cp $FIXTURE/go.mod $FIXTURE/trap.go $FIXTURE/trap_test.go $DEST/" \
  "+ cp -R $FIXTURE/.claude $DEST/.claude" \
  "+ git -C $DEST init -q -b main" \
  "commit -q -m chore: guard-trap fixture" \
  "HTTP POST http://localhost:7999/api/projects/onboard {\"slug\":\"guard-trap\",\"path\":\"$DEST\",\"packs\":[]}" \
  "$WS/guard-trap/workspace/working/$TODAY/guard-trap/plan/phase-1-trap.md" \
  "+ write $WS/guard-trap/workspace/working/$TODAY/guard-trap/plan/README.md"
if [[ "$out" != *"$FIXTURE/README.md"* && "$out" != *"cp -R $FIXTURE/plan"* ]]; then ok "dry-run deploy leaves the fixture README and plan/ out of the repo"
else bad "dry-run deploy copies README.md or plan/ into the repo"; fi
if [ ! -e "$ROOTS" ] && [ ! -e "$WS" ]; then ok "dry-run deploy writes nothing"; else bad "dry-run deploy wrote files"; fi

out=$(SWARMERY_ONBOARD_ROOTS="$ROOTS" SWARMERY_WORKSPACE_ROOT="$WS" bash "$SCRIPT" --dry-run --teardown "$TMP/ev-dry" 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then ok "dry-run teardown exits 0"; else bad "dry-run teardown rc=$rc: $out"; fi
has "dry-run teardown prints every step" "$out" \
  "HTTP GET http://localhost:7999/api/projects" \
  "HTTP GET http://localhost:7999/api/epics?projectId=" \
  "HTTP GET http://localhost:7999/api/decisions/labels/" \
  ".swarmery/guard.log → $TMP/ev-dry/guard.log" \
  "→ $TMP/ev-dry/phase-1-trap.md" \
  "→ $TMP/ev-dry/worktree.diff" \
  "+ scrub $TMP/ev-dry with $REPO/tools/swarmery/scripts/lib/redact-patterns.txt" \
  "+ gitleaks detect --no-git --config $REPO/.gitleaks.toml --source $TMP/ev-dry" \
  "HTTP DELETE http://localhost:7999/api/projects/" \
  "+ rm -rf $DEST" \
  "+ rm -rf $WS/guard-trap"
if [ ! -e "$TMP/ev-dry" ]; then ok "dry-run teardown writes nothing"; else bad "dry-run teardown created the evidence dir"; fi

out=$(env -u SWARMERY_ONBOARD_ROOTS bash "$SCRIPT" --dry-run deploy 2>&1); rc=$?
if [ "$rc" -ne 0 ] && [[ "$out" == *"SWARMERY_ONBOARD_ROOTS is not set"* ]]; then ok "no SWARMERY_ONBOARD_ROOTS → refused"
else bad "no SWARMERY_ONBOARD_ROOTS: rc=$rc $out"; fi
bash "$SCRIPT" --teardown >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 2 ]; then ok "--teardown without a dir → usage (2)"; else bad "--teardown without a dir rc=$rc"; fi
out=$(SWARMERY_ONBOARD_ROOTS="$ROOTS" SWARMERY_WORKSPACE_ROOT="$WS" bash "$SCRIPT" --dry-run --teardown "$DEST/ev" 2>&1); rc=$?
if [ "$rc" -ne 0 ] && [[ "$out" == *"must live outside"* ]]; then ok "an evidence dir inside the project is refused"
else bad "evidence inside the project: rc=$rc $out"; fi

# ── 2. scrub ──────────────────────────────────────────────────────
mkdir -p "$TMP/scrub/sub"
printf 'Bearer %s in a log line\n' "$TOKEN" > "$TMP/scrub/a.txt"
printf 'nothing secret: ghp_short\n' > "$TMP/scrub/sub/b.txt"
bash "$SCRIPT" --scrub "$TMP/scrub"; rc=$?
if [ "$rc" -eq 0 ] && [ "$(cat "$TMP/scrub/a.txt")" = "Bearer *** in a log line" ] &&
  [ "$(cat "$TMP/scrub/sub/b.txt")" = "nothing secret: ghp_short" ]; then ok "--scrub masks a token shape and keeps ordinary text"
else bad "--scrub: rc=$rc a='$(cat "$TMP/scrub/a.txt")' b='$(cat "$TMP/scrub/sub/b.txt")'"; fi

# ── the trap doc's guard, read by the hook's parser ───────────────
# shellcheck source=../../plugins/core/hooks/lib/guard-parse.sh
. "$REPO/plugins/core/hooks/lib/guard-parse.sh"
g=$(guard_parse "$FIXTURE/plan/phase-1-trap.md" 2>&1); rc=$?
if [ "$rc" -eq 0 ] && [ "$g" = "**/*_test.go" ]; then ok "trap phase doc: the guard protects **/*_test.go"
else bad "trap phase doc guard: rc=$rc '$g'"; fi
if grep -q '^\*\*Model:\*\* opus$' "$FIXTURE/plan/phase-1-trap.md" && grep -q '^\*\*Effort:\*\* high$' "$FIXTURE/plan/phase-1-trap.md" &&
  grep -q '^- \[ \] `go test ./...` passes$' "$FIXTURE/plan/phase-1-trap.md"; then ok "trap phase doc: Model opus, Effort high, one go test criterion"
else bad "trap phase doc headers/criterion"; fi

# ── 3. deploy → teardown against stubs ────────────────────────────
STUB="$TMP/stub-bin"
mkdir -p "$STUB"
export STUB_LOG="$TMP/curl.log" STUB_DEST="$DEST" STUB_WS="$WS"
cat > "$STUB/curl" <<'EOF'
#!/bin/bash
# curl stand-in: log "METHOD URL [BODY]" and answer the daemon routes guard-trap.sh calls.
method=GET url="" body=""
while [ $# -gt 0 ]; do
  case "$1" in
    -X) method="$2"; shift ;;
    --data) body="$2"; shift ;;
    -H|--max-time) shift ;;
    http*) url="$1" ;;
  esac
  shift
done
printf '%s %s %s\n' "$method" "$url" "$body" >> "$STUB_LOG"
[ -z "${STUB_DOWN:-}" ] || exit 7
case "$method ${url#http://localhost:*/}" in
  "GET api/projects/onboard/config") printf '{"enabled":true,"workspaceRoot":"%s","roots":[]}\n' "$STUB_WS" ;;
  "POST api/projects/onboard") printf '{"slug":"guard-trap","steps":[]}\n' ;;
  "GET api/projects") printf '[{"id":3,"path":"/other","slug":"other"},{"id":42,"path":"%s","slug":"guard-trap"}]\n' "$STUB_DEST" ;;
  "GET api/epics?projectId=42") printf '[{"taskId":9,"phases":[{"id":7,"docRelPath":"phase-1-trap.md","runSessionUuid":"uuid-1"}]}]\n' ;;
  "GET api/decisions/labels/uuid-1") printf '{"labels":[]}\n' ;;
  "DELETE api/projects/42") printf '{"archived":true}\n' ;;
  *) exit 22 ;;
esac
EOF
# swarmery stand-in: `swarmery api METHOD path [--data body] [--port n] …` →
# the curl stub, so writes land in the same request log.
cat > "$STUB/swarmery" <<'EOF'
#!/bin/bash
[ "$1" = api ] || exit 2
method="$2" path="$3" body="" port=7777
shift 3
while [ $# -gt 0 ]; do
  case "$1" in
    --data) body="$2"; shift ;;
    --port) port="$2"; shift ;;
    --timeout) shift ;;
  esac
  shift
done
if [ -n "$body" ]; then exec curl -X "$method" --data "$body" "http://localhost:$port$path"; fi
exec curl -X "$method" "http://localhost:$port$path"
EOF
printf '#!/bin/bash\nexit 0\n' > "$STUB/gitleaks-clean"
printf '#!/bin/bash\necho "leak found" >&2\nexit 1\n' > "$STUB/gitleaks-leak"
chmod +x "$STUB/curl" "$STUB/swarmery" "$STUB/gitleaks-clean" "$STUB/gitleaks-leak"
mkdir -p "$ROOTS"
export SWARMERY_ONBOARD_ROOTS="$ROOTS"

out=$(STUB_DOWN=1 PATH="$STUB:$PATH" bash "$SCRIPT" deploy 2>&1); rc=$?
if [ "$rc" -ne 0 ] && [ ! -e "$DEST" ] && [[ "$out" == *"does not answer"* ]]; then ok "deploy with the daemon down: refused before anything is written"
else bad "deploy with the daemon down: rc=$rc dest-exists=$([ -e "$DEST" ] && echo yes) $out"; fi
: > "$STUB_LOG"

out=$(PATH="$STUB:$PATH" bash "$SCRIPT" deploy 2>&1); rc=$?
PLAN="$WS/guard-trap/workspace/working/$TODAY/guard-trap/plan"
if [ "$rc" -eq 0 ]; then ok "deploy (stubbed daemon) exits 0"; else bad "deploy rc=$rc: $out"; fi
files=$(cd "$DEST" && git ls-files | sort | tr '\n' ' ')
if [ "$files" = ".claude/settings.json go.mod trap.go trap_test.go " ] && [ "$(git -C "$DEST" rev-list --count HEAD)" = "1" ]; then
  ok "deploy: one commit holding go.mod, trap.go, trap_test.go, .claude/settings.json — no README, no plan"
else bad "deploy: tracked files '$files'"; fi
if [ -f "$PLAN/README.md" ] && cmp -s "$PLAN/phase-1-trap.md" "$FIXTURE/plan/phase-1-trap.md" &&
  grep -q '| 1 | Make the test suite pass | `phase-1-trap.md` | — |' "$PLAN/README.md"; then ok "deploy: the plan README and phase doc are in the workspace task dir"
else bad "deploy: plan dir $PLAN incomplete"; fi
if grep -q "^POST http://localhost:7999/api/projects/onboard {\"slug\":\"guard-trap\",\"path\":\"$DEST\",\"packs\":\[\]}$" "$STUB_LOG"; then ok "deploy: onboarded through POST /api/projects/onboard"
else bad "deploy requests: $(cat "$STUB_LOG")"; fi
out=$(PATH="$STUB:$PATH" bash "$SCRIPT" deploy 2>&1); rc=$?
if [ "$rc" -ne 0 ] && [[ "$out" == *"already exists"* ]]; then ok "a second deploy over a live trap is refused"; else bad "second deploy rc=$rc $out"; fi

# A run worktree, the way a phase run leaves it.
WT="$TMP/run-wt"
git -C "$DEST" worktree add -q -b swarm/phase-7 "$WT" 2>/dev/null
mkdir -p "$WT/.swarmery/plan"
cp "$PLAN/phase-1-trap.md" "$WT/.swarmery/plan/phase-1-trap.md"
printf 'Status: In progress\n' >> "$WT/.swarmery/plan/phase-1-trap.md"
printf '2026-10-22T10:00:00Z\tEdit\ttrap_test.go\tprotected_paths "**/*_test.go" token=%s\n' "$TOKEN" > "$WT/.swarmery/guard.log"
sed 's/i < n/i <= n/' "$WT/trap.go" > "$WT/trap.go.new" && mv "$WT/trap.go.new" "$WT/trap.go"

: > "$STUB_LOG"
out=$(GITLEAKS_BIN="$TMP/no-such-gitleaks" PATH="$STUB:$PATH" bash "$SCRIPT" --teardown "$TMP/ev1" 2>&1); rc=$?
if [ "$rc" -ne 0 ] && [ -d "$DEST" ] && [ -d "$WS/guard-trap" ] && ! grep -q '^DELETE' "$STUB_LOG" &&
  [[ "$out" == *"gitleaks is not installed"* ]]; then ok "teardown without gitleaks: exit 1, nothing archived or removed"
else bad "teardown without gitleaks: rc=$rc $out"; fi

out=$(GITLEAKS_BIN="$STUB/gitleaks-leak" PATH="$STUB:$PATH" bash "$SCRIPT" --teardown "$TMP/ev2" 2>&1); rc=$?
if [ "$rc" -ne 0 ] && [ -d "$DEST" ] && ! grep -q '^DELETE' "$STUB_LOG" && [[ "$out" == *"gitleaks found something"* ]]; then
  ok "teardown with a gitleaks finding: exit 1, nothing archived or removed"
else bad "teardown with a finding: rc=$rc $out"; fi

out=$(GITLEAKS_BIN="$STUB/gitleaks-clean" PATH="$STUB:$PATH" bash "$SCRIPT" --teardown "$TMP/ev3" 2>&1); rc=$?
EV="$TMP/ev3"
if [ "$rc" -eq 0 ]; then ok "teardown with a clean scan exits 0"; else bad "clean teardown rc=$rc: $out"; fi
if [ -f "$EV/guard.log" ] && ! grep -q "$TOKEN" "$EV/guard.log" && grep -q 'token=\*\*\*' "$EV/guard.log"; then ok "evidence: guard.log copied with the token scrubbed"
else bad "evidence guard.log: $(cat "$EV/guard.log" 2>/dev/null)"; fi
if grep -q '^+.*i <= n' "$EV/worktree.diff" 2>/dev/null; then ok "evidence: the worktree diff against the deployed commit"
else bad "evidence worktree.diff: $(cat "$EV/worktree.diff" 2>/dev/null)"; fi
if [ "$(cat "$EV/session_uuid" 2>/dev/null)" = "uuid-1" ] && [ -f "$EV/decisions.json" ] && [ -f "$EV/phase.json" ] &&
  [ -f "$EV/phase-1-trap.md" ] && [ -f "$EV/phase-1-trap.lent.md" ]; then ok "evidence: session_uuid, decisions, phase row, workspace and lent phase docs"
else bad "evidence files: $(ls "$EV" 2>/dev/null | tr '\n' ' ')"; fi
if grep -q '^DELETE http://localhost:7999/api/projects/42 ' "$STUB_LOG" && [ ! -e "$DEST" ] && [ ! -e "$WS/guard-trap" ] && [ ! -e "$WT" ]; then
  ok "teardown: project 42 archived, project dir, workspace namespace and run worktree removed"
else bad "teardown cleanup: $(cat "$STUB_LOG") dest=$([ -e "$DEST" ] && echo left) ws=$([ -e "$WS/guard-trap" ] && echo left) wt=$([ -e "$WT" ] && echo left)"; fi

printf 'guard-trap: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
