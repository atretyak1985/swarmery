#!/bin/bash
# Pre-commit Test Gate
#
# PreToolUse(Bash) hook. Detects `git commit` invocations and runs the
# repo's stack-appropriate test/typecheck suite against staged files.
# Exit 2 = BLOCK the commit; exit 0 = allow.
#
# Detection is robust to the real ways `git commit` gets invoked:
#   git commit …            command git commit …      FOO=1 git commit …
#   git -C <path> commit …  git -c k=v commit …       git --no-pager commit …
# When `-C <path>` is present the checks run against THAT repo (resolved
# against the hook's cwd), not the cwd.
#
# Output contract (see change C/D of the gate-hardening plan):
#   • stdout (fd 3, reserved) carries ONLY a single-line JSON control object on
#     the real stdout fd: {"continue":true[,"systemMessage":"…"]} — nothing else.
#   • stderr (fd 1, redirected below) carries ALL human/progress/diagnostic
#     text. Claude Code discards an exit-2 hook's stdout and feeds only its
#     stderr back to the model, so every failure diagnostic and block banner
#     MUST be on fd 2 — routing fd 1 → fd 2 guarantees that centrally and
#     keeps the fd-3 JSON uncorrupted so the systemMessage actually parses.
#
# Strict mode is intentional (rules/ALWAYS.md): broken tests must not land
# on a branch silently. To bypass (emergency WIP commit you fix in the very
# next commit), set SKIP_COMMIT_TEST_GATE=1 — either exported in your shell
# OR inline in the command (`SKIP_COMMIT_TEST_GATE=1 git commit …`). Every
# bypass is appended to .claude-workspace/metrics/gate-bypasses.jsonl.
# Note: `git commit --no-verify` bypasses *git* pre-commit hooks but NOT
# this Claude Code hook — that is by design.
set -e

# ── 1. Read tool input ──────────────────────────────────────────
input=$(cat)
cmd=$(echo "$input" | jq -r '.tool_input.command // empty' 2>/dev/null)

# Bail fast if we cannot read the command
[ -z "$cmd" ] && exit 0

# ── 1a. Output channels ─────────────────────────────────────────
# Reserve fd 3 as the JSON control channel (the real stdout Claude Code
# parses) and route fd 1 → fd 2 so every progress line AND every failure
# diagnostic lands on stderr. This makes the exit-2 BLOCK reason visible to
# the model and keeps fd 3 clean for the systemMessage JSON object.
exec 3>&1 1>&2
HOOK_CWD="$PWD"

# Emit the JSON control object on fd 3 — a single-line
# {"continue":true[,"systemMessage":"…"]} object on the real stdout fd.
# $1 (optional) = a systemMessage to surface to the agent.
_emit_continue() {
  if [ -n "${1:-}" ]; then
    jq -cn --arg m "$1" '{continue:true,systemMessage:$m}' >&3 2>/dev/null \
      || printf '{"continue":true}\n' >&3
  else
    printf '{"continue":true}\n' >&3
  fi
}

# ── 2. Detect `git commit` ──────────────────────────────────────
# Split the command on shell connectors, then per-segment strip leading
# `VAR=value` env tokens and an optional `command ` builtin, then match
# `git` followed by any mix of global flags (-C <arg>, -c <arg>, --flag, -x)
# before the `commit` subcommand. Avoid matching `git committed`, `git
# commit-graph`, or `commit` appearing as an argument to another subcommand.
_segz=$cmd
_segz=${_segz//&&/$'\n'}
_segz=${_segz//||/$'\n'}
_segz=${_segz//;/$'\n'}

is_commit=0
GIT_C_PATH=""
while IFS= read -r seg; do
  [ -z "$seg" ] && continue
  # Trim leading whitespace.
  seg=${seg#"${seg%%[![:space:]]*}"}
  # Strip leading `VAR=value` env-prefix tokens (unquoted values).
  while echo "$seg" | grep -qE '^[A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]'; do
    seg=$(echo "$seg" | sed -E 's/^[A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+//')
  done
  # Strip an optional leading `command ` builtin prefix.
  seg=$(echo "$seg" | sed -E 's/^command[[:space:]]+//')
  # Is this `git … commit` (allowing global flags before the subcommand)?
  if echo "$seg" | grep -qE '^git([[:space:]]+(-C[[:space:]]+[^[:space:]]+|-c[[:space:]]+[^[:space:]]+|--[A-Za-z0-9][A-Za-z0-9-]*|-[A-Za-z]))*[[:space:]]+commit([[:space:]]|$)'; then
    is_commit=1
    # If a `-C <path>` global flag is present, remember it for repo detection.
    cpath=$(echo "$seg" | sed -nE 's/^.*[[:space:]]-C[[:space:]]+([^[:space:]]+).*$/\1/p')
    [ -n "$cpath" ] && GIT_C_PATH="$cpath"
    break
  fi
done <<< "$_segz"

[ "$is_commit" = "1" ] || exit 0

# ── 3. Locate repo root ─────────────────────────────────────────
# Honour `git -C <path>` (resolved against the hook cwd) so the checks run
# against the repo the commit actually targets, not the hook's cwd.
if [ -n "$GIT_C_PATH" ]; then
  case "$GIT_C_PATH" in
    /*) _cdir="$GIT_C_PATH" ;;
    *)  _cdir="$HOOK_CWD/$GIT_C_PATH" ;;
  esac
  REPO=$(git -C "$_cdir" rev-parse --show-toplevel 2>/dev/null || true)
else
  REPO=$(git rev-parse --show-toplevel 2>/dev/null || true)
fi

repo_name=""
[ -n "$REPO" ] && repo_name=$(basename "$REPO")

# ── 4. Escape hatch (bypass) ────────────────────────────────────
# Recognise SKIP_COMMIT_TEST_GATE=1 as an inline command token OR in the
# hook's own env — a command-line env prefix never reaches the hook process
# env, so both must be checked. Record every bypass for retrospective audit
# (@retrospective-agent flags an un-corrected bypass as a violation pattern).
bypass=0
[ "${SKIP_COMMIT_TEST_GATE:-0}" = "1" ] && bypass=1
if echo "$cmd" | grep -qE '(^|[[:space:]])SKIP_COMMIT_TEST_GATE=1([[:space:]]|$)'; then
  bypass=1
fi
if [ "$bypass" = "1" ]; then
  # Resolve the metrics dir: swarmery model first (AGENT_PROJECT → sibling
  # workspace), else $CLAUDE_PROJECT_DIR / walk-up legacy .claude-workspace.
  _mdir=""
  if [ -n "${AGENT_PROJECT:-}" ]; then
    _mdir="${AGENT_WORKSPACE_ROOT:-/Volumes/Work/swarmery-workspace}/${AGENT_PROJECT}/workspace/metrics"
  else
    metrics_base="${CLAUDE_PROJECT_DIR:-}"
    if [ -z "$metrics_base" ]; then
      _d="$HOOK_CWD"
      while [ -n "$_d" ] && [ "$_d" != "/" ]; do
        if [ -d "$_d/.claude-workspace" ]; then metrics_base="$_d"; break; fi
        _d=$(dirname "$_d")
      done
    fi
    [ -n "$metrics_base" ] && _mdir="$metrics_base/.claude-workspace/metrics"
  fi
  if [ -n "$_mdir" ]; then
    _ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    if mkdir -p "$_mdir" 2>/dev/null; then
      if [ -n "$repo_name" ]; then
        jq -cn --arg ts "$_ts" --arg cwd "$HOOK_CWD" --arg repo "$repo_name" --arg cmd "${cmd:0:200}" \
          '{ts:$ts,cwd:$cwd,repo:$repo,cmd:$cmd}' >> "$_mdir/gate-bypasses.jsonl" 2>/dev/null || true
      else
        jq -cn --arg ts "$_ts" --arg cwd "$HOOK_CWD" --arg cmd "${cmd:0:200}" \
          '{ts:$ts,cwd:$cwd,cmd:$cmd}' >> "$_mdir/gate-bypasses.jsonl" 2>/dev/null || true
      fi
    fi
  fi
  echo "🟡 pre-commit-test-gate: BYPASSED (SKIP_COMMIT_TEST_GATE=1) — logged to .claude-workspace/metrics/gate-bypasses.jsonl"
  _emit_continue
  exit 0
fi

# ── 5. Require a repo ───────────────────────────────────────────
if [ -z "$REPO" ]; then
  echo "🟡 pre-commit-test-gate: not in a git repo; skipping"
  _emit_continue
  exit 0
fi

cd "$REPO"

# ── 6. Get staged file list ─────────────────────────────────────
# Note: `git commit -a` stages tracked changes implicitly. For accuracy we
# look at BOTH the index AND tracked-modified, treating both as "in scope".
staged=$(git diff --cached --name-only 2>/dev/null)
if echo "$cmd" | grep -qE '(^|[[:space:]])(-a|--all)([[:space:]]|$)'; then
  staged="$staged"$'\n'"$(git diff --name-only 2>/dev/null)"
fi
staged=$(printf '%s\n' "$staged" | sort -u | grep -v '^$' || true)

if [ -z "$staged" ]; then
  echo "🟡 pre-commit-test-gate: nothing staged; skipping"
  _emit_continue
  exit 0
fi

# Special case for `--amend` with no new staged content (message-only rewrite)
if echo "$cmd" | grep -qE '(^|[[:space:]])--amend([[:space:]]|$)' && [ -z "$(git diff --cached --name-only 2>/dev/null)" ]; then
  echo "🟡 pre-commit-test-gate: --amend with no new staged content; skipping"
  _emit_continue
  exit 0
fi

# ── 6a. Cross-tier contract-drift WARNING (warn-mode; NEVER blocks) ──
# Staging wire-format source paths (websocket/telemetry handlers, shared
# payload types) in one tier without touching a shared contract doc is the
# classic silent cross-tier drift when two tiers evolve a payload manually
# with no generated schema. Detection is by FILE PATTERN — no repo names:
# any staged non-test source file whose path names websocket/telemetry
# wire-format code counts as a hit, unless a contract doc is staged in the
# same change set or the commit message carries a `contract-waiver:`.
# Reuses the $staged list already computed above; kept cheap.
CONTRACT_MSG=""
contract_hit=$(echo "$staged" | grep -iE '(^|/)(websocket|telemetry)s?/|(^|/)[^/]*(websocket|telemetry)[^/]*\.(ts|tsx|py)$' | grep -viE '(^|/)(test|tests|__tests__|spec)/|\.(test|spec)\.' || true)
contract_doc_staged=$(echo "$staged" | grep -iE '(^|/)contracts?/|contract[^/]*\.(md|ya?ml|json)$' || true)
if [ -n "$contract_hit" ] && [ -z "$contract_doc_staged" ] && ! echo "$cmd" | grep -qi 'contract-waiver:'; then
  _hit_csv=${contract_hit//$'\n'/, }
  CONTRACT_MSG="pre-commit-test-gate: cross-tier wire-format paths are staged (${_hit_csv}) without a contract doc update. If another tier consumes this payload with no generated/shared schema, update the shared contract doc in this same change set, or add 'contract-waiver: <reason>' to the commit message. (warning only — commit not blocked)"
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

# ── 7. Next.js / TypeScript stack ───────────────────────────────
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

# ── 8. Python stack ─────────────────────────────────────────────
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

# ── 9. Helm chart stack ─────────────────────────────────────────
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

# ── 9a. Terraform stack ─────────────────────────────────────────
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

# ── 9b. Shell scripts (any repo) ────────────────────────────────
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

# ── 9c. Flyway-style SQL migration naming ───────────────────────
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

# ── 9d. YAML syntax (non-Helm yaml) ─────────────────────────────
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

# ── 9e. SECRET-BEARING file guard (universal) ───────────────────
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

# ── 10. Verdict ─────────────────────────────────────────────────
if [ $ran_any -eq 0 ]; then
  echo "  • no recognized stack in staged files; allowing commit"
  _emit_continue "$CONTRACT_MSG"
  exit 0
fi

if [ ${#failed[@]} -gt 0 ]; then
  # All of this lands on stderr (fd 1 → fd 2) so the model receives the block
  # reason — an exit-2 hook's stdout is discarded by Claude Code.
  echo ""
  echo "🚫 BLOCKED: pre-commit-test-gate failed (${failed[*]})"
  echo ""
  echo "Fix the failure before committing. See rules/ALWAYS.md for context."
  echo "Emergency bypass: SKIP_COMMIT_TEST_GATE=1 git commit ..."
  echo "  (Note: --no-verify does NOT bypass this hook; that's intentional.)"
  [ -n "$CONTRACT_MSG" ] && echo "⚠ $CONTRACT_MSG"
  echo ""
  exit 2
fi

echo ""
echo "✅ pre-commit-test-gate PASSED — proceeding to commit"
_emit_continue "$CONTRACT_MSG"
exit 0
