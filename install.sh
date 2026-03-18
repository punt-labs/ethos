#!/bin/sh
# install.sh — Install ethos CLI and Claude Code plugin
# POSIX sh for maximum portability. Shellcheck with --shell=sh.
set -eu

VERSION="0.1.0"
REPO="punt-labs/ethos"
BINARY="ethos"
PLUGIN="ethos@punt-labs"
MARKETPLACE="punt-labs/claude-plugins"

# --- Helpers ---

info()  { printf '  %s\n' "$*"; }
fail()  { printf '  ERROR: %s\n' "$*" >&2; exit 1; }
check() { command -v "$1" >/dev/null 2>&1; }

# --- Pre-flight checks ---

info "ethos installer v${VERSION}"
info ""

# Check for Go
if ! check go; then
  fail "go is required. Install from https://go.dev/dl/"
fi

GO_VERSION=$(go version | sed 's/.*go\([0-9]*\.[0-9]*\).*/\1/')
info "Go ${GO_VERSION} found"

# Check for claude CLI (needed for plugin install)
if ! check claude; then
  info "WARNING: claude CLI not found — skipping plugin install"
  info "  Install from: https://docs.anthropic.com/en/docs/claude-code"
  SKIP_PLUGIN=1
else
  SKIP_PLUGIN=0
fi

# --- Step 1: Build and install binary ---

info ""
info "Step 1: Installing ethos binary..."

INSTALL_DIR="$HOME/.local/bin"
mkdir -p "$INSTALL_DIR"

# Build from source via go install
GOBIN="$INSTALL_DIR" go install "github.com/${REPO}/cmd/ethos@v${VERSION}" 2>/dev/null || {
  # Fallback: clone and build
  info "  go install failed, building from source..."
  TMPDIR_BUILD=$(mktemp -d)
  git clone --depth 1 --branch "v${VERSION}" "https://github.com/${REPO}.git" "$TMPDIR_BUILD" 2>/dev/null || \
    git clone --depth 1 "https://github.com/${REPO}.git" "$TMPDIR_BUILD" 2>/dev/null || \
    fail "Failed to clone repository"
  cd "$TMPDIR_BUILD"
  CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o "${INSTALL_DIR}/${BINARY}" ./cmd/ethos/
  cd -
  rm -rf "$TMPDIR_BUILD"
}

# Verify binary
if ! check "$BINARY"; then
  # Add to PATH hint
  info "  Binary installed to ${INSTALL_DIR}/${BINARY}"
  info "  Add to PATH: export PATH=\"\$HOME/.local/bin:\$PATH\""
  export PATH="$INSTALL_DIR:$PATH"
fi

info "  $(ethos version)"

# --- Step 2: Create identity directory ---

info ""
info "Step 2: Creating identity directory..."
mkdir -p "$HOME/.punt-labs/ethos/identities"
info "  $HOME/.punt-labs/ethos/identities/"

# --- Step 3: Register marketplace and install plugin ---

if [ "$SKIP_PLUGIN" = "0" ]; then
  info ""
  info "Step 3: Installing Claude Code plugin..."

  # Register marketplace if not already registered
  claude plugin marketplace add "$MARKETPLACE" < /dev/null 2>/dev/null || true

  # Refresh marketplace
  claude plugin marketplace update < /dev/null 2>/dev/null || true

  # Install plugin
  claude plugin install "$PLUGIN" --scope user < /dev/null 2>/dev/null || {
    info "  Plugin install failed (may not be in marketplace yet)"
    info "  Use 'claude --plugin-dir <ethos-repo>' for local development"
  }
else
  info ""
  info "Step 3: Skipping plugin install (claude CLI not found)"
fi

# --- Step 4: Doctor ---

info ""
info "Step 4: Health check..."
ethos doctor || true

info ""
info "Done. Run 'ethos create' to create your first identity."
