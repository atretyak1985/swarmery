#!/bin/bash
# swarmery install — download or remove a released binary. No toolchain required.
#
#   # one line:
#   curl -fsSL https://raw.githubusercontent.com/atretyak1985/swarmery/main/scripts/install.sh | bash
#
#   # or read what you execute first (piping a remote script into a shell runs
#   # whatever the server returns, unseen):
#   url=https://raw.githubusercontent.com/atretyak1985/swarmery/main/scripts/install.sh
#   curl -fsSL "$url" -o install.sh && less install.sh && bash install.sh
#
# What it does: detect os/arch → resolve the latest swarmery-v* release (or
# $SWARMERY_VERSION) → download that asset and SHA256SUMS → verify the checksum
# → install into $SWARMERY_INSTALL_DIR (default ~/.local/bin). It builds
# nothing, starts nothing, and installs no service.
#
# Use --uninstall to remove only the installed binary. User data is preserved.
#
# To build from source instead, use scripts/install-swarmery.sh (needs Go+Node).
#
# Env:
#   SWARMERY_INSTALL_DIR   install prefix   (default: $HOME/.local/bin)
#   SWARMERY_VERSION       pin a tag, e.g. swarmery-v0.2.0 (default: latest)
set -euo pipefail

REPO_SLUG="atretyak1985/swarmery"
INSTALL_DIR="${SWARMERY_INSTALL_DIR:-$HOME/.local/bin}"
API="https://api.github.com/repos/${REPO_SLUG}/releases"

for arg in "$@"; do
  case "$arg" in
    -h|--help)
      echo "usage: install.sh [--uninstall]"
      echo "  --uninstall                       remove the installed swarmery binary"
      echo "  SWARMERY_INSTALL_DIR=<dir>       install prefix (default: \$HOME/.local/bin)"
      echo "  SWARMERY_VERSION=swarmery-vX.Y.Z pin a release (default: latest)"
      exit 0 ;;
    --uninstall)
      os="$(uname -s | tr '[:upper:]' '[:lower:]')"
      if [ "$os" = "darwin" ]; then
        if launchctl print "gui/$(id -u)/com.swarmery.agent" >/dev/null 2>&1; then
          echo "✗ swarmery launchd service is installed. Run 'swarmery uninstall' first, then retry." >&2
          exit 1
        fi
      fi
      binary="${INSTALL_DIR}/swarmery"
      if [ -e "$binary" ]; then
        rm -f "$binary"
        echo "✓ removed ${binary}"
      else
        echo "• nothing to remove at ${binary}"
      fi
      echo "  User data was not removed:"
      echo "    database: $HOME/.swarmery/swarmery.db"
      echo "    logs:     $HOME/.swarmery/logs"
      exit 0 ;;
    *) echo "✗ unknown argument: $arg" >&2; exit 1 ;;
  esac
done

command -v curl >/dev/null 2>&1 || { echo "✗ missing prerequisite: curl" >&2; exit 1; }

# ── host target ────────────────────────────────────────────────────
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  darwin|linux) ;;
  msys*|mingw*|cygwin*)
    echo "✗ Windows is not supported. Run this inside WSL2, or build from source:" >&2
    echo "  https://github.com/${REPO_SLUG}#working-on-swarmery-itself" >&2
    exit 1 ;;
  *) echo "✗ unsupported OS: $os (released builds: darwin, linux)" >&2; exit 1 ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "✗ unsupported architecture: $arch (released builds: amd64, arm64)" >&2; exit 1 ;;
esac

# ── resolve the release tag ────────────────────────────────────────
if [ -n "${SWARMERY_VERSION:-}" ]; then
  tag="${SWARMERY_VERSION}"
else
  http_body="$(mktemp)"; http_code=0
  http_code="$(curl -sSL -w '%{http_code}' -o "$http_body" "${API}/latest" || echo 000)"
  if [ "$http_code" = "403" ] || [ "$http_code" = "429" ]; then
    rm -f "$http_body"
    echo "✗ GitHub API rate-limited. Pin a release and retry:" >&2
    echo "  SWARMERY_VERSION=swarmery-v0.2.0 bash install.sh" >&2
    exit 1
  fi
  if [ "$http_code" != "200" ]; then
    rm -f "$http_body"
    echo "✗ could not read ${API}/latest (HTTP ${http_code})" >&2
    exit 1
  fi
  tag="$(tr ',' '\n' < "$http_body" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
  rm -f "$http_body"
fi

# The release workflow names assets swarmery-${GITHUB_REF_NAME#swarmery-}-os-arch,
# i.e. the version KEEPS its leading "v": swarmery-v0.2.0-darwin-arm64. Derive it
# the same way, and refuse anything that does not look like that — a wrong name
# would otherwise show up as a 404 halfway through the download.
version="${tag#swarmery-}"
asset="swarmery-${version}-${os}-${arch}"
case "$asset" in
  swarmery-v[0-9]*) ;;
  *) echo "✗ unexpected release tag: '${tag}' (want swarmery-vX.Y.Z)" >&2; exit 1 ;;
esac

base="https://github.com/${REPO_SLUG}/releases/download/${tag}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "• downloading ${asset} (${tag})"
curl -fSL --progress-bar "${base}/${asset}"   -o "${tmp}/swarmery"
curl -fsSL             "${base}/SHA256SUMS" -o "${tmp}/SHA256SUMS"

# ── verify ─────────────────────────────────────────────────────────
if command -v sha256sum >/dev/null 2>&1; then
  sum_cmd="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  sum_cmd="shasum -a 256"
else
  echo "✗ neither sha256sum nor shasum available — refusing to install unverified" >&2
  exit 1
fi

want="$(sed -n "s/^\([0-9a-f]\{64\}\)[[:space:]][[:space:]]*${asset}\$/\1/p" "${tmp}/SHA256SUMS" | head -n1)"
if [ -z "$want" ]; then
  echo "✗ ${asset} is not listed in SHA256SUMS for ${tag}" >&2
  exit 1
fi
got="$(cd "$tmp" && $sum_cmd swarmery | cut -d' ' -f1)"
if [ "$want" != "$got" ]; then
  echo "✗ checksum mismatch — the download was NOT installed" >&2
  echo "  expected ${want}" >&2
  echo "  got      ${got}" >&2
  exit 1
fi
echo "✓ sha256 verified"

# ── install ────────────────────────────────────────────────────────
mkdir -p "$INSTALL_DIR"
install -m 0755 "${tmp}/swarmery" "${INSTALL_DIR}/swarmery"
echo "✓ installed swarmery ${version} → ${INSTALL_DIR}/swarmery"

if [ "$os" = "darwin" ]; then
  # The released binaries are unsigned, so Gatekeeper quarantines a download.
  xattr -d com.apple.quarantine "${INSTALL_DIR}/swarmery" 2>/dev/null || true
fi

case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *) echo ""
     echo "  ${INSTALL_DIR} is not on your PATH. Add it:"
     echo "    export PATH=\"${INSTALL_DIR}:\$PATH\"" ;;
esac

echo ""
echo "Next:"
echo "  swarmery serve                # listens on :7777 → http://localhost:7777"
echo ""
echo "  # sessions you have already run show up immediately — swarmery reads the"
echo "  # transcripts Claude Code already writes under ~/.claude/projects/"
echo ""
echo "  # bootstrap a project, from its root:"
echo "  swarmery onboard <project-slug> [pack ...]"
