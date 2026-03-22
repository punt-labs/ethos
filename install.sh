#!/bin/sh
# install.sh — Install ethos CLI and Claude Code plugin
# Usage: curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/v0.2.0/install.sh | sh
set -eu

# --- Colors (disabled when not a terminal) ---
if [ -t 1 ]; then
  BOLD='\033[1m' GREEN='\033[32m' YELLOW='\033[33m' NC='\033[0m'
else
  BOLD='' GREEN='' YELLOW='' NC=''
fi

info() { printf '%b▶%b %s\n' "$BOLD" "$NC" "$1"; }
ok()   { printf '  %b✓%b %s\n' "$GREEN" "$NC" "$1"; }
warn() { printf '  %b!%b %s\n' "$YELLOW" "$NC" "$1" >&2; }
fail() { printf '  %b✗%b %s\n' "$YELLOW" "$NC" "$1" >&2; exit 1; }

VERSION="1.1.0"
REPO="punt-labs/ethos"
BINARY="ethos"
MARKETPLACE_REPO="punt-labs/claude-plugins"
MARKETPLACE_NAME="punt-labs"
PLUGIN_NAME="ethos"

# --- Step 1: Prerequisites ---

info "Checking prerequisites..."

if command -v curl >/dev/null 2>&1; then
  ok "curl found"
else
  warn "curl not found — pre-built binary download will not work"
fi

if command -v go >/dev/null 2>&1; then
  GO_VERSION=$(go version | sed 's/.*go\([0-9]*\.[0-9]*\).*/\1/')
  ok "Go ${GO_VERSION} (fallback build)"
fi

if command -v git >/dev/null 2>&1; then
  ok "git found"
else
  warn "git not found — fallback build from source will not work"
fi

SKIP_PLUGIN=0
if command -v claude >/dev/null 2>&1; then
  ok "claude CLI found"
else
  warn "claude CLI not found — skipping plugin install"
  warn "Install from: https://docs.anthropic.com/en/docs/claude-code"
  SKIP_PLUGIN=1
fi

# Plugin install requires git for SSH/HTTPS clone
if [ "$SKIP_PLUGIN" = "0" ] && ! command -v git >/dev/null 2>&1; then
  warn "git not found — skipping plugin install (required for clone)"
  SKIP_PLUGIN=1
fi

# --- Step 2: Install binary ---

info "Installing ethos binary..."

INSTALL_DIR="$HOME/.local/bin"
mkdir -p "$INSTALL_DIR"

# Detect platform for pre-built binary download
OS_RAW="$(uname -s)"
case "$OS_RAW" in
  Darwin) OS="darwin" ;;
  Linux)  OS="linux" ;;
  *)      OS="" ;;
esac
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
  *)       ARCH="" ;;
esac

DOWNLOAD_URL="https://github.com/${REPO}/releases/download/v${VERSION}/${BINARY}-${OS}-${ARCH}"
INSTALLED=0

# Try downloading pre-built binary first (atomic: temp file then mv)
if [ -n "$OS" ] && [ -n "$ARCH" ] && command -v curl >/dev/null 2>&1; then
  TMPBIN="$(mktemp "${INSTALL_DIR}/${BINARY}.tmp.XXXXXX")"
  if curl -fsSL -o "$TMPBIN" "$DOWNLOAD_URL"; then
    chmod +x "$TMPBIN"
    mv "$TMPBIN" "${INSTALL_DIR}/${BINARY}"
    INSTALLED=1
  else
    warn "Download failed for ${BINARY}-${OS}-${ARCH}, falling back to source build"
    rm -f "$TMPBIN"
  fi
fi

# Fallback: build from source with version injection
if [ "$INSTALLED" = "0" ]; then
  if ! command -v go >/dev/null 2>&1; then
    fail "No pre-built binary (OS=${OS_RAW}, arch=$(uname -m)) and Go is not installed"
  fi
  if ! command -v git >/dev/null 2>&1; then
    fail "No pre-built binary and git is not installed for source build"
  fi
  warn "Pre-built binary not available, building from source..."
  ORIG_DIR=$(pwd)
  TMPDIR_BUILD=$(mktemp -d)
  cleanup_build() { rm -rf "$TMPDIR_BUILD"; }
  trap cleanup_build EXIT
  if ! git clone --depth 1 --branch "v${VERSION}" "https://github.com/${REPO}.git" "$TMPDIR_BUILD"; then
    fail "Tag v${VERSION} not found. This installer requires a tagged release."
  fi
  cd "$TMPDIR_BUILD" || fail "Cannot enter build directory"
  CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o "${INSTALL_DIR}/${BINARY}" ./cmd/ethos/
  cd "$ORIG_DIR" || true
  rm -rf "$TMPDIR_BUILD"
  trap - EXIT
fi

export PATH="$INSTALL_DIR:$PATH"
ok "$("$INSTALL_DIR/$BINARY" version)"

# Ensure ~/.local/bin is on PATH permanently (idempotent)
SHELL_NAME="$(basename "${SHELL:-sh}")"
PROFILE=""
case "$SHELL_NAME" in
  zsh)  PROFILE="$HOME/.zshrc" ;;
  bash)
    if [ -f "$HOME/.bash_profile" ]; then
      PROFILE="$HOME/.bash_profile"
    else
      PROFILE="$HOME/.bashrc"
    fi ;;
  fish) warn "fish shell detected — add $INSTALL_DIR to PATH manually" ;;
  *)    PROFILE="$HOME/.profile" ;;
esac
MARKER='# Added by ethos installer'
if [ -n "$PROFILE" ] && ! grep -qF "$MARKER" "$PROFILE" 2>/dev/null; then
  # shellcheck disable=SC2016 # $PATH must stay literal in the profile
  printf '\n%s\nexport PATH="%s:$PATH"\n' "$MARKER" "$INSTALL_DIR" >> "$PROFILE"
  ok "Added $INSTALL_DIR to PATH in $PROFILE"
fi

# --- Step 3: Create identity directory ---

info "Creating directories..."
mkdir -p "$HOME/.punt-labs/ethos/identities"
chmod 700 "$HOME/.punt-labs/ethos/identities"
mkdir -p "$HOME/.punt-labs/ethos/talents"
chmod 700 "$HOME/.punt-labs/ethos/talents"
mkdir -p "$HOME/.punt-labs/ethos/personalities"
chmod 700 "$HOME/.punt-labs/ethos/personalities"
mkdir -p "$HOME/.punt-labs/ethos/writing-styles"
chmod 700 "$HOME/.punt-labs/ethos/writing-styles"
ok "$HOME/.punt-labs/ethos/"

# --- Step 4: Register marketplace ---

if [ "$SKIP_PLUGIN" = "0" ]; then
  info "Registering Punt Labs marketplace..."

  if claude plugin marketplace list < /dev/null 2>/dev/null | grep -q "$MARKETPLACE_NAME"; then
    ok "marketplace already registered"
  else
    claude plugin marketplace add "$MARKETPLACE_REPO" < /dev/null || fail "Failed to register marketplace"
    ok "marketplace registered"
  fi

  # Always update to get the latest plugin versions (including this one).
  if ! claude plugin marketplace update "$MARKETPLACE_NAME" < /dev/null 2>/dev/null; then
    warn "marketplace update failed — plugin may install a stale version"
  fi

  # --- Step 5: SSH fallback for plugin install ---

  # claude plugin install clones via SSH (git@github.com:...).
  # Users without SSH keys need an HTTPS fallback.
  NEED_HTTPS_REWRITE=0
  cleanup_https_rewrite() {
    if [ "$NEED_HTTPS_REWRITE" = "1" ]; then
      git config --global --unset url."https://github.com/".insteadOf 2>/dev/null || true
      NEED_HTTPS_REWRITE=0
    fi
  }
  trap cleanup_https_rewrite EXIT INT TERM

  if ! ssh -n -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=5 -T git@github.com 2>&1 | grep -q "successfully authenticated"; then
    warn "SSH auth to GitHub unavailable, using HTTPS fallback"
    git config --global url."https://github.com/".insteadOf "git@github.com:"
    NEED_HTTPS_REWRITE=1
  fi

  # --- Step 6: Install plugin ---

  info "Installing $PLUGIN_NAME plugin..."

  claude plugin uninstall "${PLUGIN_NAME}@${MARKETPLACE_NAME}" < /dev/null 2>/dev/null || true
  if ! claude plugin install "${PLUGIN_NAME}@${MARKETPLACE_NAME}" --scope user < /dev/null; then
    cleanup_https_rewrite
    fail "Failed to install $PLUGIN_NAME plugin"
  fi
  if ! claude plugin list < /dev/null 2>/dev/null | grep -q "$PLUGIN_NAME@$MARKETPLACE_NAME"; then
    cleanup_https_rewrite
    fail "$PLUGIN_NAME install reported success but plugin not found"
  fi

  # Verify installed plugin version matches expected version
  INSTALLED_PLUGIN_DIR="$HOME/.claude/plugins/cache/$MARKETPLACE_NAME/$PLUGIN_NAME/$VERSION"
  if [ -d "$INSTALLED_PLUGIN_DIR" ]; then
    ok "$PLUGIN_NAME plugin v${VERSION} installed"
  else
    # Find the most recently installed version (newest by mtime)
    INSTALLED_VERSION=""
    PLUGIN_CACHE_BASE="$HOME/.claude/plugins/cache/$MARKETPLACE_NAME/$PLUGIN_NAME"
    if [ -d "$PLUGIN_CACHE_BASE" ]; then
      # shellcheck disable=SC2012 # directory names are version numbers, safe for ls
      INSTALLED_VERSION="$(ls -1t "$PLUGIN_CACHE_BASE" 2>/dev/null | head -n 1 || true)"
    fi
    if [ -n "$INSTALLED_VERSION" ]; then
      warn "$PLUGIN_NAME plugin v${INSTALLED_VERSION} installed (expected v${VERSION})"
      warn "The marketplace may not have v${VERSION} yet. Run:"
      warn "  claude plugin marketplace update $MARKETPLACE_NAME"
      warn "  claude plugin install ${PLUGIN_NAME}@${MARKETPLACE_NAME} --scope user"
    else
      ok "$PLUGIN_NAME plugin installed (version not verified)"
    fi
  fi

  cleanup_https_rewrite
else
  info "Skipping plugin install (claude CLI not found)"
fi

# --- Step 7: Health check ---

info "Verifying installation..."
printf '\n'
if "$INSTALL_DIR/$BINARY" doctor; then
  printf '\n%b%b%s is ready!%b\n\n' "$GREEN" "$BOLD" "$BINARY" "$NC"
  printf 'Run "ethos create" to create your first identity.\n'
  printf 'Restart Claude Code twice to activate the plugin.\n\n'
else
  printf '\n'
  warn "ethos installed but doctor found issues (see above)"
  printf 'Fix the issues above, then run "ethos doctor" to verify.\n\n'
fi
