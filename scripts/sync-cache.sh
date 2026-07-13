#!/usr/bin/env bash
# scripts/sync-cache.sh — sync local plugins/ edits into the Claude Code plugin cache.
# Runs automatically via .git/hooks/post-commit whenever plugins/** changes.
# Can also be run manually: bash scripts/sync-cache.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PLUGINS_DIR="$REPO_ROOT/plugins"
CACHE_BASE="${CLAUDE_PLUGIN_CACHE:-$HOME/.claude/plugins/cache}"
MARKETPLACE="swarmery"

synced=0
skipped=0

for plugin_dir in "$PLUGINS_DIR"/*/; do
  plugin_name="$(basename "$plugin_dir")"
  cache_plugin="$CACHE_BASE/$MARKETPLACE/$plugin_name"

  if [ ! -d "$cache_plugin" ]; then
    skipped=$((skipped + 1))
    continue
  fi

  for version_dir in "$cache_plugin"/*/; do
    [ -d "$version_dir" ] || continue
    rsync -a --delete \
      --exclude=".claude-plugin/" \
      "$plugin_dir/" "$version_dir/"
    echo "  ✓ $plugin_name → cache/$MARKETPLACE/$plugin_name/$(basename "$version_dir")"
    synced=$((synced + 1))
  done
done

echo "sync-cache: $synced dir(s) updated, $skipped plugin(s) not in cache (not installed)"
