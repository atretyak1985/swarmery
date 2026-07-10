#!/bin/bash
# Code Formatter Hook for Claude Code
# Automatically formats code after file edits based on file type

set -e

# Read JSON input from stdin
input=$(cat)

# Malformed/non-JSON stdin: nothing to track — never break the tool call
# (non-blocking contract; every jq below assumes valid JSON).
if ! printf '%s' "$input" | jq -e . >/dev/null 2>&1; then
  exit 0
fi

# Extract file path using jq
file_path=$(echo "$input" | jq -r '.tool_input.file_path // empty' 2>/dev/null || true)

# Exit if no file path
if [ -z "$file_path" ]; then
  exit 0
fi

# Determine the project root generically: walk up from the file to the nearest
# project marker (package.json or pyproject.toml). No hard-coded repo names —
# works for any project layout. Each formatter runs from $project_dir so it
# picks up that project's own config (eslint flat config, black/isort in
# pyproject.toml).
project_dir=""
_d="$(dirname "$file_path")"
while [ "$_d" != "/" ] && [ "$_d" != "." ]; do
  if [ -f "$_d/package.json" ] || [ -f "$_d/pyproject.toml" ]; then project_dir="$_d"; break; fi
  _d="$(dirname "$_d")"
done

# Exit if the file is not inside a recognized project
if [ -z "$project_dir" ]; then
  exit 0
fi

# Get file extension
ext="${file_path##*.}"

# Format based on file type
case "$ext" in
  ts|tsx|js|jsx)
    # TypeScript/JavaScript - run prettier and eslint fix
    cd "$project_dir" 2>/dev/null || exit 0
    if [ -f "package.json" ] && grep -q "code:fix" package.json; then
      # Run prettier and eslint fix on the specific file
      npx prettier --write "$file_path" 2>/dev/null || true
      npx eslint --fix "$file_path" 2>/dev/null || true
      echo "✅ Formatted: $file_path"
    fi
    ;;

  json)
    # JSON - use prettier
    cd "$project_dir" 2>/dev/null || exit 0
    [ -f "package.json" ] || exit 0
    npx prettier --write "$file_path" 2>/dev/null || true
    echo "✅ Formatted JSON: $file_path"
    ;;

  md)
    # Markdown - use prettier
    cd "$project_dir" 2>/dev/null || exit 0
    [ -f "package.json" ] || exit 0
    npx prettier --write "$file_path" 2>/dev/null || true
    echo "✅ Formatted Markdown: $file_path"
    ;;

  py)
    # Python — black then isort on this single file, preferring the project
    # venv binaries, then PATH, else skip silently.
    cd "$project_dir" 2>/dev/null || exit 0
    formatted=0
    if [ -x ".venv/bin/black" ]; then
      .venv/bin/black "$file_path" 2>/dev/null || true
      formatted=1
    elif command -v black >/dev/null 2>&1; then
      black "$file_path" 2>/dev/null || true
      formatted=1
    fi
    if [ -x ".venv/bin/isort" ]; then
      .venv/bin/isort "$file_path" 2>/dev/null || true
      formatted=1
    elif command -v isort >/dev/null 2>&1; then
      isort "$file_path" 2>/dev/null || true
      formatted=1
    fi
    if [ "$formatted" -eq 1 ]; then
      echo "✅ Formatted Python: $file_path"
    fi
    ;;

  *)
    # Nothing to format for this file type.
    exit 0
    ;;
esac

exit 0
