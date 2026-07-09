#!/bin/bash
# Pre-commit Test Gate
#
# PreToolUse(Bash) hook. Detects `git commit` invocations and runs the
# repo's stack-appropriate test/typecheck suite against staged files.
# Exit 2 = BLOCK the commit; exit 0 = allow.
#
# Strict mode is intentional (rules/ALWAYS.md): broken tests must not land
# on a branch silently. To bypass (e.g., emergency WIP commit you will fix
# in the very next commit), set SKIP_COMMIT_TEST_GATE=1 in your shell.
# Note: `git commit --no-verify` bypasses *git* pre-commit hooks but NOT
# this Claude Code hook — that is by design.
set -e

# ── 1. Read tool input ──────────────────────────────────────────
input=$(cat)
cmd=$(echo "$input" | jq -r '.tool_input.command // empty' 2>/dev/null)

# Bail fast if we cannot read the command
[ -z "$cmd" ] && exit 0

# ── 2. Detect git commit ────────────────────────────────────────
# Match: `git commit`, `git commit -m`, `git commit --amend`, etc.
# Avoid matching: `git commitmessage`, `git committreee`, `git log --oneline commits`.
if ! echo "$cmd" | grep -qE '(^|;|&&|\|\|)\s*git\s+commit(\s|$)'; then
  exit 0
fi

# ── 3. Escape hatch ─────────────────────────────────────────────
if [ "${SKIP_COMMIT_TEST_GATE:-0}" = "1" ]; then
  echo "🟡 pre-commit-test-gate: BYPASSED (SKIP_COMMIT_TEST_GATE=1)"
  exit 0
fi

# ── 4. Locate repo root ─────────────────────────────────────────
REPO=$(git rev-parse --show-toplevel 2>/dev/null || true)
if [ -z "$REPO" ]; then
  echo "🟡 pre-commit-test-gate: not in a git repo; skipping"
  exit 0
fi

cd "$REPO"

# ── 5. Get staged file list ─────────────────────────────────────
# Note: `git commit -a` stages tracked changes implicitly. For accuracy we
# look at BOTH the index AND tracked-modified, treating both as "in scope".
staged=$(git diff --cached --name-only 2>/dev/null)
if echo "$cmd" | grep -qE '\bgit commit\b.*\b-a\b|--all\b'; then
  staged="$staged"$'\n'"$(git diff --name-only 2>/dev/null)"
fi
staged=$(printf '%s\n' "$staged" | sort -u | grep -v '^$' || true)

if [ -z "$staged" ]; then
  echo "🟡 pre-commit-test-gate: nothing staged; skipping"
  exit 0
fi

# Special case for `--amend` with no new staged content (message-only rewrite)
if echo "$cmd" | grep -qE '\bgit commit\b.*\B--amend\b' && [ -z "$(git diff --cached --name-only 2>/dev/null)" ]; then
  echo "🟡 pre-commit-test-gate: --amend with no new staged content; skipping"
  exit 0
fi

failed=()
ran_any=0

# Cross-platform timeout wrapper.
# Linux ships GNU `timeout`; macOS does not unless `brew install coreutils` adds `gtimeout`.
# Strict mode is preserved: if neither binary is available, we run the command unguarded
# and emit a visible warning so the operator knows the budget is unenforced (the underlying
# check still blocks on FAIL — only the wall-clock cap is dropped).
if command -v timeout >/dev/null 2>&1; then
  _timeout() { timeout "$@"; }
elif command -v gtimeout >/dev/null 2>&1; then
  _timeout() { gtimeout "$@"; }
else
  echo "🟡 pre-commit-test-gate: GNU timeout/gtimeout not found; checks run UNGUARDED."
  echo "   (Install with: brew install coreutils. Strict pass/fail behaviour preserved.)"
  _timeout() { shift; "$@"; }
fi

echo ""
echo "🔍 pre-commit-test-gate ($(basename "$REPO")): $(echo "$staged" | wc -l | tr -d ' ') staged file(s)"

# ── 6. Next.js / TypeScript stack ─────────────────────────────────
if echo "$staged" | grep -qE '\.(ts|tsx)$|^package\.json$' \
   && [ -f "$REPO/package.json" ] \
   && [ -f "$REPO/tsconfig.json" ]; then
  ran_any=1
  echo "  • TypeScript stack: running typecheck + tests"

  if ! NODE_ENV=production _timeout 180 npm run typecheck >/tmp/_pcg_tc.$$ 2>&1; then
    echo "    ✗ typecheck FAILED:"
    tail -25 /tmp/_pcg_tc.$$
    failed+=("typecheck")
  else
    echo "    ✓ typecheck"
  fi
  rm -f /tmp/_pcg_tc.$$

  # Only run tests if a `test` script exists in package.json
  if grep -q '"test"\s*:' "$REPO/package.json"; then
    if ! _timeout 300 npm test --silent -- --passWithNoTests >/tmp/_pcg_test.$$ 2>&1; then
      echo "    ✗ npm test FAILED:"
      tail -25 /tmp/_pcg_test.$$
      failed+=("npm test")
    else
      echo "    ✓ npm test"
    fi
    rm -f /tmp/_pcg_test.$$
  fi
fi

# ── 7. Python stack ─────────────────────────────────────────────
if echo "$staged" | grep -qE '\.py$' \
   && [ -f "$REPO/pyproject.toml" ]; then
  ran_any=1
  echo "  • Python stack: running mypy + pytest"

  # Prefer the repo venv tools — the system pytest/mypy may lack plugins
  # the suite depends on (pytest-asyncio) or pin a different Python.
  MYPY_BIN="mypy"; PYTEST_BIN="pytest"
  [ -x "$REPO/venv/bin/mypy" ] && MYPY_BIN="$REPO/venv/bin/mypy"
  [ -x "$REPO/venv/bin/pytest" ] && PYTEST_BIN="$REPO/venv/bin/pytest"

  if command -v "$MYPY_BIN" >/dev/null 2>&1 && [ -d "$REPO/src" ]; then
    if ! _timeout 90 "$MYPY_BIN" src/ >/tmp/_pcg_my.$$ 2>&1; then
      echo "    ✗ mypy FAILED:"
      tail -15 /tmp/_pcg_my.$$
      failed+=("mypy")
    else
      echo "    ✓ mypy"
    fi
    rm -f /tmp/_pcg_my.$$
  fi

  if command -v "$PYTEST_BIN" >/dev/null 2>&1 && [ -d "$REPO/test" ]; then
    if ! _timeout 300 "$PYTEST_BIN" test/ -x --tb=short -q >/tmp/_pcg_pt.$$ 2>&1; then
      echo "    ✗ pytest FAILED:"
      tail -25 /tmp/_pcg_pt.$$
      failed+=("pytest")
    else
      echo "    ✓ pytest"
    fi
    rm -f /tmp/_pcg_pt.$$
  fi
fi

# ── 8. Helm chart stack ─────────────────────────────────────────
if echo "$staged" | grep -qE '^(Chart\.yaml|values.*\.ya?ml|templates/)' \
   && [ -f "$REPO/Chart.yaml" ]; then
  ran_any=1
  echo "  • Helm chart: running helm lint"

  if command -v helm >/dev/null 2>&1; then
    if ! _timeout 30 helm lint . >/tmp/_pcg_hl.$$ 2>&1; then
      echo "    ✗ helm lint FAILED:"
      tail -15 /tmp/_pcg_hl.$$
      failed+=("helm lint")
    else
      echo "    ✓ helm lint"
    fi
    rm -f /tmp/_pcg_hl.$$
  fi
fi

# ── 8a. Terraform stack ─────────────────────────────────────────
if echo "$staged" | grep -qE '\.(tf|tfvars)$'; then
  ran_any=1
  echo "  • Terraform stack: running fmt + validate"

  if command -v terraform >/dev/null 2>&1; then
    if ! _timeout 30 terraform fmt -check -recursive -diff >/tmp/_pcg_tf.$$ 2>&1; then
      echo "    ✗ terraform fmt -check FAILED (run 'terraform fmt -recursive' to fix):"
      tail -15 /tmp/_pcg_tf.$$
      failed+=("terraform fmt")
    else
      echo "    ✓ terraform fmt"
    fi
    rm -f /tmp/_pcg_tf.$$

    # validate requires init; skip if .terraform missing (don't auto-init in a hook)
    if [ -d "$REPO/.terraform" ]; then
      if ! _timeout 30 terraform validate >/tmp/_pcg_tfv.$$ 2>&1; then
        echo "    ✗ terraform validate FAILED:"
        tail -15 /tmp/_pcg_tfv.$$
        failed+=("terraform validate")
      else
        echo "    ✓ terraform validate"
      fi
      rm -f /tmp/_pcg_tfv.$$
    else
      echo "    ⚠ terraform validate skipped (.terraform not initialised; run 'terraform init' once)"
    fi
  else
    echo "    ⚠ terraform not installed; skipping (install: brew install terraform)"
  fi
fi

# ── 8b. Shell scripts (any repo) ────────────────────────────────
if echo "$staged" | grep -qE '\.sh$'; then
  if command -v shellcheck >/dev/null 2>&1; then
    ran_any=1
    echo "  • Shell scripts: running shellcheck"
    sh_files=$(echo "$staged" | grep -E '\.sh$' || true)
    # `[ ! -f ] ||` (not `[ -f ] &&`): staged DELETIONS must be skipped
    # silently, not reported as a shellcheck failure with empty output.
    if ! echo "$sh_files" | xargs -I{} sh -c "[ ! -f '$REPO/{}' ] || shellcheck '$REPO/{}'" >/tmp/_pcg_sc.$$ 2>&1; then
      echo "    ✗ shellcheck FAILED:"
      tail -25 /tmp/_pcg_sc.$$
      failed+=("shellcheck")
    else
      echo "    ✓ shellcheck"
    fi
    rm -f /tmp/_pcg_sc.$$
  fi
  # If shellcheck not installed, silently skip — not a hard requirement
fi

# ── 8c. Flyway-style SQL migration naming ───────────────────────
# Convention: V{major}.{minor}.{patch}__description.sql.
# Hook only checks NAMING; semantic safety is on @migration-helper.
flyway_sql=$(echo "$staged" | grep -E '/backendMigration/.*\.sql$|/migrations/V[0-9].*\.sql$' || true)
if [ -n "$flyway_sql" ]; then
  ran_any=1
  echo "  • Flyway migrations: checking naming convention"
  bad_names=""
  while IFS= read -r sql; do
    [ -z "$sql" ] && continue
    base=$(basename "$sql")
    if ! echo "$base" | grep -qE '^V[0-9]+(\.[0-9]+)*__[a-zA-Z0-9_-]+\.sql$'; then
      bad_names="${bad_names}      ${sql}"$'\n'
    fi
  done <<< "$flyway_sql"
  if [ -n "$bad_names" ]; then
    echo "    ✗ Flyway naming FAILED — expected V{n}[.{n}]*__description.sql:"
    echo "$bad_names"
    failed+=("flyway naming")
  else
    echo "    ✓ Flyway naming (also verify checksums of applied migrations are unchanged — see rules/NEVER.md)"
  fi
fi

# ── 8d. YAML syntax (non-Helm yaml) ─────────────────────────────
yaml_staged=$(echo "$staged" | grep -E '\.ya?ml$' | grep -vE '(Chart\.yaml|values.*\.ya?ml|templates/)' || true)
if [ -n "$yaml_staged" ] && command -v yq >/dev/null 2>&1; then
  ran_any=1
  echo "  • YAML syntax: running yq eval"
  bad_yaml=""
  while IFS= read -r yf; do
    [ -z "$yf" ] && continue
    if ! yq eval '.' "$REPO/$yf" >/dev/null 2>&1; then
      bad_yaml="${bad_yaml}      ${yf}"$'\n'
    fi
  done <<< "$yaml_staged"
  if [ -n "$bad_yaml" ]; then
    echo "    ✗ YAML parse FAILED:"
    echo "$bad_yaml"
    failed+=("yaml syntax")
  else
    echo "    ✓ YAML syntax"
  fi
fi

# ── 8e. SECRET-BEARING file guard (universal) ───────────────────
# These files should NEVER be committed. Even if user force-staged them,
# block here. Documented in rules/NEVER.md.
secret_bearing=$(echo "$staged" | grep -E '\.populated\.ya?ml$|/output/.*\.rsc$|\.tfstate(\.backup)?$|^\.env(\.|$)' || true)
if [ -n "$secret_bearing" ]; then
  ran_any=1
  echo "  • SECRET-BEARING file BLOCK"
  # shellcheck disable=SC2001  # sed for per-line indent is clearer than ${var//}
  echo "$secret_bearing" | sed 's/^/      /'
  echo "    ✗ These files contain secrets and must NEVER be committed (see rules/NEVER.md)."
  echo "      Unstage them with 'git restore --staged <file>' before committing."
  failed+=("secret-bearing files staged")
fi

# ── 9. Verdict ──────────────────────────────────────────────────
if [ $ran_any -eq 0 ]; then
  echo "  • no recognized stack in staged files; allowing commit"
  exit 0
fi

if [ ${#failed[@]} -gt 0 ]; then
  echo ""
  echo "🚫 BLOCKED: pre-commit-test-gate failed (${failed[*]})"
  echo ""
  echo "Fix the failure before committing. See rules/ALWAYS.md for context."
  echo "Emergency bypass: SKIP_COMMIT_TEST_GATE=1 git commit ..."
  echo "  (Note: --no-verify does NOT bypass this hook; that's intentional.)"
  echo ""
  exit 2
fi

echo ""
echo "✅ pre-commit-test-gate PASSED — proceeding to commit"
exit 0
