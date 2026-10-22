#!/bin/bash
# Protect Sensitive Files Hook for Claude Code
# Blocks edits to sensitive files and directories
#
# FAIL CLOSED. This guard stands between an agent's Edit / Write / MultiEdit /
# NotebookEdit and the paths below, so when it cannot read its input it blocks
# (exit 2) rather than allows: jq missing, or stdin that is not a JSON object.
#
# EVERY PATH, EVERY SPELLING. One payload can name several paths (file_path,
# path, notebook_path, edits[].file_path), and one path can reach a protected
# file under another spelling: through a symlinked directory or file, or in a
# different letter case on a case-insensitive file system. The dispatcher below
# expands a payload into every candidate — each path as written, plus where the
# write lands once symlinks are resolved — and runs the single-path check once
# per candidate (PSF_SINGLE=1 re-invocation of this file). Any candidate blocked
# blocks the call. Every name comparison in the single-path check ignores case.
# Covered by scripts/tests/protect-sensitive-files.test.sh.

set -e

# Read JSON input from stdin
input=$(cat)

if ! command -v jq >/dev/null 2>&1; then
  echo "protect-sensitive-files: jq missing — blocking" >&2
  exit 2
fi
if ! printf '%s' "$input" | jq -e 'type == "object"' >/dev/null 2>&1; then
  echo "protect-sensitive-files: hook input is not a JSON object — blocking" >&2
  exit 2
fi

# existing_dir <path> — the deepest directory of <path> that exists.
existing_dir() {
  local p="$1"
  while [ ! -d "$p" ]; do p=$(dirname "$p"); done
  printf '%s\n' "$p"
}

# physical <path> — <path> with its existing directory prefix resolved through
# every symlink (cd -P / pwd -P: stock macOS has no readlink -f) and the part
# that does not exist yet appended as written.
physical() {
  local dir rest phys
  dir=$(existing_dir "$1")
  rest="${1#"${dir%/}"}"
  phys=$(cd -P "$dir" 2>/dev/null && pwd -P) || phys="$dir"
  printf '%s%s\n' "${phys%/}" "$rest"
}

# ── Dispatcher: payload → every candidate path → one check each ───
if [ -z "${PSF_SINGLE:-}" ]; then
  hook_cwd=$(printf '%s' "$input" | jq -r '.cwd // empty')
  [ -n "$hook_cwd" ] || hook_cwd="$PWD"
  candidates=()
  add_candidate() {
    local c
    for c in ${candidates[@]+"${candidates[@]}"}; do [ "$c" = "$1" ] && return 0; done
    candidates+=("$1")
  }
  while IFS= read -r -d '' p; do
    add_candidate "$p"
    case "$p" in /*) cur="$p" ;; *) cur="${hook_cwd%/}/$p" ;; esac
    hops=0
    while :; do
      land=$(physical "$cur")
      case "$land" in
        */../*|*/..)
          echo "🚫 BLOCKED: path climbs out of a directory that does not exist: $p" >&2
          exit 2 ;;
      esac
      add_candidate "$land"
      [ -L "$land" ] || break
      hops=$((hops + 1))
      if [ "$hops" -gt 8 ]; then
        echo "🚫 BLOCKED: symlink chain too deep: $p" >&2
        exit 2
      fi
      target=$(readlink "$land") || break
      case "$target" in /*) cur="$target" ;; *) cur="$(dirname "$land")/$target" ;; esac
    done
  done < <(printf '%s' "$input" | jq -j '
    [ .tool_input.file_path, .tool_input.path, .tool_input.notebook_path,
      ((.tool_input.edits // []) | if type == "array" then .[] | objects | .file_path else empty end) ]
    | .[] | strings | select(. != "") | . + "\u0000"')
  for c in ${candidates[@]+"${candidates[@]}"}; do
    rc=0
    jq -nc --arg p "$c" --arg cwd "$hook_cwd" '{cwd: $cwd, tool_input: {file_path: $p}}' \
      | PSF_SINGLE=1 bash "${BASH_SOURCE[0]}" || rc=$?
    [ "$rc" -eq 0 ] || exit 2
  done
  exit 0
fi

# ── Single-path check (PSF_SINGLE=1): exactly one tool_input.file_path ──
file_path=$(echo "$input" | jq -r '.tool_input.file_path // .tool_input.path // empty')

# Exit if no file path
if [ -z "$file_path" ]; then
  exit 0
fi

# Every name comparison below ignores case: .GIT/ is .git/ on a case-insensitive
# file system. Messages keep the path as written.
lc_path=$(printf '%s' "$file_path" | tr '[:upper:]' '[:lower:]')
lc_base=$(basename "$lc_path")

# Protected directory names — blocked only when they appear as a full
# path segment, so e.g. skills/docker-build/ is NOT mistaken for build/.
protected_dirs=(
  ".git"
  "node_modules"
  "dist"
  "build"
  ".next"
  "coverage"
)

# Protected files — lock files match by exact basename.
protected_files=(
  "package-lock.json"
  "yarn.lock"
  "pnpm-lock.yaml"
)

base_name=$(basename "$file_path")

# Block edits to files inside a protected directory (path-segment match,
# not substring — anchored by a leading "/" or the start of the path).
# Block messages go to STDERR because only STDERR reaches the model.
for dir in "${protected_dirs[@]}"; do
  if [[ "$lc_path" == "$dir/"* || "$lc_path" == *"/$dir/"* ]]; then
    echo "🚫 BLOCKED: Cannot modify file inside protected directory: $file_path" >&2
    echo "Protected directory: $dir/" >&2
    echo "" >&2
    echo "If you need to modify this file, please do it manually." >&2
    exit 2  # Exit code 2 blocks the operation
  fi
done

# Canonicalize a relative file_path against the hook's cwd. Two rules below need
# the absolute form: the lock-file temp-dir exemption, and the root-artifact
# guard — where `dirname` of a relative path (e.g. "PLAN-x.md" → ".") would
# otherwise never equal an absolute workspace_root and silently bypass it. The
# hook JSON carries `cwd`; if it is absent, fall back to $PWD.
abs_file_path="$file_path"
case "$file_path" in
  /*) ;;  # already absolute — use as-is
  *)
    hook_cwd=$(echo "$input" | jq -r '.cwd // empty')
    [ -n "$hook_cwd" ] || hook_cwd="$PWD"
    abs_file_path="${hook_cwd%/}/$file_path"
    ;;
esac

# (existing_dir and physical are defined above the dispatcher.)

# A lock file in a scratch copy under the OS temp dir is not the project's lock
# file. It is judged by where the write would LAND: a path with a ".." segment
# never qualifies; symlinks in the existing part of the path are resolved first
# (/tmp/link → /repo makes /tmp/link/package-lock.json the repo's); a symlinked
# lock file never qualifies; and nothing inside a git work tree qualifies — a
# checkout or worktree under the temp dir (agent scratchpads live there) is a
# real project. Resolved only for a lock-file basename, the one rule that reads it.
under_os_tmp=0
is_lock=0
for f in "${protected_files[@]}"; do
  [[ "$lc_base" == "$f" ]] && is_lock=1
done
if [ "$is_lock" -eq 1 ] && [ ! -L "$abs_file_path" ]; then
  case "$abs_file_path" in
    */../*|*/..) ;;
    *)
      land=$(physical "$abs_file_path")
      tmp_roots=(/tmp /private/tmp "$(physical /tmp)")
      tmp_root="${TMPDIR%/}"
      if [ -n "$tmp_root" ] && [ "$tmp_root" != "/" ]; then
        tmp_roots+=("$tmp_root" "/private$tmp_root" "$(physical "$tmp_root")")
      fi
      for root in "${tmp_roots[@]}"; do
        # A root that resolves to the filesystem root must not make everything "temp".
        [ -n "$root" ] && [ "$root" != "/" ] || continue
        case "$land" in "$root"/*) under_os_tmp=1 ;; esac
      done
      if [ "$under_os_tmp" -eq 1 ] && command -v git >/dev/null 2>&1 &&
        [ "$(env -u GIT_DIR -u GIT_WORK_TREE git -C "$(existing_dir "$land")" \
          rev-parse --is-inside-work-tree 2>/dev/null)" = "true" ]; then
        under_os_tmp=0
      fi
      ;;
  esac
fi

# Block edits to protected lock files (exact basename match) — except under the
# OS temp dir (above). Only this loop reads under_os_tmp: the protected-directory
# loop, the credential block and every later rule apply there as everywhere else.
if [ "$under_os_tmp" -eq 0 ]; then
  for f in "${protected_files[@]}"; do
    if [[ "$lc_base" == "$f" ]]; then
      echo "🚫 BLOCKED: Cannot modify protected file: $file_path" >&2
      echo "Protected pattern: $f" >&2
      echo "" >&2
      echo "If you need to modify this file, please do it manually." >&2
      exit 2  # Exit code 2 blocks the operation
    fi
  done
fi

# Block edits to credential material. Extended 2026-08-24. Historical note:
# the retired read-before-write.sh hook echoed a refused file's CONTENTS to
# stderr and deferred to this list to decide what must never be echoed; the
# hook is gone (the check is native now), but this list remains the single
# place deciding what is both un-editable and un-quotable.
if [[ "$lc_base" == *.pem || "$lc_base" == *.key || "$lc_base" == *.p12 || \
      "$lc_base" == *.pfx || "$lc_base" == *.jks || "$lc_base" == *.keystore || \
      "$lc_base" == id_rsa* || "$lc_base" == id_ed25519* || "$lc_base" == id_ecdsa* || \
      "$lc_base" == id_dsa* || "$lc_base" == .npmrc || "$lc_base" == .netrc || \
      "$lc_base" == _netrc || "$lc_base" == .pgpass || "$lc_base" == .htpasswd || \
      "$lc_base" == credentials || "$lc_base" == credentials.json || \
      "$lc_base" == service-account*.json || "$lc_base" == kubeconfig || \
      "$lc_base" == .dockercfg || "$lc_base" == .docker.json || \
      "$lc_base" == *.tfvars || "$lc_base" == settings.local.json ]]; then
  echo "🚫 BLOCKED: Cannot modify credential material: $file_path" >&2
  echo "Protected pattern: credential file ($base_name)" >&2
  echo "" >&2
  echo "Private keys, tokens, kubeconfigs and local settings are edited by a human," >&2
  echo "never by an agent — and their contents are never quoted back into a session." >&2
  exit 2
fi

# Template dotenv files document variable NAMES and are committed; they hold no
# secrets. Only a TRAILING suffix counts: .env.example.local stays blocked.
case "$lc_base" in
  .env*.example|.env*.sample|.env*.template) env_template=1 ;;
  *) env_template=0 ;;
esac

# Block edits to any other .env* file (basename prefix match — covers
# .env, .env.local, .env.production, …). Only this rule reads env_template.
if [[ "$env_template" -eq 0 && "$lc_base" == .env* ]]; then
  echo "🚫 BLOCKED: Cannot modify protected file: $file_path" >&2
  echo "Protected pattern: .env*" >&2
  echo "" >&2
  echo "If you need to modify this file, please do it manually." >&2
  exit 2  # Exit code 2 blocks the operation
fi

# Block edits to production / populated values files. Documented in rules/NEVER.md.
# Pattern matches values.prod.yaml, values.prod.yml, *.populated.yaml, *.populated.yml.
if [[ "$lc_base" == values.prod.yaml || "$lc_base" == values.prod.yml ]]; then
  echo "🚫 BLOCKED: Cannot modify production values file: $file_path" >&2
  echo "Protected pattern: values.prod.{yaml,yml}" >&2
  echo "" >&2
  echo "Production values changes require specialist review with the sre-operations skill" >&2
  echo "and explicit user approval per rules/ASK.md." >&2
  echo "If this is a legitimate change, edit manually outside of an agent session." >&2
  exit 2
fi

if [[ "$lc_base" == *.populated.yaml || "$lc_base" == *.populated.yml ]]; then
  echo "🚫 BLOCKED: Cannot modify populated (rendered-with-secrets) values file: $file_path" >&2
  echo "Protected pattern: *.populated.{yaml,yml}" >&2
  echo "" >&2
  echo "These files are generated by secret-bootstrap scripts and contain rendered" >&2
  echo "secrets. They are gitignored and must never be hand-edited." >&2
  exit 2
fi

# Block edits to generated router/network configs (may contain private keys + PSKs).
if [[ "$lc_path" == *"/output/"* && "$lc_base" == *.rsc ]]; then
  echo "🚫 BLOCKED: Cannot modify generated network config: $file_path" >&2
  echo "Protected pattern: */output/*.rsc" >&2
  echo "" >&2
  echo "These files are generated by scripts/generate-*.sh and contain secrets." >&2
  echo "Re-run the generator script instead of hand-editing." >&2
  exit 2
fi

# Block edits to Terraform state files.
if [[ "$lc_base" == terraform.tfstate || "$lc_base" == terraform.tfstate.backup || "$lc_base" == *.tfstate || "$lc_base" == *.tfstate.backup ]]; then
  echo "🚫 BLOCKED: Cannot modify Terraform state: $file_path" >&2
  echo "Protected pattern: *.tfstate(.backup)?" >&2
  echo "" >&2
  echo "Live infrastructure state is managed by 'terraform apply'; never edit by hand." >&2
  exit 2
fi

# Warn on hand-edits to generated wiki pages (agents/docs/generated/, also reachable as
# .claude/docs/generated/ via the symlink). Regeneration-only zone — same rule as
# agents-index.json. README.md at the tree root stays hand-editable.
# WARN-MODE BURN-IN (gate-hardening rule 1, added 2026-07-06): logs + exit 0 for ~1 week;
# flip to `exit 2` on/after 2026-07-13 if no false positives in the metrics log.
if [[ ("$file_path" == *"agents/docs/generated/"* || "$file_path" == *".claude/docs/generated/"*) && "$base_name" != "README.md" ]]; then
  echo "⚠️  WARN (enforce from 2026-07-13): hand-editing a generated wiki page: $file_path" >&2
  echo "This tree is regeneration-only (see agents/docs/generated/README.md)." >&2
  echo "Fix the source code or the generator prompt, then re-run the wiki generator." >&2
  if [ -n "${AGENT_PROJECT:-}" ]; then
    metrics_dir="${AGENT_WORKSPACE_ROOT:-$HOME/swarmery-workspace}/${AGENT_PROJECT}/workspace/metrics"
  else
    metrics_dir="${CLAUDE_PROJECT_DIR:-.}/.claude-workspace/metrics"
  fi
  mkdir -p "$metrics_dir" 2>/dev/null || true
  # jq encodes the file name, so a quote or backslash in it cannot break the
  # line or add fields to the audit record.
  jq -nc --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg file "$file_path" \
    '{ts:$ts,hook:"protect-sensitive-files",stanza:"generated-wiki",mode:"warn",file:$file}' \
    >> "$metrics_dir/gate-bypasses.jsonl" 2>/dev/null || true
  exit 0
fi

# Block loose task/plan artifacts dropped DIRECTLY in the workspace root.
# Task artifacts belong in .claude-workspace/working/YYYY/MM/DD/<slug>/ (rules/ALWAYS.md),
# never loose at the meta-repo root. Only the root level itself is guarded — files nested
# under .claude-workspace/ or inside a repo subdirectory are untouched, and non-artifact
# .md files (CLAUDE.md, README.md, …) stay writable.
#
# The workspace root is the directory that contains .claude-workspace. Prefer
# $CLAUDE_PROJECT_DIR; fall back to the file's own parent directory (path heuristic).
# abs_file_path is the cwd-canonicalized path computed above the lock-file loop.
file_dir=$(dirname "$abs_file_path")
if [ -n "$CLAUDE_PROJECT_DIR" ]; then
  workspace_root="${CLAUDE_PROJECT_DIR%/}"
else
  workspace_root="$file_dir"
fi

if [[ "$file_dir" == "$workspace_root" && -d "$workspace_root/.claude-workspace" ]]; then
  # Unquoted regex var so bash 3.2 treats it as an ERE (quoting would force a literal match).
  # Also matches the bare TODO.md (optional second group).
  artifact_re='^(PLAN|AUDIT|TASK|TODO|INVESTIGATION)([-_][A-Za-z0-9._-]*)?\.md$'
  if [[ "$base_name" =~ $artifact_re ]]; then
    echo "🚫 BLOCKED: Cannot create loose task/plan artifact at the workspace root: $file_path" >&2
    echo "Protected pattern: (PLAN|AUDIT|TASK|TODO|INVESTIGATION)*.md directly in the workspace root" >&2
    echo "" >&2
    echo "Task artifacts belong in .claude-workspace/working/YYYY/MM/DD/<slug>/ —" >&2
    echo "create the task dir via scripts/agent-work.sh init." >&2
    exit 2
  fi
fi

# Allow the operation
exit 0
