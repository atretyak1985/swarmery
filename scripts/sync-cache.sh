#!/usr/bin/env bash
# scripts/sync-cache.sh — sync local plugins/ edits into the Claude Code plugin cache.
# Runs automatically via .git/hooks/post-commit whenever plugins/** changes.
# Can also be run manually: bash scripts/sync-cache.sh
#
# WHERE IT WRITES. Only into the version directory a config dir has actually
# INSTALLED, as recorded in <config-dir>/plugins/installed_plugins.json — and
# into every config dir on the machine ($HOME/.claude plus every $HOME/.claude-*
# account dir), because a project bound to another Claude account loads its
# packs from that account's cache, not from ~/.claude.
#
# WHY NOT EVERY VERSION DIR. The old behaviour rsynced the working tree into
# every cached version under ~/.claude only. That put 1.4.0 content into a dir
# named 1.2.0, so sessions on the default account ran the new skill under the
# old version label while sessions on another account kept the real 1.2.0 —
# the same pack behaving two ways on one machine, with nothing in the install
# records to show it. Writing only to the installed path keeps the label and
# the content in agreement, and the per-target line below says out loud when
# the installed version is not the source version.
#
# Overrides (tests): SWARMERY_PLUGINS_DIR (source tree), SWARMERY_CONFIG_DIRS
# (space-separated config dirs instead of the $HOME glob).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PLUGINS_DIR="${SWARMERY_PLUGINS_DIR:-$REPO_ROOT/plugins}"
MARKETPLACE="swarmery"

command -v node >/dev/null 2>&1 || { echo "sync-cache: node is required to read installed_plugins.json" >&2; exit 1; }

config_dirs() {
  if [ -n "${SWARMERY_CONFIG_DIRS:-}" ]; then
    # shellcheck disable=SC2086 # intentional word splitting of the override
    printf '%s\n' $SWARMERY_CONFIG_DIRS
    return
  fi
  local d
  for d in "$HOME/.claude" "$HOME"/.claude-*; do
    [ -d "$d" ] && printf '%s\n' "$d"
  done
}

# installed_targets <config-dir> → lines "plugin<TAB>version<TAB>installPath",
# one per distinct install path recorded for a *@swarmery plugin.
installed_targets() {
  local file="$1/plugins/installed_plugins.json"
  [ -f "$file" ] || return 0
  node -e '
    const fs = require("fs");
    let root;
    try { root = JSON.parse(fs.readFileSync(process.argv[1], "utf8")); } catch { process.exit(0); }
    const plugins = root && typeof root.plugins === "object" && root.plugins ? root.plugins : root;
    const seen = new Set();
    for (const [id, entries] of Object.entries(plugins || {})) {
      const at = id.lastIndexOf("@");
      if (at < 0 || id.slice(at + 1) !== process.argv[2]) continue;
      const name = id.slice(0, at);
      for (const e of Array.isArray(entries) ? entries : [entries]) {
        if (!e || typeof e.installPath !== "string" || !e.installPath) continue;
        const key = name + "\t" + e.installPath;
        if (seen.has(key)) continue;
        seen.add(key);
        process.stdout.write(name + "\t" + (e.version || "?") + "\t" + e.installPath + "\n");
      }
    }
  ' "$file" "$MARKETPLACE"
}

source_version() {
  node -e 'try{process.stdout.write(String(JSON.parse(require("fs").readFileSync(process.argv[1],"utf8")).version||"?"))}catch{process.stdout.write("?")}' \
    "$PLUGINS_DIR/$1/.claude-plugin/plugin.json"
}

synced=0
drift=0
skipped_dirs=0

while IFS= read -r cfg; do
  [ -n "$cfg" ] || continue
  targets="$(installed_targets "$cfg")"
  if [ -z "$targets" ]; then
    skipped_dirs=$((skipped_dirs + 1))
    continue
  fi
  while IFS=$'\t' read -r plugin installed_version install_path; do
    [ -n "$plugin" ] || continue
    src="$PLUGINS_DIR/$plugin"
    if [ ! -d "$src" ]; then
      continue # a pack this checkout does not ship (removed upstream, or foreign)
    fi
    if [ ! -d "$install_path" ]; then
      echo "  ! $plugin → $install_path is recorded but missing on disk — skipped" >&2
      continue
    fi
    rsync -a --delete --exclude=".claude-plugin/" "$src/" "$install_path/"
    src_version="$(source_version "$plugin")"
    if [ "$installed_version" != "$src_version" ]; then
      echo "  ⚠ $plugin → ${install_path#"$HOME"/} (installed $installed_version, source $src_version — content is now $src_version under the $installed_version label; run \`claude plugin update $plugin@$MARKETPLACE\` there)"
      drift=$((drift + 1))
    else
      echo "  ✓ $plugin → ${install_path#"$HOME"/} ($installed_version)"
    fi
    synced=$((synced + 1))
  done <<< "$targets"
done < <(config_dirs)

echo "sync-cache: $synced installed dir(s) updated across config dirs, $drift with a version drift, $skipped_dirs config dir(s) without $MARKETPLACE installs"
