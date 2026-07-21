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

# is_shell_hook FILE — succeed when FILE has a shell-family shebang, or none
# (git runs a hook without a shebang via sh). Used to refuse chaining our
# POSIX-sh section onto a Python/Node/binary host, which git would run under
# the host's interpreter — breaking the host and never running the seal.
is_shell_hook() {
  IFS= read -r _sh < "$1" 2>/dev/null || _sh=""
  case "$_sh" in
    '#!'*) ;;
    *) return 0 ;; # no shebang — git uses sh
  esac
  _i=${_sh#\#!}; _i=${_i# }; _i=${_i%% *}; _i=${_i##*/}
  if [ "$_i" = "env" ]; then
    _i=${_sh#*env }; _i=${_i%% *}; _i=${_i##*/}
  fi
  case "$_i" in
    sh|bash|dash|ksh|zsh|mksh|ash) return 0 ;;
    *) return 1 ;;
  esac
}

# emit_section TAG SRC — print SRC (minus its shebang) fenced by our markers.
emit_section() {
  printf '# --- BEGIN %s ---\n' "$1"
  awk 'NR == 1 && /^#!/ { next } { print }' "$2"
  printf '# --- END %s ---\n' "$1"
}

# write_marker_form DEST SRC TAG — write a fresh hook: a shebang plus the
# marker-delimited section. Every state install_hook creates is marker-managed,
# so re-install always resolves through the marker branch.
write_marker_form() {
  _tmp=$(mktemp "${1}.XXXXXX") || fail "cannot create a temp file next to $1 — hook not installed"
  {
    printf '#!/bin/sh\n'
    emit_section "$3" "$2"
  } > "$_tmp"
  mv "$_tmp" "$1"
  chmod +x "$1"
}

# install_hook DEST SRC TAG IDENT
# Install the ethos hook at DEST, coexisting with any hook already there.
#   - no hook present: write the marker form
#   - our marker section present: replace it in place (idempotent upgrade)
#   - our pre-marker standalone (positively identified by its header IDENT,
#     no section markers): replace with the marker form
#   - a foreign or hybrid hook present: append our marker section
#
# The appended section runs after the host hook's own content, so it fires
# only when that content falls through — the beads pre-commit hook, the case
# this fix exists for, exits nonzero only on failure and otherwise falls
# through. The section carries SRC, which preserves the host's fall-through
# exit status (SRC captures $? and returns it on passthrough). A host hook
# that exits or execs a program unconditionally bypasses the section; that
# case is detected on the host's last effective line and warned.
install_hook() {
  dest=$1 src=$2 tag=$3 ident=$4

  # A symlinked hook (dotfile manager, shared hooks dir): operate on the
  # link's target so mv does not flatten the link into a regular file and
  # sever the managing tool. One level of indirection covers the common case.
  if [ -L "$dest" ]; then
    link=$(readlink "$dest") || fail "cannot resolve symlink $dest — hook not installed"
    [ -n "$link" ] || fail "empty symlink target for $dest — hook not installed"
    case "$link" in
      /*) ;;
      *) link="$(dirname "$dest")/$link" ;;
    esac
    warn "$dest is a symlink — updating its target $link instead"
    dest=$link
  fi

  if [ ! -e "$dest" ]; then
    write_marker_form "$dest" "$src" "$tag"
    ok "$dest installed"
    return
  fi

  if grep -q "^# --- BEGIN $tag" "$dest" 2>/dev/null; then
    : # our section is present — strip and re-append below (idempotent)
  elif awk 'NR == 2' "$dest" 2>/dev/null | grep -qF "$ident" && ! grep -q "^# --- BEGIN " "$dest" 2>/dev/null; then
    # Our own pre-marker standalone: positively identified by the header line
    # IDENT on LINE 2, which every version of our hook carries there. Checking
    # line 2 specifically — not anywhere — is what distinguishes our standalone
    # from a `cat hooks/pre-commit.sh >> hook` hybrid, whose host content comes
    # first and pushes our header mid-file. The hybrid falls through to
    # strip-and-append, preserving the host. Replace the standalone with the
    # marker form.
    write_marker_form "$dest" "$src" "$tag"
    ok "$dest refreshed"
    return
  fi

  # Only chain into a shell host. git runs the mixed file with the host's
  # interpreter, so appending our POSIX-sh section to a Python/Node/binary
  # hook would break the host and never run the seal. Leaving a non-shell
  # host untouched is better than breaking it — doctor then FAILs on the
  # missing seal, the correct signal.
  if ! is_shell_hook "$dest"; then
    warn "$dest has a non-shell shebang — leaving it untouched; the ethos seal was not chained in"
    return
  fi

  # Refuse to edit a hook with our BEGIN marker but no matching END: a
  # hand-truncated section would make the strip-awk drop everything after
  # BEGIN, destroying host content. Abort and let the operator fix it.
  if grep -q "^# --- BEGIN $tag" "$dest" 2>/dev/null && ! grep -q "^# --- END $tag" "$dest" 2>/dev/null; then
    fail "$dest has a '$tag' BEGIN marker with no matching END — refusing to edit a truncated hook; fix it by hand"
  fi

  tmp=$(mktemp "${dest}.XXXXXX") || fail "cannot create a temp file next to $dest — hook not installed"
  awk -v tag="$tag" '
    $0 ~ "^# --- BEGIN " tag { skip = 1 }
    skip && $0 ~ "^# --- END " tag { skip = 0; next }
    !skip { print }
  ' "$dest" > "$tmp"

  # Last effective line: the last non-blank, non-comment line. A trailing
  # comment after an exit must not hide it. An unconditional `exit`, or an
  # `exec` of a program, bypasses the appended section. `exec` with a
  # redirection target (exec 3>&1, exec >log) is an fd builtin that does not
  # replace the shell, so those forms are excluded rather than flagged.
  last=$(awk '/^[[:space:]]*#/ { next } NF { l = $0 } END { sub(/^[[:space:]]+/, "", l); sub(/[[:space:]]+$/, "", l); print l }' "$tmp")
  case "$last" in
    exit|exit\ *|exit';'*)
      warn "$dest ends in an unconditional 'exit' — the ethos section may not run" ;;
    exec\ [0-9]*|exec\ '<'*|exec\ '>'*|exec\ '&'*)
      : ;; # fd redirection, not a process replacement
    exec\ ?*)
      warn "$dest ends in an unconditional 'exec' — the ethos section may not run" ;;
  esac

  emit_section "$tag" "$src" >> "$tmp"

  mv "$tmp" "$dest"
  chmod +x "$dest"
  ok "$dest chained (ethos section)"
}

# resolve_hooks_dir — print the hooks directory git runs, or nothing when not
# in a git work tree. `--git-path hooks` is git's own answer: it resolves a
# worktree's common dir (where --git-dir points at the dead
# .git/worktrees/<name>) and honors core.hooksPath. Doctor resolves the same
# way, so installer and doctor agree on one path.
#
# core.hooksPath can point at a TRACKED path inside the work tree (husky's
# .husky/) or at a shared directory OUTSIDE the repo (~/.githooks). Installing
# there is correct — the seal must live where git runs hooks — but each case
# has a different consequence worth naming: a tracked file dirties the tree, a
# shared dir affects every repo using it. Warn accordingly (to stderr) rather
# than acting silently. Relative paths are resolved against the PHYSICAL cwd
# (pwd -P) so the comparison against git's already-physical --show-toplevel /
# --git-common-dir is not defeated by a symlinked cwd (macOS /tmp vs
# /private/tmp). Classification is skipped when either anchor is empty, which
# would otherwise turn the pattern into "/*" and match every absolute path.
resolve_hooks_dir() {
  command -v git >/dev/null 2>&1 || return 0
  _hd=$(git rev-parse --git-path hooks 2>/dev/null || true)
  [ -n "$_hd" ] || return 0
  _common=$(git rev-parse --git-common-dir 2>/dev/null || true)
  _top=$(git rev-parse --show-toplevel 2>/dev/null || true)
  _pwd=$(pwd -P)
  case "$_hd" in /*) ;; *) _hd="$_pwd/$_hd" ;; esac
  case "$_common" in ""|/*) ;; *) _common="$_pwd/$_common" ;; esac
  if [ -n "$_common" ] && [ -n "$_top" ]; then
    case "$_hd" in
      "$_common"/*) ;; # under the git dir — private, not tracked
      "$_top"/*) warn "core.hooksPath places hooks at $_hd inside the work tree — the seal will be written into a tracked file" ;;
      *) warn "core.hooksPath places hooks outside the repo at $_hd — shared across every repo using this hooksPath" ;;
    esac
  fi
  printf '%s\n' "$_hd"
}

VERSION="4.1.0"
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
mkdir -p "$HOME/.punt-labs/ethos/roles"
chmod 700 "$HOME/.punt-labs/ethos/roles"
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

# --- Step 6b: Seed starter content ---

info "Seeding starter content..."
if "$INSTALL_DIR/$BINARY" seed; then
  ok "Starter roles, talents, and skills deployed"
else
  warn "Could not seed starter content — run 'ethos seed' manually"
fi

# --- Step 6c: Resolve the git hooks directory ---

HOOKS_DIR=$(resolve_hooks_dir)

# --- Step 6d: Install commit-msg trailer hook (DES-054) ---

# When run inside a git work tree, install hooks/commit-msg.sh into the hooks
# dir so commits under a Tier B worker pick up the Mission: and Delegation:
# git trailers automatically. Passthrough on every other commit — the hook
# exits with the host status unless MISSION_ID or DELEGATION_ID is set.
#
# Skipped silently when not in a git work tree (curl|sh from $HOME). When an
# unrelated commit-msg hook already exists, chain into it with a marker-
# delimited section rather than skipping (ethos-2ol1).
HOOK_SRC=""
if [ -f "./hooks/commit-msg.sh" ]; then
  HOOK_SRC="./hooks/commit-msg.sh"
elif [ -n "${TMPDIR_BUILD:-}" ] && [ -f "$TMPDIR_BUILD/hooks/commit-msg.sh" ]; then
  HOOK_SRC="$TMPDIR_BUILD/hooks/commit-msg.sh"
fi
if [ -n "$HOOK_SRC" ] && [ -n "$HOOKS_DIR" ]; then
  info "Installing commit-msg trailer hook..."
  mkdir -p "$HOOKS_DIR"
  install_hook "$HOOKS_DIR/commit-msg" "$HOOK_SRC" "ETHOS DES-054 TRAILER" \
    "hooks/commit-msg.sh — Append Mission:/Delegation:"
fi

# --- Step 6e: Install pre-commit seal hook (DES-058) ---

# When run inside a git work tree, install hooks/pre-commit.sh into the hooks
# dir so `ethos audit seal` runs before every commit's index snapshot — the
# sealed audit chunks land in the same commit as the work. Passthrough when
# ethos is not installed or nothing is pending; fail-closed (exit 2) on a
# broken audit store.
#
# Skipped silently when not in a git work tree. When a foreign pre-commit hook
# already exists — the beads hook on every org machine — chain into it with a
# marker-delimited section rather than skipping (ethos-2ol1). Without this the
# seal, the feature's primary trigger, never installs.
PRECOMMIT_SRC=""
if [ -f "./hooks/pre-commit.sh" ]; then
  PRECOMMIT_SRC="./hooks/pre-commit.sh"
elif [ -n "${TMPDIR_BUILD:-}" ] && [ -f "$TMPDIR_BUILD/hooks/pre-commit.sh" ]; then
  PRECOMMIT_SRC="$TMPDIR_BUILD/hooks/pre-commit.sh"
fi
if [ -n "$PRECOMMIT_SRC" ] && [ -n "$HOOKS_DIR" ]; then
  info "Installing pre-commit seal hook..."
  mkdir -p "$HOOKS_DIR"
  install_hook "$HOOKS_DIR/pre-commit" "$PRECOMMIT_SRC" "ETHOS DES-058 SEAL" \
    "hooks/pre-commit.sh — Seal pending live audit lines"
fi

# --- Step 7: Health check ---

info "Verifying installation..."
printf '\n'
if "$INSTALL_DIR/$BINARY" doctor; then
  printf '\n%b%b%s is ready!%b\n\n' "$GREEN" "$BOLD" "$BINARY" "$NC"
  printf 'Run "ethos setup" in your project directory to get started.\n'
  printf 'Restart Claude Code twice to activate the plugin.\n\n'
else
  printf '\n'
  warn "ethos installed but doctor found issues (see above)"
  printf 'Fix the issues above, then run "ethos doctor" to verify.\n\n'
fi
