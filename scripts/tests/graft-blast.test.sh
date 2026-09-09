#!/bin/bash
# Behavioral tests for plugins/graft-pack/hooks/graft-blast.sh.
#
# Framework-free and hermetic: `graft` is an inert stub on a synthetic PATH (the
# hook requires the CLI to exist but never calls it — it reads the wiring graph
# directly), and each case writes its own graft/.graph/wiring.json. The fixtures
# use the real 0.16.0 node/edge shape, captured by building an index and reading
# it back: node ids are `<path>#<name>`, and relations are contains/imports/calls.
#
# Each case asserts: (a) exit 0 — always, (b) stdout is valid JSON or empty,
# (c) the dependent list and its cap. Run: bash scripts/tests/graft-blast.test.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOOK="$ROOT/plugins/graft-pack/hooks/graft-blast.sh"

pass=0
fail=0
fail_case() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }
ok_case() { pass=$((pass + 1)); }

valid_json() { printf '%s' "$1" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{JSON.parse(s)})' 2>/dev/null; }

ctx() {
  printf '%s' "$1" | node -e '
    let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{
      try { process.stdout.write(JSON.parse(s).hookSpecificOutput.additionalContext || ""); }
      catch (e) { process.stdout.write(""); }
    })' 2>/dev/null
}

TMP=$(mktemp -d)
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

BIN="$TMP/bin"
mkdir -p "$BIN"
printf '#!/bin/bash\nexit 0\n' > "$BIN/graft"   # presence check only; never invoked
chmod +x "$BIN/graft"

STUBPATH="$BIN:/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin"

# NOGRAFT is the same toolchain WITHOUT graft: jq and node are symlinked in by
# absolute path and nothing else is on PATH. Simply stripping PATH would make the
# no-CLI case pass for the wrong reason — the hook would bail on a missing jq
# long before it ever looked for graft, and the assertion could never tell.
NOGRAFT="$TMP/nograft"
mkdir -p "$NOGRAFT"
# cat is in the list because the hook drains stdin with it BEFORE any early
# exit; leaving it out would make the hook skip the drain it is being tested for.
for tool in jq node cat; do
  bin=$(command -v "$tool") || { echo "graft-blast.test: $tool is required" >&2; exit 1; }
  ln -sf "$bin" "$NOGRAFT/$tool"
done
if PATH="$NOGRAFT" command -v graft >/dev/null 2>&1; then
  echo "graft-blast.test: graft leaked into the no-CLI PATH" >&2
  exit 1
fi

REPO="$TMP/repo"
mkdir -p "$REPO/graft/.graph" "$REPO/.claude"
WIRING="$REPO/graft/.graph/wiring.json"

# write_wiring <n-dependents> — a.ts with one exported symbol, plus N files that
# import it and call into it. Mirrors the real shape exactly.
write_wiring() {
  N="$1" node -e '
    const n = Number.parseInt(process.env.N, 10);
    const nodes = [
      { id: "src/a.ts", name: "a.ts", kind: "file", path: "src/a.ts", span: "L1-L3", exported: true, origin: "ast" },
      { id: "src/a.ts#alpha", name: "alpha", kind: "function", path: "src/a.ts", span: "L1-L1",
        signature: "function alpha(): number", exported: true, origin: "ast" },
      // A second symbol in the SAME file that calls the first: an intra-file
      // edge, which must never be reported as a dependent.
      { id: "src/a.ts#localCaller", name: "localCaller", kind: "function", path: "src/a.ts", span: "L3-L3",
        signature: "function localCaller(): number", exported: false, origin: "ast" },
    ];
    const edges = [
      { source: "src/a.ts", target: "src/a.ts#alpha", relation: "contains", confidence: "extracted" },
      { source: "src/a.ts", target: "src/a.ts#localCaller", relation: "contains", confidence: "extracted" },
      { source: "src/a.ts#localCaller", target: "src/a.ts#alpha", relation: "calls", confidence: "extracted" },
    ];
    for (let i = 0; i < n; i += 1) {
      const p = "src/dep" + String(i).padStart(2, "0") + ".ts";
      nodes.push({ id: p, name: p.split("/").pop(), kind: "file", path: p, span: "L1-L2", exported: true, origin: "ast" });
      nodes.push({ id: p + "#use" + i, name: "use" + i, kind: "function", path: p, span: "L2-L2",
                   signature: "function use" + i + "(): number", exported: true, origin: "ast" });
      edges.push({ source: p, target: p + "#use" + i, relation: "contains", confidence: "extracted" });
      edges.push({ source: p, target: "src/a.ts", relation: "imports", confidence: "extracted" });
      edges.push({ source: p + "#use" + i, target: "src/a.ts#alpha", relation: "calls", confidence: "inferred" });
    }
    process.stdout.write(JSON.stringify({
      meta: { version: 1, nodeCount: nodes.length, edgeCount: edges.length, languages: ["typescript"], scopes: [] },
      nodes, edges,
    }));
  ' > "$WIRING"
}

RC=0
OUT=""
# run_hook <abs-file-path> [extra-env...] — sets OUT and RC.
run_hook() {
  local fp="$1"; shift
  local payload
  payload=$(node -e '
    process.stdout.write(JSON.stringify({
      session_id: "sid", tool_name: "Edit", tool_input: { file_path: process.argv[1] },
    }))' "$fp")
  OUT=$(printf '%s' "$payload" | env "$@" PATH="$STUBPATH" CLAUDE_PROJECT_DIR="$REPO" "$HOOK")
  RC=$?
}

# ── Case 1: three dependents → all three listed, none truncated ─────────────
write_wiring 3
run_hook "$REPO/src/a.ts"
C=$(ctx "$OUT")
if [ "$RC" -eq 0 ] && valid_json "$OUT" \
   && [[ "$C" == *"3 files depend"* ]] \
   && [[ "$C" == *"src/dep00.ts"* ]] && [[ "$C" == *"src/dep02.ts"* ]] \
   && [[ "$C" != *"more"* ]]; then
  ok_case
else
  fail_case "case1 three-dependents: expected all three, no truncation (rc=$RC, ctx=$C)"
fi

# ── Case 2: intra-file caller is not a dependent ────────────────────────────
# localCaller calls alpha from inside a.ts. Counting it would report the edited
# file as depending on itself.
# Only the indented dependent lines are inspected — the header names the edited
# file by design, so matching the whole block would always "find" src/a.ts.
deplines=$(printf '%s\n' "$C" | grep '^  ' || true)
if [[ "$deplines" != *"localCaller"* ]] && [[ "$deplines" != *"src/a.ts"* ]]; then
  ok_case
else
  fail_case "case2 intra-file: a.ts's own symbols must not be dependents (deplines=$deplines)"
fi

# ── Case 3: 12 dependents → 8 shown + "+4 more" ─────────────────────────────
write_wiring 12
run_hook "$REPO/src/a.ts"
C=$(ctx "$OUT")
shown=$(printf '%s' "$C" | grep -c 'src/dep')
if [ "$RC" -eq 0 ] && [ "$shown" -eq 8 ] && [[ "$C" == *"+4 more"* ]] && [[ "$C" == *"12 files depend"* ]]; then
  ok_case
else
  fail_case "case3 cap: expected 8 shown + '+4 more' of 12 (rc=$RC, shown=$shown, ctx=$C)"
fi

# ── Case 4: maxDependents from project.json is honoured ─────────────────────
echo '{"name":"t","codePath":".","enabledPacks":[],"graft":{"maxDependents":3}}' > "$REPO/.claude/project.json"
run_hook "$REPO/src/a.ts"
C=$(ctx "$OUT")
shown=$(printf '%s' "$C" | grep -c 'src/dep')
RC4=$RC
rm -f "$REPO/.claude/project.json"
if [ "$RC4" -eq 0 ] && [ "$shown" -eq 3 ] && [[ "$C" == *"+9 more"* ]]; then
  ok_case
else
  fail_case "case4 configured-cap: expected 3 shown + '+9 more' (rc=$RC4, shown=$shown, ctx=$C)"
fi

# ── Case 5: blastRadius:false → silent ──────────────────────────────────────
echo '{"name":"t","codePath":".","enabledPacks":[],"graft":{"blastRadius":false}}' > "$REPO/.claude/project.json"
run_hook "$REPO/src/a.ts"
RC5=$RC; OUT5=$OUT
rm -f "$REPO/.claude/project.json"
if [ "$RC5" -eq 0 ] && [ -z "$OUT5" ]; then
  ok_case
else
  fail_case "case5 project-config-off: expected silence (rc=$RC5, out=$OUT5)"
fi

# ── Case 6: a leaf file (no dependents) → silent ────────────────────────────
write_wiring 2
run_hook "$REPO/src/dep00.ts"
if [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case6 leaf-file: expected silence (rc=$RC, out=$OUT)"
fi

# ── Case 7: a file absent from the graph → silent ──────────────────────────
run_hook "$REPO/src/brand-new.ts"
if [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case7 unknown-file: expected silence (rc=$RC, out=$OUT)"
fi

# ── Case 8: an edit UNDER graft/ → silent ──────────────────────────────────
# Rebuilding the index edits its own files; reporting their dependents would fire
# the hook on its own bookkeeping.
run_hook "$REPO/graft/.graph/wiring.json"
RC8a=$RC; OUT8a=$OUT
run_hook "$REPO/graft/index.md"
if [ "$RC8a" -eq 0 ] && [ -z "$OUT8a" ] && [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case8 under-graft-dir: expected silence (rc=$RC8a/$RC, out='$OUT8a'/'$OUT')"
fi

# ── Case 9: kill switch → silent ──────────────────────────────────────────
run_hook "$REPO/src/a.ts" SWARMERY_GRAFT_BLAST=0
if [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case9 kill-switch: expected silence (rc=$RC, out=$OUT)"
fi

# ── Case 10: no wiring.json → silent ─────────────────────────────────────
BARE="$TMP/bare"
mkdir -p "$BARE"
OUT=$(printf '{"session_id":"sid","tool_name":"Edit","tool_input":{"file_path":"%s/src/a.ts"}}' "$BARE" \
  | env PATH="$STUBPATH" CLAUDE_PROJECT_DIR="$BARE" "$HOOK")
RC=$?
if [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case10 no-graph: expected silence (rc=$RC, out=$OUT)"
fi

# ── Case 11: no graft on PATH → silent ──────────────────────────────────
write_wiring 3
OUT=$(printf '{"session_id":"sid","tool_name":"Edit","tool_input":{"file_path":"%s/src/a.ts"}}' "$REPO" \
  | env PATH="$NOGRAFT" CLAUDE_PROJECT_DIR="$REPO" "$HOOK")
RC=$?
if [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case11 no-cli: expected silence (rc=$RC, out=$OUT)"
fi

# ── Case 12: corrupt wiring.json → exit 0, no stdout, one stderr line ───
printf 'not json {{{' > "$WIRING"
ERR="$TMP/err"
OUT=$(printf '{"session_id":"sid","tool_name":"Edit","tool_input":{"file_path":"%s/src/a.ts"}}' "$REPO" \
  | env PATH="$STUBPATH" CLAUDE_PROJECT_DIR="$REPO" "$HOOK" 2>"$ERR")
RC=$?
if [ "$RC" -eq 0 ] && [ -z "$OUT" ] && [ "$(wc -l < "$ERR")" -eq 1 ]; then
  ok_case
else
  fail_case "case12 corrupt-graph: expected exit 0 + 1 stderr line (rc=$RC, out=$OUT, err=$(cat "$ERR"))"
fi

# ── Case 13: a path outside the project → silent ───────────────────────
write_wiring 3
run_hook "/etc/hosts"
if [ "$RC" -eq 0 ] && [ -z "$OUT" ]; then
  ok_case
else
  fail_case "case13 outside-project: expected silence (rc=$RC, out=$OUT)"
fi

# ── Case 14: malformed stdin / no file_path → silent ───────────────────
OUT=$(printf 'not json at all {{{' \
  | env PATH="$STUBPATH" CLAUDE_PROJECT_DIR="$REPO" "$HOOK")
RC=$?
OUT2=$(printf '{"session_id":"sid","tool_name":"Edit","tool_input":{}}' \
  | env PATH="$STUBPATH" CLAUDE_PROJECT_DIR="$REPO" "$HOOK")
RC2=$?
if [ "$RC" -eq 0 ] && [ -z "$OUT" ] && [ "$RC2" -eq 0 ] && [ -z "$OUT2" ]; then
  ok_case
else
  fail_case "case14 malformed-stdin: expected silence + exit 0 (rc=$RC/'$OUT', rc2=$RC2/'$OUT2')"
fi

# ── Case 15: a custom graphDir is honoured ─────────────────────────────
ALT="$TMP/altrepo"
mkdir -p "$ALT/.ctx/.graph" "$ALT/.claude"
cp "$WIRING" "$ALT/.ctx/.graph/wiring.json" 2>/dev/null
WIRING="$ALT/.ctx/.graph/wiring.json"
write_wiring 2
echo '{"name":"t","codePath":".","enabledPacks":[],"graft":{"graphDir":".ctx"}}' > "$ALT/.claude/project.json"
OUT=$(printf '{"session_id":"sid","tool_name":"Edit","tool_input":{"file_path":"%s/src/a.ts"}}' "$ALT" \
  | env PATH="$STUBPATH" CLAUDE_PROJECT_DIR="$ALT" "$HOOK")
RC=$?
C=$(ctx "$OUT")
if [ "$RC" -eq 0 ] && [[ "$C" == *"2 files depend"* ]]; then
  ok_case
else
  fail_case "case15 custom-graphDir: expected the graph at .ctx to be read (rc=$RC, ctx=$C)"
fi

printf 'graft-blast: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
