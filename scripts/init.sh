#!/bin/bash
# swarmery init — bootstrap a new consumer project in one command.
#
#   bash /Volumes/Work/swarmery/scripts/init.sh <project-slug> [pack ...]
#   # or from anywhere, once the repo is on GitHub:
#   bash <(curl -sL https://raw.githubusercontent.com/atretyak1985/swarmery/main/scripts/init.sh) my-project web-pack
#
# Run it FROM THE PROJECT ROOT. Creates:
#   .claude/settings.json   — marketplace + enabled plugins + env (skipped if it exists)
#   .claude/project.json    — flavor config skeleton to fill in (skipped if it exists)
#   <workspace>/<slug>/…    — the workspace namespace
# Then: start a fresh Claude Code session and accept the marketplace trust prompt.
set -euo pipefail

SLUG="${1:-}"
shift || true
PACKS=("$@")   # e.g. web-pack iot-pack uav-pack (core is always on)

MARKETPLACE_REPO="atretyak1985/swarmery"
WS_ROOT="${SWARMERY_WORKSPACE_ROOT:-/Volumes/Work/swarmery-workspace}"

if [ -z "$SLUG" ]; then
  echo "usage: init.sh <project-slug> [pack ...]        packs: uav-pack | iot-pack | web-pack"
  exit 1
fi
case "$SLUG" in
  *[!a-z0-9-]*) echo "✗ slug must be kebab-case ([a-z0-9-]): $SLUG"; exit 1;;
esac
for p in "${PACKS[@]:-}"; do
  case "$p" in uav-pack|iot-pack|web-pack|"") ;; *) echo "✗ unknown pack: $p"; exit 1;; esac
done

PROJECT_DIR="$(pwd)"
mkdir -p .claude

# ── settings.json ──────────────────────────────────────────────────
if [ -f .claude/settings.json ]; then
  echo "• .claude/settings.json exists — not touching it (merge manually if needed)"
else
  plugins="    \"core@swarmery\": true"
  for p in "${PACKS[@]:-}"; do
    [ -n "$p" ] && plugins="${plugins},
    \"${p}@swarmery\": true"
  done
  cat > .claude/settings.json <<EOF
{
  "extraKnownMarketplaces": {
    "swarmery": {
      "source": { "source": "github", "repo": "${MARKETPLACE_REPO}" }
    }
  },
  "enabledPlugins": {
${plugins}
  },
  "env": {
    "AGENT_PROJECT": "${SLUG}",
    "AGENT_WORKSPACE_ROOT": "${WS_ROOT}"
  },
  "permissions": {
    "deny": [
      "Read(./.env)", "Read(./.env.*)", "Read(./secrets/**)",
      "Edit(./.env)", "Edit(./.env.*)",
      "Write(./.env)", "Write(./.env.*)"
    ],
    "additionalDirectories": ["${WS_ROOT}"]
  }
}
EOF
  echo "✓ .claude/settings.json (core${PACKS:+ + ${PACKS[*]}})"
fi

# ── project.json skeleton ──────────────────────────────────────────
if [ -f .claude/project.json ]; then
  echo "• .claude/project.json exists — not touching it"
else
  packs_json=""
  for p in "${PACKS[@]:-}"; do
    [ -n "$p" ] && packs_json="${packs_json:+${packs_json}, }\"${p}\""
  done
  cat > .claude/project.json <<EOF
{
  "name": "${SLUG}",
  "displayName": "${SLUG}",
  "codePath": "${PROJECT_DIR}",
  "mainApp": "TODO-main-app",
  "apps": [],
  "repos": [],
  "domainTerms": { "product": "TODO: one line about the product" },
  "stack": {},
  "commitScopes": [],
  "enabledPacks": [${packs_json}]
}
EOF
  echo "✓ .claude/project.json — FILL IN the TODO fields (agents read this at runtime)"
fi

# ── workspace namespace ────────────────────────────────────────────
mkdir -p "${WS_ROOT}/${SLUG}/wiki" \
         "${WS_ROOT}/${SLUG}/workspace"/{working,archive,plans,specs,sessions,logs,metrics}
echo "✓ workspace: ${WS_ROOT}/${SLUG}/"

# ── validate if node is around ─────────────────────────────────────
if command -v node >/dev/null 2>&1; then
  node -e "JSON.parse(require('fs').readFileSync('.claude/settings.json'));JSON.parse(require('fs').readFileSync('.claude/project.json'))" \
    && echo "✓ JSON valid"
fi

echo ""
echo "Next: open a FRESH Claude Code session in ${PROJECT_DIR}"
echo "      → accept the 'swarmery' marketplace trust prompt → plugins install."
echo "      Fill in .claude/project.json TODOs so agents know your repos/stack."
