# Thin-shim git hooks: the binary owns seal and trailer policy

**Status**: Implemented on branch `feat/hook-shim` (DES-083). Bead `ethos-1xp6`.

Read first: DES-055 (exit 2 blocks), DES-058 as amended by DES-079,
DES-054 phase 3, DES-061 R4, DES-074, DES-081 (moves storage, not the hook
boundary), `docs/design-hook-drift-detection.md`,
`docs/spec-hook-gates.tex`, both hook scripts, `internal/githook`,
`internal/enable/refresh.go`, `internal/doctor/{doctor,sandbox}.go`.

## 1. Problem

`ethos enable` copies `plugin/hooks/pre-commit.sh` into
`.git/hooks/pre-commit` as a marker-fenced section. The copy contains the
seal policy: which `ethos audit seal` flags to pass and what runs after
it. A policy change reaches a repo only when something rewrites that
copy. v4.21.0 made `--commit-context` the flag that lets a blame-mode
archival error warn. Repos enabled before 4.21 kept a section without the
flag, so every commit failed closed on legacy audit data.

PR #583 made SessionStart rewrite a stale section. That is a repair loop,
not a fix: each release leaves a window before the next SessionStart,
shared or tracked `core.hooksPath` hooks are skipped by design, and drift
is the normal result of a release. The structural fix puts nothing in
`.git/hooks` that a release needs to change. The section becomes a shim
that finds the binary and calls it; a new binary takes effect on first
use.

## 2. The shim line

A step stays in shell only if it must work with the binary absent, old,
or broken.

| Stays in the shim | Why |
|---|---|
| `_host_status=$?` capture and `exit "$_host_status"` on every pass path | Chaining after a foreign hook; the binary cannot know it |
| Dormant fast path | A dormant repo must pay no probe and cannot be blocked by one |
| `_git` trace-clearing wrapper, used for `rev-parse` only | The probe runs before any binary is trusted |
| Marker gate: root validation, dangling-link and non-directory checks, marker absent means pass | Gate decides whether ethos is in play at all |
| Binary resolution (PATH, then `$HOME/.local/bin/ethos`) and "enabled but not installed" blocks | Only the shell can report a missing binary |
| commit-msg: `$1` empty or not a file means pass | No file to edit, no need to start the binary |

| Moves to the binary | Today |
|---|---|
| `audit seal --commit-context` and `provenance verify-index --staged`, in that order, with exit 2 on either failing | pre-commit.sh lines 156-163 |
| Trailer decision: `MISSION_ID`/`DELEGATION_ID` env, else session resolution, else nothing; the `Usage:` help-output guard becomes unnecessary | commit-msg.sh lines 171-206 |

### Shim text

The shims differ only in the `_hook` line and the tail. The first
comment line is the frozen ident, so `githook.InstalledSection` still
finds every existing section. Tags and idents do not change.

```sh
#!/bin/sh
# hooks/pre-commit.sh — Seal pending live audit lines into the local archive.
# Thin shim (DES-083): finds the binary and delegates; policy lives in
# `ethos hook pre-commit`. Passes through unless ethos is enabled here.
_host_status=$?
_hook=pre-commit
_git() {
  (
    for _v in $(env | sed -n 's/^\(GIT_TRACE[A-Za-z0-9_]*\)=.*/\1/p'); do
      unset "$_v"
    done
    unset GIT_CURL_VERBOSE
    GIT_TRACE=0 GIT_TRACE2=0 GIT_TRACE2_PERF=0 GIT_TRACE2_EVENT=0
    export GIT_TRACE GIT_TRACE2 GIT_TRACE2_PERF GIT_TRACE2_EVENT
    exec git "$@"
  )
}
_ethos_dormant=1
for _component in .punt-labs .punt-labs/ethos; do
  if [ -L "$_component" ]; then
    _ethos_dormant=""
  elif [ -e "$_component" ] && { [ ! -d "$_component" ] || [ ! -r "$_component" ] || [ ! -x "$_component" ]; }; then
    _ethos_dormant=""
  fi
done
if [ -e .punt-labs/ethos/enabled ] || [ -L .punt-labs/ethos/enabled ]; then
  _ethos_dormant=""
fi
if [ -n "$_ethos_dormant" ] && [ -e .git ]; then
  exit "$_host_status"
fi
if ! REPO_ROOT=$(_git rev-parse --show-toplevel); then
  printf 'ethos: %s: git rev-parse --show-toplevel failed\n' "$_hook" >&2
  exit 1
fi
case "$REPO_ROOT" in
/*) ;;
[A-Za-z]:/*)
  case "$(uname -s 2>/dev/null)" in
  MINGW* | MSYS* | CYGWIN*) ;;
  *) _revparse_bad=1 ;;
  esac
  ;;
*) _revparse_bad=1 ;;
esac
case "$REPO_ROOT" in
*"
"*) _revparse_bad=1 ;;
esac
if [ -n "${_revparse_bad:-}" ] || [ ! -d "$REPO_ROOT" ]; then
  printf 'ethos: %s: git rev-parse --show-toplevel did not print one absolute directory: %s\n' "$_hook" "$REPO_ROOT" >&2
  exit 1
fi
_ethos_path="$REPO_ROOT/.punt-labs"
for _component in "$_ethos_path" "$_ethos_path/ethos" "$_ethos_path/ethos/enabled"; do
  if [ -L "$_component" ] && [ ! -e "$_component" ]; then
    printf 'ethos: %s: %s is a dangling link; repair or remove it\n' "$_hook" "$_component" >&2
    exit 1
  fi
  [ -e "$_component" ] || break
  case "$_component" in
  */enabled)
    if [ ! -f "$_component" ]; then
      printf 'ethos: %s: the enabled marker %s is not a regular file\n' "$_hook" "$_component" >&2
      exit 1
    fi
    ;;
  *)
    if [ ! -d "$_component" ] || [ ! -r "$_component" ] || [ ! -x "$_component" ]; then
      printf 'ethos: %s: %s is not a readable directory\n' "$_hook" "$_component" >&2
      exit 1
    fi
    ;;
  esac
done
[ -f "$REPO_ROOT/.punt-labs/ethos/enabled" ] || exit "$_host_status"
if command -v ethos >/dev/null 2>&1; then
  ethos_bin="ethos"
elif [ -x "$HOME/.local/bin/ethos" ]; then
  ethos_bin="$HOME/.local/bin/ethos"
else
  printf 'ethos: %s: ethos is enabled here but not installed; the commit is not sealed\n' "$_hook" >&2
  exit 1
fi
"$ethos_bin" hook pre-commit || exit 2
exit "$_host_status"
```

The commit-msg shim sets `_hook=commit-msg` and ends with:

```sh
[ -z "$1" ] && exit "$_host_status"
[ -f "$1" ] || exit "$_host_status"
"$ethos_bin" hook commit-msg "$1" || exit 2
exit "$_host_status"
```

Both shims exit 2 on binary failure (operator ruling, DES-055's "ethos blocked
this" code). On failure the shipped shims also probe `--help` and print `upgrade
ethos: make install` when the binary does not know the subcommand.

### CLI contract

`ethos hook pre-commit` (`cobra.NoArgs`) and `ethos hook commit-msg
<message-file>` (`ExactArgs(1)`) are new children of the hidden `hook`
group in `cmd/ethos/hook.go`.

| | `hook pre-commit` | `hook commit-msg <file>` |
|---|---|---|
| Args | none | the message file git passes |
| Environment read | `GIT_INDEX_FILE` (partial commits), config snapshot | `MISSION_ID`, `DELEGATION_ID`, `ETHOS_SESSION`, Claude process tree |
| Does | `sealRepository` with commit-context on (the code `audit seal` runs), then verify-index on the staged index | env else `resolve.SessionID` sidecars, then `hook.ApplyCommitMessage` |
| Exit 0 | sealed, nothing pending, or blame-mode retained-data warning | trailers added, or none to add |
| Exit 2 | invalid policy, non-excepted archival failure, full-mode publication failure, cannot locate the checkout or store (outside a repository, a refused root), verify-index failure (`failClosed`) | usage error |
| Exit 1 | none | trailer render or install failure |
| stderr | what `audit seal` prints today, unchanged; a verify-index failure is prefixed `ethos: provenance verify-index:` | `ethos: commit-trailers: <err>; no trailer added` when under Claude Code with no resolvable session (DES-074), unchanged; `ethos: commit-message: <err>` on exit 1 |

Neither reads stdin; both print nothing on stdout. The shim's `_git` no
longer protects the binary's git calls, so a test must show
`GIT_TRACE=/dev/stdout` changes neither subcommand's output. `audit seal`
and `provenance verify-index` stay public; the new subcommands call the
same functions.

## 3. Compatibility

**New binary, old section.** The old section calls `audit seal
--commit-context` and `provenance verify-index --staged`, which keep
working, and the policy those select lives in the binary. #583's
SessionStart refresh rewrites the section to the shim. A repo refresh
skips (shared or tracked `core.hooksPath`) keeps the old section and
`doctor` reports it stale.

**Old binary, new shim.** The shim calls `hook pre-commit`. A binary
without it answers in one of two ways. From v4.9 (#415) the `hook` group
rejects an unknown subcommand: non-zero, `unknown command` on stderr. Before
that the group had no `RunE` or `Args`, so v4.8.0 and earlier print the group
help on stdout and exit 0. Both subcommands print nothing on stdout, so the
shim treats a non-zero exit or any stdout as failure, and the commit blocks
with a readable cause rather than passing unsealed. Three things cover this: (a) a binary containing #583 refreshes
at SessionStart, sees the shim differs from its own section, and rewrites
it to the form that binary expects; (b) `doctor` from the old binary
reports the section stale; (c) the fail-closed block. Binaries of
different ages alternating on one machine make refresh flip the section.
Each state works with the binary that wrote it and the other blocks
loudly, which is acceptable. A later change that needs the shim to pass
something new adds a new subcommand name, never a flag on these two.

## 4. Doctor and refresh

`RefreshHooksTo` and `CheckHookCurrency` are unchanged in mechanism: the
shim is still an embedded section compared by sha. The sha now moves only
when the shim moves, so refresh is migration machinery, not the
policy-delivery path.

- `hooks.PreCommit` and `hooks.CommitMsg` embed the new shims. Tags and
  idents are frozen and untouched.
- `HookSpec.InvokeArgs` becomes `{"hook","pre-commit"}` and
  `{"hook","commit-msg"}`. The sandbox stub records argv, so
  `hookInvocationObserved` now proves the shim reaches the binary with the
  new subcommand. An old copied section calls `audit seal`, is not
  observed, and reads "present but no active call (stale)": the right
  verdict. `looksLikeInvocation` takes the same args.
- `StaleBlocksCommit` stays true for the seal spec: a pre-shim section
  can still block commits. It can be deleted once no supported release
  writes one.
- New doctor line "Hook binary" runs `ethos hook pre-commit --help` and
  FAILs when the subcommand is absent: the old-binary detector.

## 5. Spec

`docs/spec-hook-gates.tex` models only the SubagentStart verifier gate
and the PreToolUse write decision. It has no schema for the git hooks, so
no existing schema changes and none is made false. The shim line has no
model at all. Add a section to that file with the schemas below (sketch
notation; written in fuzz syntax, type-checked, and animated with ProB at
implementation).

```text
Binary ::= absent | old | current
Tree   ::= dormant | enabled
Verdict ::= passHost | block

┌ ShimState ────────────
│ tree : Tree
│ bin  : Binary
│ hostFails : B           -- $_host_status ≠ 0
└────────────────────────

ShimVerdict : ShimState → Verdict
  ∀ s : ShimState •
    (s.tree = dormant ⇒ ShimVerdict s = if s.hostFails then block else passHost)
  ∧ (s.tree = enabled ∧ s.bin = absent ⇒ ShimVerdict s = block)
  ∧ (s.tree = enabled ∧ s.bin = old ⇒ ShimVerdict s = block)
  ∧ (s.tree = enabled ∧ s.bin = current ⇒
       ShimVerdict s = if policyBlocks s ∨ s.hostFails then block else passHost)
```

Theorems: dormant never reaches the binary; enabled with no usable binary
never passes; `policyBlocks` ignores installed text (the incident).

## 6. Tests

Each case runs a real `git commit` in a scratch repo against a binary
built from the branch, not a stub, with `HOME` and git config isolated as
in `sandbox.go`.

| Case | Both hooks | Expected |
|---|---|---|
| Dormant, shim installed, no binary on PATH | pre-commit, commit-msg | commit succeeds; binary never started |
| Enabled, invalid config | pre-commit | commit blocked, exit 2, stderr names the config |
| Enabled, blame archival error | pre-commit | commit succeeds, warning on stderr, live data retained |
| Enabled, active mission | commit-msg | `Mission:` and `Delegation:` trailers in `git log` |
| Enabled, `GIT_TRACE=/dev/stdout` | both | same result and output as untraced |
| Enabled, no binary on PATH or in `$HOME/.local/bin` | both | commit blocked, `enabled here but not installed` |
| Enabled, binary predating the subcommand (a build of 93c9f82) | both | commit blocked, `unknown command` on stderr |
| Old copied section plus new binary | both | still seals and adds trailers; refresh rewrites it to the shim |

Unit tests: exit codes per subcommand; `CheckHookCurrency` on the new shims
(CRLF included); `hookInvocationObserved` with the new `InvokeArgs`; the
"Hook binary" line.

## 7. Rejected alternatives

- **Keep copying, version the section.** The stamp must be bumped by hand
  on the releases that change policy, and a refresh must still run before
  the change applies. `design-hook-drift-detection.md` already rejected
  stamps.
- **SessionStart refresh as the only mechanism.** This is #583. It cannot
  reach a shared or tracked hooks directory, runs only inside a Claude
  session, and leaves a window per release.
- **Policy in a config file.** The shell would need a second parser and
  would bypass the binary's single validated configuration snapshot
  (DES-079); the file can go stale like the section did.
- **`exec` the binary.** Breaks host-status chaining: a section chained
  after foreign content cannot `exec` away the host's status.

## 8. Implementation

Commit order, each commit passing `make check`:

1. `cmd/ethos/hook_precommit.go`, `cmd/ethos/hook_commit_msg.go`, with
   tests and `hook_test.go` additions: the two subcommands, exit codes per
   the contract, registered in `hook.go`. Existing commands untouched, so
   old sections keep working at this commit.
2. `plugin/hooks/{pre-commit,commit-msg}.sh`, `plugin/hooks/embed.go`
   comment, and the `internal/enable` tests that read the embedded text.
3. `internal/doctor/doctor.go` (`InvokeArgs`, "Hook binary"), its tests.
4. Real-`git commit` cases from section 6 in the `check-storage` harness,
   including the old-binary build.
5. `docs/spec-hook-gates.tex` section, DES-083 in `DESIGN.md`,
   `docs/enable-disable.md`, `CHANGELOG.md`.

`internal/githook` and `internal/enable/refresh.go` need no code change.
