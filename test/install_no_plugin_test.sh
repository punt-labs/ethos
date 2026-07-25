#!/bin/sh
# Smoke test for install.sh plugin-skip resolution (--no-plugin / ETHOS_NO_PLUGIN).
#
# Runs the real install.sh end-to-end against a sandbox HOME with stubbed
# commands on PATH: a fake `curl` that delivers a fake `ethos` binary, and fake
# `claude`/`ssh` that satisfy the plugin steps. Asserts the four ratified cases:
#
#   --no-plugin        -> plugin steps skipped, CLI-only message, exit 0
#   ETHOS_NO_PLUGIN=1  -> same
#   unknown flag       -> exit 2
#   default            -> plugin installed, "is ready!" message, exit 0
#
# Standalone: `sh test/install_no_plugin_test.sh`. Exit 0 = all pass.
set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
INSTALL_SH="$SCRIPT_DIR/../install.sh"

fails=0
pass()   { printf 'ok   - %s\n' "$1"; }
failed() { printf 'FAIL - %s\n' "$1"; fails=$((fails + 1)); }

assert_rc()       { if [ "$RC" = "$2" ]; then pass "$1"; else failed "$1 (rc=$RC want $2)"; fi; }
assert_contains() { case "$OUT" in *"$2"*) pass "$1" ;; *) failed "$1 (missing: $2)" ;; esac; }
assert_absent()   { case "$OUT" in *"$2"*) failed "$1 (unexpected: $2)" ;; *) pass "$1" ;; esac; }

# Fresh sandbox with stub commands and a fake ethos binary. Sets SANDBOX, BIN.
make_sandbox() {
  SANDBOX=$(mktemp -d "${TMPDIR:-/tmp}/ethos-noplugin-test.XXXXXX")
  BIN="$SANDBOX/bin"
  mkdir -p "$BIN"

  cat > "$SANDBOX/ethos" <<'SH'
#!/bin/sh
case "$1" in
  version) echo "ethos 0.0.0-test" ;;
  *)       ;;
esac
exit 0
SH
  chmod +x "$SANDBOX/ethos"

  cat > "$BIN/curl" <<SH
#!/bin/sh
out=""
prev=""
for a in "\$@"; do
  [ "\$prev" = "-o" ] && out="\$a"
  prev="\$a"
done
[ -n "\$out" ] && cp "$SANDBOX/ethos" "\$out"
exit 0
SH
  chmod +x "$BIN/curl"

  cat > "$BIN/claude" <<'SH'
#!/bin/sh
case "$*" in
  "plugin marketplace list") echo "punt-labs" ;;
  "plugin list")             echo "ethos@punt-labs" ;;
esac
exit 0
SH
  chmod +x "$BIN/claude"

  cat > "$BIN/ssh" <<'SH'
#!/bin/sh
echo "Hi! You've successfully authenticated"
exit 0
SH
  chmod +x "$BIN/ssh"
}

# Run install.sh in the sandbox. Call as: run [VAR=value ...] -- [args ...].
# Items before '--' are env assignments; items after are install.sh args.
# Rebuilds "$@" as "VAR=value ... sh INSTALL_SH args ..." so the whole line
# is handed to env as quoted operands — no unquoted expansion.
# Sets OUT (combined stdout+stderr) and RC.
run() {
  n=$#
  seen_sep=0
  while [ "$n" -gt 0 ]; do
    arg=$1
    shift
    n=$((n - 1))
    if [ "$seen_sep" = 0 ] && [ "$arg" = "--" ]; then
      seen_sep=1
      set -- "$@" sh "$INSTALL_SH"
    else
      set -- "$@" "$arg"
    fi
  done
  set +e
  OUT=$(cd "$SANDBOX" && env HOME="$SANDBOX" PATH="$BIN:/usr/bin:/bin" "$@" 2>&1)
  RC=$?
  set -e
}

# --- Case: --no-plugin flag ---
make_sandbox
run -- --no-plugin
assert_rc       "--no-plugin: exit 0" 0
assert_contains "--no-plugin: skip announced" "plugin install skipped by request"
assert_contains "--no-plugin: CLI-only message" "CLI-only mode"
assert_absent   "--no-plugin: no plugin restart line" "Restart Claude Code"
assert_contains "--no-plugin: requested remediation" "Re-run the installer without --no-plugin"
assert_absent   "--no-plugin: no claude-absent remedy" "claude CLI was not found"
rm -rf "$SANDBOX"

# --- Case: ETHOS_NO_PLUGIN=1 env ---
make_sandbox
run ETHOS_NO_PLUGIN=1 --
assert_rc       "ETHOS_NO_PLUGIN=1: exit 0" 0
assert_contains "ETHOS_NO_PLUGIN=1: skip announced" "plugin install skipped by request"
assert_contains "ETHOS_NO_PLUGIN=1: CLI-only message" "CLI-only mode"
assert_absent   "ETHOS_NO_PLUGIN=1: no plugin restart line" "Restart Claude Code"
assert_contains "ETHOS_NO_PLUGIN=1: requested remediation" "Re-run the installer without --no-plugin"
assert_contains "ETHOS_NO_PLUGIN=1: remediation names env var" "ETHOS_NO_PLUGIN"
rm -rf "$SANDBOX"

# --- Case: claude absent, no flag -> auto-skip names the real blocker ---
make_sandbox
rm -f "$BIN/claude"
run --
assert_rc       "auto-skip: exit 0" 0
assert_contains "auto-skip: CLI-only message" "CLI-only mode"
assert_absent   "auto-skip: no plugin restart line" "Restart Claude Code"
assert_contains "auto-skip: names missing claude" "claude CLI was not found"
assert_absent   "auto-skip: no requested remedy" "Re-run the installer without --no-plugin"
rm -rf "$SANDBOX"

# --- Case: ETHOS_NO_PLUGIN=0 is ignored (only =1 skips) ---
make_sandbox
run ETHOS_NO_PLUGIN=0 --
assert_rc       "ETHOS_NO_PLUGIN=0: exit 0" 0
assert_absent   "ETHOS_NO_PLUGIN=0: not skipped" "CLI-only mode"
assert_contains "ETHOS_NO_PLUGIN=0: plugin path" "is ready!"
rm -rf "$SANDBOX"

# --- Case: unknown flag ---
make_sandbox
run -- --no-plguin
assert_rc       "unknown flag: exit 2" 2
assert_contains "unknown flag: reports option" "unknown option: --no-plguin"
rm -rf "$SANDBOX"

# --- Case: --help ---
make_sandbox
run -- --help
assert_rc       "--help: exit 0" 0
assert_contains "--help: prints usage" "install the ethos CLI"
rm -rf "$SANDBOX"

# --- Case: default (claude + git present, no flag) -> plugin installed ---
make_sandbox
run --
assert_rc       "default: exit 0" 0
assert_contains "default: claude detected" "claude CLI found"
assert_contains "default: plugin path message" "is ready!"
assert_contains "default: plugin restart line" "Restart Claude Code"
assert_absent   "default: not CLI-only" "CLI-only mode"
rm -rf "$SANDBOX"

if [ "$fails" -eq 0 ]; then
  printf '\nall cases passed\n'
  exit 0
fi
printf '\n%d assertion(s) failed\n' "$fails"
exit 1
