#!/bin/bash
# Code Formatter Hook for Claude Code
# Automatically formats code after file edits based on file type

set -e

# Read JSON input from stdin
input=$(cat)

# Extract file path using jq
file_path=$(echo "$input" | jq -r '.tool_input.file_path // empty')

# Exit if no file path
if [ -z "$file_path" ]; then
  exit 0
fi

# Determine the package root generically: walk up from the file to the nearest
# package.json. No hard-coded repo names — works for any project layout.
project_dir=""
_d="$(dirname "$file_path")"
while [ "$_d" != "/" ] && [ "$_d" != "." ]; do
  if [ -f "$_d/package.json" ]; then project_dir="$_d"; break; fi
  _d="$(dirname "$_d")"
done

# Exit if the file is not inside a JS/TS package
if [ -z "$project_dir" ]; then
  exit 0
fi

# Get file extension
ext="${file_path##*.}"

# Format based on file type
case "$ext" in
  ts|tsx|js|jsx)
    # TypeScript/JavaScript - run prettier and eslint fix
    cd "$project_dir"
    if [ -f "package.json" ] && grep -q "code:fix" package.json; then
      # Run prettier and eslint fix on the specific file
      npx prettier --write "$file_path" 2>/dev/null || true
      npx eslint --fix "$file_path" 2>/dev/null || true
      echo "✅ Formatted: $file_path"
    fi
    ;;

  json)
    # JSON - use prettier
    cd "$project_dir"
    npx prettier --write "$file_path" 2>/dev/null || true
    echo "✅ Formatted JSON: $file_path"
    ;;

  md)
    # Markdown - use prettier
    cd "$project_dir"
    npx prettier --write "$file_path" 2>/dev/null || true
    echo "✅ Formatted Markdown: $file_path"
    ;;

  py)
    # Python - use black if available
    if command -v black &> /dev/null; then
      black "$file_path" 2>/dev/null || true
      echo "✅ Formatted Python: $file_path"
    fi
    ;;

  *)
    # Unknown file type, skip
    exit 0
    ;;
esac

exit 0

