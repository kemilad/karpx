#!/usr/bin/env bash
# ============================================================
#  karpx installer
#  Usage: curl -fsSL https://raw.githubusercontent.com/kemilad/karpx/main/install.sh | bash
# ============================================================
set -euo pipefail

REPO="kemilad/karpx"
BINARY="karpx"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# -----------------------------------------------------------------------
# Colours
# -----------------------------------------------------------------------
if [ -t 1 ] && command -v tput &>/dev/null && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
  BOLD="$(tput bold)"; RESET="$(tput sgr0)"
  VIOLET=$'\033[38;5;135m'; CYAN=$'\033[38;5;87m'; GREEN=$'\033[38;5;84m'
  YELLOW=$'\033[38;5;220m'; RED=$'\033[38;5;196m'; WHITE=$'\033[38;5;255m'
  GRAY=$'\033[38;5;240m'
else
  BOLD=""; RESET=""; VIOLET=""; CYAN=""; GREEN=""; YELLOW=""; RED=""; WHITE=""; GRAY=""
fi

# -----------------------------------------------------------------------
# UI helpers
# -----------------------------------------------------------------------
banner() {
  printf "\n"
  # kar  ◆  p      x (violet)
  printf "  ${WHITE}${BOLD}██╗  ██╗ █████╗ ██████╗ ${RESET}  ${WHITE}${BOLD}██████╗ ${RESET}${VIOLET}${BOLD}██╗  ██╗${RESET}\n"
  printf "  ${WHITE}${BOLD}██║ ██╔╝██╔══██╗██╔══██╗${RESET}  ${WHITE}${BOLD}██╔══██╗${RESET}${VIOLET}${BOLD}╚██╗██╔╝${RESET}\n"
  printf "  ${WHITE}${BOLD}█████╔╝ ███████║██████╔╝${RESET}${VIOLET}${BOLD}◆${RESET}${WHITE}${BOLD} ██████╔╝${RESET}${VIOLET}${BOLD} ╚███╔╝ ${RESET}\n"
  printf "  ${WHITE}${BOLD}██╔═██╗ ██╔══██║██╔══██╗${RESET}${VIOLET}${BOLD}◆${RESET}${WHITE}${BOLD} ██╔═══╝ ${RESET}${VIOLET}${BOLD} ██╔██╗ ${RESET}\n"
  printf "  ${WHITE}${BOLD}██║  ██╗██║  ██║██║  ██║${RESET}  ${WHITE}${BOLD}██║     ${RESET}${VIOLET}${BOLD}██╔╝ ██╗${RESET}\n"
  printf "  ${WHITE}${BOLD}╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝${RESET}  ${WHITE}${BOLD}╚═╝     ${RESET}${VIOLET}${BOLD}╚═╝  ╚═╝${RESET}\n"
  printf "\n"
  printf "  ${VIOLET}THE KUBERNETES ESSENTIALS TOOLKIT${RESET}\n"
  printf "  ${GRAY}https://github.com/${REPO}${RESET}\n"
  printf "\n"
}

step()    { printf "${CYAN}${BOLD}  ›${RESET} ${WHITE}%s${RESET}\n" "$*"; }
ok()      { printf "${GREEN}${BOLD}  ✓${RESET} ${WHITE}%s${RESET}\n" "$*"; }
warn()    { printf "${YELLOW}${BOLD}  !${RESET} ${YELLOW}%s${RESET}\n" "$*"; }
fail()    { printf "${RED}${BOLD}  ✗${RESET} ${RED}%s${RESET}\n" "$*"; exit 1; }
dim()     { printf "${GRAY}    %s${RESET}\n" "$*"; }
divider() { printf "${GRAY}  ──────────────────────────────────────────${RESET}\n"; }

spinner() {
  local pid=$1 msg="$2"
  local frames=('⠋' '⠙' '⠹' '⠸' '⠼' '⠴' '⠦' '⠧' '⠇' '⠏')
  local i=0
  while kill -0 "$pid" 2>/dev/null; do
    printf "\r  ${CYAN}${frames[$i]}${RESET}  ${WHITE}%s${RESET}   " "$msg"
    i=$(( (i + 1) % ${#frames[@]} ))
    sleep 0.08
  done
  printf "\r%-60s\r" " "
}

# -----------------------------------------------------------------------
# Platform detection
# -----------------------------------------------------------------------
detect_platform() {
  OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64)        ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) fail "Unsupported architecture: $ARCH" ;;
  esac
  case "$OS" in
    linux|darwin) ;;
    *) fail "Unsupported OS: $OS. Download from https://github.com/$REPO/releases" ;;
  esac
  PLATFORM="${OS}_${ARCH}"
}

# -----------------------------------------------------------------------
# Resolve latest version
# -----------------------------------------------------------------------
resolve_version() {
  if [ -n "${VERSION:-}" ]; then
    ok "Pinned version: ${CYAN}${VERSION}${RESET}"
    return
  fi
  step "Resolving latest release..."
  # Use the HTML redirect (no API rate limits) — github.com/releases/latest
  # redirects to the actual release URL which contains the tag in the path.
  (curl -fsSL -o /dev/null -w "%{url_effective}" \
    "https://github.com/$REPO/releases/latest" 2>/dev/null \
    | sed 's|.*/tag/||' > /tmp/karpx_ver) &
  spinner $! "Fetching latest release tag"
  wait
  VERSION="$(cat /tmp/karpx_ver 2>/dev/null || true)"
  rm -f /tmp/karpx_ver
  [ -z "$VERSION" ] && fail "Could not resolve latest version. Set VERSION=vX.Y.Z to pin."
  ok "Latest: ${CYAN}${VERSION}${RESET}"
}

# -----------------------------------------------------------------------
# Check existing install
# -----------------------------------------------------------------------
check_existing() {
  if command -v "$BINARY" &>/dev/null; then
    EXISTING="$($BINARY version 2>/dev/null | awk '{print $2}' || echo '?')"
    if [ "$EXISTING" = "$VERSION" ]; then
      ok "Already on ${CYAN}${VERSION}${RESET} — nothing to do."
      post_install
      exit 0
    fi
    warn "Found existing install: ${EXISTING}"
    dim "Will upgrade  ${EXISTING} → ${VERSION}"
  fi
}

# -----------------------------------------------------------------------
# Download and verify
# -----------------------------------------------------------------------
download() {
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT

  TARBALL="${BINARY}_${PLATFORM}.tar.gz"
  URL="https://github.com/$REPO/releases/download/$VERSION/$TARBALL"
  CHECKSUM_URL="https://github.com/$REPO/releases/download/$VERSION/checksums.txt"

  step "Downloading karpx ${VERSION} for ${OS}/${ARCH}..."
  dim "${URL}"
  curl -fsSL "$URL" -o "$TMP/$TARBALL" || fail "Download failed (404?). Release may still be building — try again in a minute."
  curl -fsSL "$CHECKSUM_URL" -o "$TMP/checksums.txt" || fail "Could not download checksums."
  ok "Download complete"

  step "Verifying SHA-256 checksum..."
  expected=$(grep "$TARBALL" "$TMP/checksums.txt" | awk '{print $1}')
  [ -z "$expected" ] && fail "Could not find checksum for $TARBALL in checksums.txt"
  actual=$(shasum -a 256 "$TMP/$TARBALL" | awk '{print $1}')
  [ "$expected" = "$actual" ] || fail "Checksum mismatch — expected $expected, got $actual"
  ok "Checksum verified"

  tar -xzf "$TMP/$TARBALL" -C "$TMP"
  chmod +x "$TMP/$BINARY"
}

# -----------------------------------------------------------------------
# Install binary
# -----------------------------------------------------------------------
install_binary() {
  step "Installing to ${INSTALL_DIR}/${BINARY}..."
  if [ -w "$INSTALL_DIR" ]; then
    mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
    INSTALLED_PATH="$INSTALL_DIR/$BINARY"
  else
    warn "No write access to ${INSTALL_DIR} — installing to ~/bin"
    mkdir -p "$HOME/bin"
    mv "$TMP/$BINARY" "$HOME/bin/$BINARY"
    INSTALLED_PATH="$HOME/bin/$BINARY"
    if ! echo "$PATH" | grep -q "$HOME/bin"; then
      warn "Add this to your shell profile and reload:"
      dim 'export PATH="$HOME/bin:$PATH"'
    fi
  fi
  ok "Installed  ${CYAN}${INSTALLED_PATH}${RESET}"
}

# -----------------------------------------------------------------------
# Post-install summary
# -----------------------------------------------------------------------
post_install() {
  echo ""
  divider
  echo ""
  printf "  ${GREEN}${BOLD}karpx ${VERSION} is ready ⚡${RESET}\n"
  echo ""
  printf "  ${WHITE}${BOLD}Open the TUI${RESET}\n"
  printf "  ${CYAN}karpx${RESET}                             open interactive TUI\n"
  printf "  ${CYAN}karpx${RESET} ${GRAY}-c${RESET} ${WHITE}my-cluster${RESET}                target a specific cluster\n"
  printf "  ${CYAN}karpx${RESET} ${GRAY}-c${RESET} ${WHITE}my-cluster -r us-east-1${RESET}   with explicit region\n"
  echo ""
  printf "  ${WHITE}${BOLD}Non-interactive commands${RESET}\n"
  printf "  ${CYAN}karpx detect${RESET}    ${GRAY}─${RESET}  print installed Karpenter version\n"
  printf "  ${CYAN}karpx install${RESET}   ${GRAY}─${RESET}  install Karpenter (CI / scripting)\n"
  printf "  ${CYAN}karpx upgrade${RESET}   ${GRAY}─${RESET}  upgrade to latest compatible version\n"
  printf "  ${CYAN}karpx np${RESET}        ${GRAY}─${RESET}  manage NodePools and EC2NodeClasses\n"
  printf "  ${CYAN}karpx version${RESET}   ${GRAY}─${RESET}  print karpx version\n"
  echo ""
  printf "  ${GRAY}docs  https://github.com/${REPO}${RESET}\n"
  echo ""
  divider
  echo ""
}

# -----------------------------------------------------------------------
# Main
# -----------------------------------------------------------------------
main() {
  banner
  divider
  detect_platform
  dim "Platform: ${OS}/${ARCH}"
  echo ""
  resolve_version
  check_existing
  echo ""
  divider
  download
  install_binary
  post_install
}

main
