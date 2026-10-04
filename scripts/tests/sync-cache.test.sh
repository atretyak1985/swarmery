#!/bin/bash
# Behavioral tests for scripts/sync-cache.sh.
#
# Framework-free. Each case builds a throwaway HOME with fake Claude config dirs
# (a default ~/.claude and an account ~/.claude-acct), each carrying a plugin
# cache and an installed_plugins.json, plus a fake source plugins/ tree, and
# asserts where the script writes:
#   (a) only into the version dir a config dir has INSTALLED, never into a
#       stray sibling version dir;
#   (b) into every config dir, not just ~/.claude;
#   (c) a version drift between installed and source is said out loud;
#   (d) a recorded-but-missing install path is skipped, not created.
# It also exercises the primary-worktree/main-branch guard: a linked worktree
# or a non-main branch must refuse (exit 0, nothing synced) unless overridden
# with --force or SWARMERY_SYNC_CACHE_FORCE=1. These cases use the
# SWARMERY_REPO_ROOT test override so the guard runs against a disposable
# fixture git repo instead of this real checkout (whose own branch is
# whatever the test happens to run from). Finally it commits plugins/ changes
# through scripts/git-hooks/post-commit in a fixture repo whose sync script is
# a stale, unguarded stub, proving the hook refuses on its own.
# Run locally with `bash scripts/tests/sync-cache.test.sh`; CI discovers
# scripts/tests/*.test.sh.
set -uo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/scripts/sync-cache.sh"

pass=0
fail=0
fail_case() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }
ok_case() { pass=$((pass + 1)); }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# --- git fixture (primary worktree on main + a linked worktree + a branch) -
GIT_FIXTURE="$tmp/git-fixture"
mkdir -p "$GIT_FIXTURE"
git init -q -b main "$GIT_FIXTURE"
git -C "$GIT_FIXTURE" -c user.email=t@t -c user.name=t commit -q --allow-empty -m init
PRIMARY_MAIN="$GIT_FIXTURE"
git -C "$GIT_FIXTURE" worktree add -q -b feature/x "$tmp/git-fixture-wt" >/dev/null
LINKED_WORKTREE="$tmp/git-fixture-wt"
git -C "$GIT_FIXTURE" branch -q feature/y
PRIMARY_NON_MAIN="$tmp/git-fixture-branch"
cp -R "$GIT_FIXTURE" "$PRIMARY_NON_MAIN"
git -C "$PRIMARY_NON_MAIN" checkout -q feature/y

# --- fixture ---------------------------------------------------------------
SRC="$tmp/plugins"
mkdir -p "$SRC/core/.claude-plugin" "$SRC/core/hooks" "$SRC/web-pack/.claude-plugin"
printf '{"name":"core","version":"2.0.0"}\n' > "$SRC/core/.claude-plugin/plugin.json"
printf '{"name":"web-pack","version":"1.0.0"}\n' > "$SRC/web-pack/.claude-plugin/plugin.json"
printf 'echo new\n' > "$SRC/core/hooks/marker.sh"
printf 'x\n' > "$SRC/web-pack/README.md"

HOME_DIR="$tmp/home"
DEF="$HOME_DIR/.claude"
ACCT="$HOME_DIR/.claude-acct"
# default dir: core installed at 1.9.0 (drift vs source 2.0.0); a stray 1.8.0 dir
# that must stay untouched; web-pack installed at 1.0.0 (no drift).
mkdir -p "$DEF/plugins/cache/swarmery/core/1.9.0/.claude-plugin" \
         "$DEF/plugins/cache/swarmery/core/1.8.0" \
         "$DEF/plugins/cache/swarmery/web-pack/1.0.0/.claude-plugin"
printf '{"name":"core","version":"1.9.0"}\n' > "$DEF/plugins/cache/swarmery/core/1.9.0/.claude-plugin/plugin.json"
printf 'old\n' > "$DEF/plugins/cache/swarmery/core/1.8.0/stale.txt"
cat > "$DEF/plugins/installed_plugins.json" <<EOF
{"version":2,"plugins":{
  "core@swarmery":[{"scope":"user","version":"1.9.0","installPath":"$DEF/plugins/cache/swarmery/core/1.9.0"}],
  "web-pack@swarmery":[{"scope":"project","projectPath":"/p","version":"1.0.0","installPath":"$DEF/plugins/cache/swarmery/web-pack/1.0.0"}],
  "other@elsewhere":[{"scope":"user","version":"9.9.9","installPath":"$DEF/plugins/cache/elsewhere/other/9.9.9"}]
}}
EOF
# account dir: core installed at 2.0.0 (matches source); a recorded-but-missing
# install path for web-pack.
mkdir -p "$ACCT/plugins/cache/swarmery/core/2.0.0/.claude-plugin"
printf '{"name":"core","version":"2.0.0"}\n' > "$ACCT/plugins/cache/swarmery/core/2.0.0/.claude-plugin/plugin.json"
cat > "$ACCT/plugins/installed_plugins.json" <<EOF
{"plugins":{
  "core@swarmery":[{"scope":"user","version":"2.0.0","installPath":"$ACCT/plugins/cache/swarmery/core/2.0.0"}],
  "web-pack@swarmery":[{"scope":"user","version":"1.0.0","installPath":"$ACCT/plugins/cache/swarmery/web-pack/1.0.0"}]
}}
EOF
# a third config dir with no swarmery installs at all
mkdir -p "$HOME_DIR/.claude-empty/plugins"

out="$(HOME="$HOME_DIR" SWARMERY_PLUGINS_DIR="$SRC" SWARMERY_REPO_ROOT="$PRIMARY_MAIN" bash "$SCRIPT" 2>&1)"
rc=$?

# --- assertions ------------------------------------------------------------
[ "$rc" -eq 0 ] && ok_case || fail_case "exit code $rc, output: $out"

[ -f "$DEF/plugins/cache/swarmery/core/1.9.0/hooks/marker.sh" ] && ok_case \
  || fail_case "default dir: installed core/1.9.0 did not receive the source tree"

[ ! -e "$DEF/plugins/cache/swarmery/core/1.8.0/hooks" ] && [ -f "$DEF/plugins/cache/swarmery/core/1.8.0/stale.txt" ] && ok_case \
  || fail_case "default dir: stray core/1.8.0 was written to — only the INSTALLED version dir may change"

grep -q '"version":"1.9.0"' "$DEF/plugins/cache/swarmery/core/1.9.0/.claude-plugin/plugin.json" && ok_case \
  || fail_case "default dir: .claude-plugin/ of the installed copy was overwritten (must be excluded)"

[ -f "$DEF/plugins/cache/swarmery/web-pack/1.0.0/README.md" ] && ok_case \
  || fail_case "default dir: project-scoped web-pack install was not synced"

[ -f "$ACCT/plugins/cache/swarmery/core/2.0.0/hooks/marker.sh" ] && ok_case \
  || fail_case "account dir ~/.claude-acct was not synced — only ~/.claude was"

[ ! -e "$ACCT/plugins/cache/swarmery/web-pack" ] && ok_case \
  || fail_case "account dir: a recorded-but-missing install path was created instead of skipped"

[ ! -e "$DEF/plugins/cache/elsewhere" ] && ok_case \
  || fail_case "a plugin from another marketplace was touched"

printf '%s\n' "$out" | grep -q 'installed 1.9.0, source 2.0.0' && ok_case \
  || fail_case "version drift (installed 1.9.0 vs source 2.0.0) was not reported; output: $out"

printf '%s\n' "$out" | grep -q '3 installed dir(s) updated' && ok_case \
  || fail_case "summary should count 3 synced dirs; output: $out"

printf '%s\n' "$out" | grep -q '1 with a version drift' && ok_case \
  || fail_case "summary should count 1 drift; output: $out"

printf '%s\n' "$out" | grep -q '1 config dir(s) without swarmery installs' && ok_case \
  || fail_case "summary should count the empty config dir; output: $out"

# New source content the guarded runs below must NOT deliver: a refused run
# that re-synced only already-synced files would leave no trace otherwise.
printf 'echo after-guard\n' > "$SRC/core/hooks/after-guard.sh"
guarded_copy="$DEF/plugins/cache/swarmery/core/1.9.0/hooks/after-guard.sh"

# --- guard: linked worktree refuses ----------------------------------------
before="$(find "$DEF/plugins/cache" -newer "$tmp" 2>/dev/null | sort)"
out_wt="$(HOME="$HOME_DIR" SWARMERY_PLUGINS_DIR="$SRC" SWARMERY_REPO_ROOT="$LINKED_WORKTREE" bash "$SCRIPT" 2>&1)"
rc_wt=$?
after_wt="$(find "$DEF/plugins/cache" -newer "$tmp" 2>/dev/null | sort)"

[ "$rc_wt" -eq 0 ] && ok_case || fail_case "linked worktree: expected exit 0, got $rc_wt"
printf '%s\n' "$out_wt" | grep -qi 'not the primary worktree' && ok_case \
  || fail_case "linked worktree: refusal message missing; output: $out_wt"
[ "$before" = "$after_wt" ] && [ ! -e "$guarded_copy" ] && ok_case \
  || fail_case "linked worktree: cache files changed despite refusal"

# --- guard: non-main branch in primary worktree refuses ---------------------
before="$(find "$DEF/plugins/cache" -newer "$tmp" 2>/dev/null | sort)"
out_branch="$(HOME="$HOME_DIR" SWARMERY_PLUGINS_DIR="$SRC" SWARMERY_REPO_ROOT="$PRIMARY_NON_MAIN" bash "$SCRIPT" 2>&1)"
rc_branch=$?
after_branch="$(find "$DEF/plugins/cache" -newer "$tmp" 2>/dev/null | sort)"

[ "$rc_branch" -eq 0 ] && ok_case || fail_case "non-main branch: expected exit 0, got $rc_branch"
printf '%s\n' "$out_branch" | grep -qi "feature/y" && ok_case \
  || fail_case "non-main branch: refusal message missing branch name; output: $out_branch"
[ "$before" = "$after_branch" ] && [ ! -e "$guarded_copy" ] && ok_case \
  || fail_case "non-main branch: cache files changed despite refusal"

# --- override: SWARMERY_SYNC_CACHE_FORCE=1 proceeds -------------------------
out_force_env="$(HOME="$HOME_DIR" SWARMERY_PLUGINS_DIR="$SRC" SWARMERY_REPO_ROOT="$PRIMARY_NON_MAIN" SWARMERY_SYNC_CACHE_FORCE=1 bash "$SCRIPT" 2>&1)"
rc_force_env=$?
[ "$rc_force_env" -eq 0 ] && ok_case || fail_case "SWARMERY_SYNC_CACHE_FORCE=1: expected exit 0, got $rc_force_env"
printf '%s\n' "$out_force_env" | grep -q 'installed dir(s) updated' && [ -f "$guarded_copy" ] && ok_case \
  || fail_case "SWARMERY_SYNC_CACHE_FORCE=1 did not sync; output: $out_force_env"
rm -f "$guarded_copy"

# --- override: --force flag proceeds ----------------------------------------
out_force_flag="$(HOME="$HOME_DIR" SWARMERY_PLUGINS_DIR="$SRC" SWARMERY_REPO_ROOT="$PRIMARY_NON_MAIN" bash "$SCRIPT" --force 2>&1)"
rc_force_flag=$?
[ "$rc_force_flag" -eq 0 ] && ok_case || fail_case "--force: expected exit 0, got $rc_force_flag"
printf '%s\n' "$out_force_flag" | grep -q 'installed dir(s) updated' && [ -f "$guarded_copy" ] && ok_case \
  || fail_case "--force did not sync; output: $out_force_flag"

# --- hook: scripts/git-hooks/post-commit guards on its own ------------------
# The fixture repo's scripts/sync-cache.sh is a STALE, unguarded stub that only
# drops a sentinel, so every refusal below proves the hook's own guard — the
# case where an old worktree still carries a pre-guard copy of the script.
HOOK_REPO="$tmp/hook-repo"
SENTINEL="$tmp/hook-sentinel"
HOOK_SRC="$(dirname "$SCRIPT")/git-hooks/post-commit"
mkdir -p "$HOOK_REPO/scripts/git-hooks"
git init -q -b main "$HOOK_REPO"
cp "$HOOK_SRC" "$HOOK_REPO/scripts/git-hooks/post-commit"
printf '#!/usr/bin/env bash\ntouch "%s"\n' "$SENTINEL" > "$HOOK_REPO/scripts/sync-cache.sh"
git -C "$HOOK_REPO" config core.hooksPath scripts/git-hooks
gitc() { git -c user.email=t@t -c user.name=t -c commit.gpgsign=false "$@"; }
gitc -C "$HOOK_REPO" add -A
gitc -C "$HOOK_REPO" commit -q -m init 2>/dev/null
rm -f "$SENTINEL"
git -C "$HOOK_REPO" worktree add -q -b feature/z "$tmp/hook-wt" >/dev/null 2>&1
# commit_plugin <dir> <file> [env...] — commit a plugins/ change, print hook output
commit_plugin() {
  local dir="$1" file="$2"
  shift 2
  mkdir -p "$dir/plugins"
  echo x > "$dir/plugins/$file"
  gitc -C "$dir" add -A
  env "$@" git -c user.email=t@t -c user.name=t -c commit.gpgsign=false -C "$dir" commit -q -m "$file" 2>&1
}

out_hook_wt="$(commit_plugin "$tmp/hook-wt" a)"
[ ! -e "$SENTINEL" ] && printf '%s\n' "$out_hook_wt" | grep -q 'linked worktree' && ok_case \
  || fail_case "hook: a linked-worktree plugin commit reached the (stale) script or printed no refusal; output: $out_hook_wt"

gitc -C "$HOOK_REPO" checkout -q -b feature/w
out_hook_branch="$(commit_plugin "$HOOK_REPO" b)"
[ ! -e "$SENTINEL" ] && printf '%s\n' "$out_hook_branch" | grep -q "feature/w" && ok_case \
  || fail_case "hook: a non-main plugin commit reached the (stale) script or printed no refusal; output: $out_hook_branch"
gitc -C "$HOOK_REPO" checkout -q main

echo y > "$HOOK_REPO/README"
gitc -C "$HOOK_REPO" add -A
gitc -C "$HOOK_REPO" commit -q -m readme 2>/dev/null
[ ! -e "$SENTINEL" ] && ok_case \
  || fail_case "hook: a commit that did not touch plugins/ ran the script"

commit_plugin "$HOOK_REPO" c >/dev/null
[ -e "$SENTINEL" ] && ok_case \
  || fail_case "hook: a plugin commit in the primary worktree on main did not run the script"
rm -f "$SENTINEL"

commit_plugin "$tmp/hook-wt" d SWARMERY_SYNC_CACHE_FORCE=1 >/dev/null
[ -e "$SENTINEL" ] && ok_case \
  || fail_case "hook: SWARMERY_SYNC_CACHE_FORCE=1 did not let a linked-worktree commit run the script"

printf 'sync-cache: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
