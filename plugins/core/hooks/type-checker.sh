#!/bin/bash
# Type Checker Hook for Claude Code
# Runs TypeScript type checking after file edits

set -e

# Read JSON input from stdin
input=$(cat)

# Extract file path using jq
file_path=$(echo "$input" | jq -r '.tool_input.file_path // empty')

# Exit if no file path
if [ -z "$file_path" ]; then
  exit 0
fi

# Only check TypeScript files
ext="${file_path##*.}"
if [[ "$ext" != "ts" && "$ext" != "tsx" ]]; then
  exit 0
fi

# Determine the package root generically: nearest package.json walking up from the file.
# No hard-coded repo names — works for any project layout.
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

# Run type check
cd "$project_dir"

# Check if project has typecheck script
if [ -f "package.json" ] && grep -q "typecheck\|type-check" package.json; then
  echo "🔍 Running type check in $project_dir..."

  # Run type check and capture output (warn only, don't block)
  if npm run typecheck 2>&1; then
    echo "✅ Type check passed"
  else
    echo "⚠️ Type errors found - please review"
  fi
fi

exit 0

