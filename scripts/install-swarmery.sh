#!/bin/bash
# swarmery install — clone (if needed), build, and launch the control plane.
#
#   # from an existing checkout:
#   bash scripts/install-swarmery.sh
#
#   # fetch-and-run from anywhere. PREFER the download-then-inspect form so you
#   # read what you execute (piping a remote script into a shell runs whatever
#   # the server returns, unseen):
#   url=https://raw.githubusercontent.com/atretyak1985/swarmery/main/scripts/install-swarmery.sh
#   curl -fsSL "$url" -o install-swarmery.sh && less install-swarmery.sh && bash install-swarmery.sh
#
# What it does: verify prerequisites (git, Go, Node) → locate or clone the repo
# → `make build` the single embedded binary → print how to serve it. It does NOT
# start a background service unless you pass --serve (foreground) — installing a
# launchd auto-start service is a separate, macOS-only `swarmery install`.
set -euo pipefail

REPO_SLUG="atretyak1985/swarmery"
REPO_URL="https://github.com/${REPO_SLUG}.git"
CLONE_DIR="${SWARMERY_SRC:-$HOME/.swarmery/src}"
DO_SERVE=0

for arg in "$@"; do
  case "$arg" in
    --serve) DO_SERVE=1 ;;
    -h|--help)
      echo "usage: install-swarmery.sh [--serve]"
      echo "  --serve   run './swarmery serve' in the foreground after building"
      exit 0 ;;
    *) echo "✗ unknown flag: $arg"; exit 1 ;;
  esac
done

# ── prerequisites ──────────────────────────────────────────────────
miss=0
for bin in git go node npm; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "✗ missing prerequisite: $bin"
    miss=1
  fi
done
if [ "$miss" -ne 0 ]; then
  echo ""
  echo "Install the missing tools first:"
  echo "  • Go    ≥ 1.25   https://go.dev/dl/   (older Go auto-downloads the pinned toolchain)"
  echo "  • Node  ≥ 22     https://nodejs.org/   (for the Vite web build)"
  echo "  • git"
  exit 1
fi

# ── locate an existing checkout, else clone ────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
if [ -f "${SCRIPT_DIR}/../.claude-plugin/marketplace.json" ]; then
  REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
  echo "• using existing checkout: ${REPO_DIR}"
elif [ -f "${CLONE_DIR}/.claude-plugin/marketplace.json" ]; then
  REPO_DIR="${CLONE_DIR}"
  echo "• updating existing clone: ${REPO_DIR}"
  git -C "${REPO_DIR}" pull --ff-only || echo "  (pull skipped — local changes present)"
else
  echo "• cloning ${REPO_URL} → ${CLONE_DIR}"
  mkdir -p "$(dirname "${CLONE_DIR}")"
  git clone --depth 1 "${REPO_URL}" "${CLONE_DIR}"
  REPO_DIR="${CLONE_DIR}"
fi

# ── build the embedded single binary ───────────────────────────────
echo "• building control plane (make build) …"
make -C "${REPO_DIR}/tools/swarmery" build
BIN="${REPO_DIR}/tools/swarmery/swarmery"
echo "✓ built ${BIN}"

# ── run or print next steps ────────────────────────────────────────
PORT="${SWARMERY_PORT:-7777}"
if [ "$DO_SERVE" -eq 1 ]; then
  echo "• starting: ${BIN} serve  (http://localhost:${PORT})"
  exec "${BIN}" serve
fi

echo ""
echo "Next:"
echo "  ${BIN} serve                 # listens on :${PORT}"
echo "  # health check (in another shell):"
echo "  curl -s http://localhost:${PORT}/api/health && open http://localhost:${PORT}"
echo ""
echo "  # bootstrap a consumer project from its root:"
echo "  cd /path/to/your/project && ${BIN} onboard <project-slug> [pack ...]"
echo ""
echo "  # macOS only — install as a launchd auto-start service:"
echo "  ${BIN} install"
