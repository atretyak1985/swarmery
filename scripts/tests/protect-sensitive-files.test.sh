#!/bin/bash
# Behavioral tests for plugins/core/hooks/protect-sensitive-files.sh.
#
# Framework-free (portable, no bats dependency): each case feeds a hook JSON
# payload on stdin and asserts the exit code — 2 = BLOCK, 0 = ALLOW. Run
# locally with `bash scripts/tests/protect-sensitive-files.test.sh`; wired into
# CI alongside the shell-syntax/shellcheck gates.
set -uo pipefail

HOOK="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/plugins/core/hooks/protect-sensitive-files.sh"

pass=0
fail=0

# record <expected-exit> <actual-exit> <description> — tally one case. Passing
# cases are silent unless VERBOSE is set (`VERBOSE=1 bash <this file>`).
record() {
  local expected="$1" actual="$2" desc="$3"
  if [ "$actual" -eq "$expected" ]; then
    pass=$((pass + 1))
    if [ -n "${VERBOSE:-}" ]; then
      printf '  ✓ %s (exit %s)\n' "$desc" "$actual"
    fi
  else
    fail=$((fail + 1))
    printf '  ✗ %s (expected exit %s, got %s)\n' "$desc" "$expected" "$actual"
  fi
}

# assert <expected-exit> <description> <json-payload>
assert() {
  local expected="$1" desc="$2" payload="$3"
  printf '%s' "$payload" | "$HOOK" >/dev/null 2>&1
  record "$expected" "$?" "$desc"
}

# assert_tmpdir <expected-exit> <description> <TMPDIR value | --unset> <json-payload>
# Same as assert, but runs the hook with TMPDIR pinned to a value or removed
# from its environment, so the temp-dir exemption is tested hermetically.
assert_tmpdir() {
  local expected="$1" desc="$2" tmpdir="$3" payload="$4"
  if [ "$tmpdir" = "--unset" ]; then
    printf '%s' "$payload" | ( unset TMPDIR; "$HOOK" ) >/dev/null 2>&1
  else
    printf '%s' "$payload" | TMPDIR="$tmpdir" "$HOOK" >/dev/null 2>&1
  fi
  record "$expected" "$?" "$desc"
}

# jp <path> — build a minimal hook payload naming a target file_path.
jp() { printf '{"tool_input":{"file_path":"%s"}}' "$1"; }

# jpc <path> <cwd> — the same payload plus the session `cwd`, which the hook
# uses to canonicalize a relative file_path.
jpc() { printf '{"cwd":"%s","tool_input":{"file_path":"%s"}}' "$2" "$1"; }

# ── BLOCK (exit 2) ────────────────────────────────────────────────
assert 2 ".env file"                 "$(jp '/repo/.env')"
assert 2 ".env.production"           "$(jp '/repo/.env.production')"
assert 2 "package-lock.json"         "$(jp '/repo/package-lock.json')"
assert 2 "file inside .git/"         "$(jp '/repo/.git/config')"
assert 2 "file inside node_modules/" "$(jp '/repo/node_modules/x/index.js')"
assert 2 "terraform state"           "$(jp '/repo/infra/terraform.tfstate')"
assert 2 "populated values"          "$(jp '/repo/secrets.populated.yaml')"
assert 2 "prod values"               "$(jp '/repo/values.prod.yaml')"
assert 2 "generated .rsc"            "$(jp '/repo/output/router.rsc')"

# ── BLOCK: credential material (added with read-before-write.sh) ──
# These are also the paths read-before-write.sh refuses to echo, so the list is
# load-bearing twice: un-editable AND un-quotable.
assert 2 "private key .pem"          "$(jp '/repo/certs/server.pem')"
assert 2 "private key .key"          "$(jp '/repo/certs/server.key')"
assert 2 "ssh id_rsa"                "$(jp '/home/u/.ssh/id_rsa')"
assert 2 "ssh id_ed25519"            "$(jp '/home/u/.ssh/id_ed25519')"
assert 2 ".npmrc"                    "$(jp '/repo/.npmrc')"
assert 2 ".netrc"                    "$(jp '/home/u/.netrc')"
assert 2 "aws credentials"           "$(jp '/home/u/.aws/credentials')"
assert 2 "gcp service account"       "$(jp '/repo/service-account-prod.json')"
assert 2 "kubeconfig"                "$(jp '/home/u/.kube/kubeconfig')"
assert 2 "terraform tfvars"          "$(jp '/repo/infra/prod.tfvars')"
assert 2 "settings.local.json"       "$(jp '/repo/.claude/settings.local.json')"
assert 2 "java keystore"             "$(jp '/repo/app.jks')"

# ── ALLOW (exit 0) ────────────────────────────────────────────────
assert 0 "ordinary source file"      "$(jp '/repo/src/app.ts')"
assert 0 "README.md"                 "$(jp '/repo/README.md')"
assert 0 "no file_path"              '{"tool_input":{}}'
# Segment match, not substring: docker-build/ must NOT trip the build/ rule.
assert 0 "skills/docker-build/ ok"   "$(jp '/repo/skills/docker-build/x.sh')"
# .env* is a basename prefix; environment.ts is not a dotenv file.
assert 0 "environment.ts not dotenv" "$(jp '/repo/environment.ts')"
# The credential patterns are basename-anchored, not substring: a source file
# that merely mentions a credential word stays editable.
assert 0 "keyboard.ts is not a .key"  "$(jp '/repo/src/keyboard.ts')"
assert 0 "credentials.test.ts ok"     "$(jp '/repo/src/credentials.test.ts')"
assert 0 "settings.json (not local)"  "$(jp '/repo/.claude/settings.json')"

# ── Exemption 1: dotenv templates ─────────────────────────────────
# A template documents variable NAMES and is committed. Only a TRAILING
# .example/.sample/.template suffix qualifies; every other .env* stays blocked.
assert 0 "tpl allow: .env.example"               "$(jp '/repo/.env.example')"
assert 0 "tpl allow: .env.sample"                "$(jp '/repo/.env.sample')"
assert 0 "tpl allow: .env.template"              "$(jp '/repo/.env.template')"
assert 0 "tpl allow: .env.local.example"         "$(jp '/repo/.env.local.example')"
assert 2 "tpl block: .env"                       "$(jp '/repo/.env')"
assert 2 "tpl block: .env.production"            "$(jp '/repo/.env.production')"
assert 2 "tpl block: .env.example.local"         "$(jp '/repo/.env.example.local')"

# ── Exemption 2: lock files under the OS temp dir ─────────────────
# A lock file in a scratch copy under /tmp, /private/tmp or $TMPDIR is not the
# project's lock file. The exemption is lock-files-only and path-anchored.
assert 0 "tmp allow: /tmp package-lock.json"     "$(jp '/tmp/w/package-lock.json')"
assert 0 "tmp allow: /private/tmp yarn.lock"     "$(jp '/private/tmp/s/u/scratchpad/wt/yarn.lock')"
assert 0 "tmp allow: /tmp pnpm-lock.yaml"        "$(jp '/tmp/w/pnpm-lock.yaml')"
assert_tmpdir 0 "tmp allow: \$TMPDIR package-lock.json" '/var/folders/x/T/' \
  "$(jp '/var/folders/x/T/w/package-lock.json')"
assert 0 "tmp allow: relative lock, cwd /tmp/w"  "$(jpc 'package-lock.json' '/tmp/w')"

assert 2 "tmp block: /repo package-lock.json"    "$(jp '/repo/package-lock.json')"
assert 2 "tmp block: relative lock, cwd /repo"   "$(jpc 'package-lock.json' '/repo')"
# A ".." segment never qualifies — the path escapes the temp dir.
assert 2 "tmp block: /tmp/../repo lock"          "$(jp '/tmp/../repo/package-lock.json')"
# Every other rule applies under the temp dir exactly as elsewhere.
assert 2 "tmp block: /tmp .env"                  "$(jp '/tmp/w/.env')"
assert 2 "tmp block: /tmp server.pem"            "$(jp '/tmp/w/server.pem')"
assert 2 "tmp block: /tmp .git/config"           "$(jp '/tmp/w/.git/config')"
assert 2 "tmp block: /tmp node_modules lock"     "$(jp '/tmp/w/node_modules/x/package-lock.json')"
# A degenerate TMPDIR must not turn the whole filesystem into "temp".
assert_tmpdir 2 "tmp block: /repo lock, TMPDIR=/"    '/'       "$(jp '/repo/package-lock.json')"
assert_tmpdir 2 "tmp block: /repo lock, TMPDIR unset" '--unset' "$(jp '/repo/package-lock.json')"

# Real paths under the OS temp dir. A git checkout or worktree there is a real
# project (agent scratchpads live under /private/tmp), and a symlink there can
# point anywhere — the exemption must judge where a write would LAND.
scratch=$(mktemp -d /tmp/psf-test.XXXXXX)
trap 'rm -rf "$scratch"' EXIT
mkdir -p "$scratch/copy" "$scratch/repo" "$scratch/w"
git -C "$scratch/repo" init -q
ln -s /usr "$scratch/link"
ln -s /usr/package-lock.json "$scratch/w/package-lock.json"
assert 0 "tmp allow: lock in a plain temp copy"      "$(jp "$scratch/copy/package-lock.json")"
assert 2 "tmp block: lock in a temp-dir git repo"    "$(jp "$scratch/repo/package-lock.json")"
assert 2 "tmp block: lock deep in a temp-dir repo"   "$(jp "$scratch/repo/sub/yarn.lock")"
assert 2 "tmp block: lock through a temp symlink"    "$(jp "$scratch/link/package-lock.json")"
assert 2 "tmp block: a symlinked lock file"          "$(jp "$scratch/w/package-lock.json")"

printf 'protect-sensitive-files: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
