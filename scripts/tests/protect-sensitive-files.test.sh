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

# assert <expected-exit> <description> <json-payload>
assert() {
  local expected="$1" desc="$2" payload="$3" actual
  printf '%s' "$payload" | "$HOOK" >/dev/null 2>&1
  actual=$?
  if [ "$actual" -eq "$expected" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf '  ✗ %s (expected exit %s, got %s)\n' "$desc" "$expected" "$actual"
  fi
}

# jp <path> — build a minimal hook payload naming a target file_path.
jp() { printf '{"tool_input":{"file_path":"%s"}}' "$1"; }

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

printf 'protect-sensitive-files: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
