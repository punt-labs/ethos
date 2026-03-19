#!/bin/sh
# install.sh — Install ethos CLI and Claude Code plugin
# Usage: curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/<SHA>/install.sh | sh
set -eu

# --- Colors (disabled when not a terminal) ---
if [ -t 1 ]; then
  BOLD='\033[1m' GREEN='\033[32m' YELLOW='\033[33m' NC='\033[0m'
else
  BOLD='' GREEN='' YELLOW='' NC=''
fi

info() { printf '%b▶%b %s\n' "$BOLD" "$NC" "$1"; }
ok()   { printf '  %b✓%b %s\n' "$GREEN" "$NC" "$1"; }
warn() { printf '  %b!%b %s\n' "$YELLOW" "$NC" "$1"; }
fail() { printf '  %b✗%b %s\n' "$YELLOW" "$NC" "$1"; exit 1; }

VERSION="0.1.0"
REPO="punt-labs/ethos"
BINARY="ethos"
MARKETPLACE_REPO="punt-labs/claude-plugins"
MARKETPLACE_NAME="punt-labs"
PLUGIN_NAME="ethos"

# --- Step 1: Prerequisites ---

info "Checking prerequisites..."

if command -v go >/dev/null 2>&1; then
  GO_VERSION=$(go version | sed 's/.*go\([0-9]*\.[0-9]*\).*/\1/')
  ok "Go ${GO_VERSION}"
else
  fail "go is required. Install from https://go.dev/dl/"
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

# --- Step 2: Build and install binary ---

info "Installing ethos binary..."

INSTALL_DIR="$HOME/.local/bin"
mkdir -p "$INSTALL_DIR"

GOBIN="$INSTALL_DIR" go install "github.com/${REPO}/cmd/ethos@v${VERSION}" 2>/dev/null || {
  if ! command -v git >/dev/null 2>&1; then
    fail "go install failed and git is not available for fallback build"
  fi
  warn "go install failed, building from source..."
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
}

export PATH="$INSTALL_DIR:$PATH"
ok "$BINARY $("$INSTALL_DIR/$BINARY" version)"

# --- Step 3: Create identity directory ---

info "Creating identity directory..."
mkdir -p "$HOME/.punt-labs/ethos/identities"
chmod 700 "$HOME/.punt-labs/ethos/identities"
ok "$HOME/.punt-labs/ethos/identities/"

# --- Step 4: Register marketplace ---

if [ "$SKIP_PLUGIN" = "0" ]; then
  info "Registering Punt Labs marketplace..."

  if claude plugin marketplace list < /dev/null 2>/dev/null | grep -q "$MARKETPLACE_NAME"; then
    ok "marketplace already registered"
    claude plugin marketplace update "$MARKETPLACE_NAME" < /dev/null 2>/dev/null || true
  else
    claude plugin marketplace add "$MARKETPLACE_REPO" < /dev/null || fail "Failed to register marketplace"
    ok "marketplace registered"
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
  ok "$PLUGIN_NAME plugin installed"

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
