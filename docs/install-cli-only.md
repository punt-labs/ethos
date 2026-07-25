# CLI-only install — skipping the Claude Code plugin (m-2026-07-24-026)

Status: Shipped. Operator-ratified 2026-07-25 and implemented in
`install.sh` (`--no-plugin` / `ETHOS_NO_PLUGIN=1`). The ADR is DES-063 in
`DESIGN.md`; this doc is the full design record it ratifies, and the
"Punt-kit standard draft" section below graduated to the punt-kit
`install-cli-only` standard.

## Problem

`install.sh` does two jobs in one run: it installs the **ethos CLI**
(binary, `~/.local/bin` on PATH, identity dir, seed content, per-repo
`ethos enable`, `doctor`) and it registers the **Claude Code plugin**
(marketplace add/update, SSH→HTTPS fallback, `claude plugin install`).
The CLI is harness-neutral. The plugin is Claude-Code-only.

Two audiences want the first job without the second:

- **(a) Non-Claude harnesses** — Codex, Cursor, a plain terminal. There
  is no plugin surface to install. ethos serves them via the CLI, MCP
  (`ethos serve`), and the filesystem (DES-061 makes sessions
  harness-neutral).
- **(b) Enterprise-policy Claude users** — `claude` is present and
  working, but org policy blocks plugin/marketplace installation. They
  use ethos fine via the CLI.

`install.sh` already handles the **capability-absent** case. Step 1 sets
an internal `SKIP_PLUGIN=1` when the `claude` CLI is missing or when
`git` is missing; the marketplace-register, SSH-fallback, and
plugin-install steps are gated on `[ "$SKIP_PLUGIN" = "0" ]`. So audience (a)
with no `claude` on PATH is already covered.

The gap is two-fold:

1. **No explicit operator-driven skip.** Audience (b) has `claude`
   present and working, so the auto-skip never fires — the installer
   proceeds to `claude plugin install` and fails on the policy block.
   There is no way to say "install the CLI, skip the plugin, on purpose."
2. **No argument passing through `curl … | sh`.** The documented install
   is `curl -fsSL …/install.sh | sh` (see the README quick-start). A piped
   script parses no arguments today — there is nowhere to put a flag.

A third, smaller gap: when the auto-skip *does* fire, the final message
still says `ethos is ready!` and `Restart Claude Code twice to activate
the plugin` — inaccurate when no plugin was
installed.

## Scope

`--no-plugin` skips **only** the plugin/marketplace steps (4, 5, 6).
Everything else runs unchanged:

| Step | Runs under `--no-plugin`? |
|------|---------------------------|
| 1 Prerequisites | yes |
| 2 Install binary | yes |
| 2.5 PATH in profile | yes |
| 3 Identity directory | yes |
| 4 Register marketplace | **skipped** |
| 5 SSH→HTTPS fallback | **skipped** |
| 6 Install plugin | **skipped** |
| 6b Seed starter content | yes |
| 6c `ethos enable` (per-repo) | yes |
| 7 Health check (`doctor`) | yes |

The flag maps exactly onto the existing `SKIP_PLUGIN=1`. No new gating
logic — the flag and env var become two more inputs that set the variable
Step 1 already owns.

## Decisions

Each decision states a recommendation for operator ratification.

### D1 — Flag name and semantics

**Recommendation: `--no-plugin`.**

Three candidates:

- `--cli-only` — overclaims. It reads as "install only the CLI," but
  hooks, the identity dir, PATH edits, seed, and `ethos enable` all still
  run. A user reading `--cli-only` would not expect their `CLAUDE.md` to
  gain an import line and their git hooks to be chained. The name
  describes the *audience*, not the *action*.
- `--skip-plugin` — accurate but imperative and slightly ambiguous ("skip
  it if already present?"). "Skip" is an implementation verb (it names the
  internal variable), not a user intent.
- `--no-plugin` — the GNU/POSIX `--no-<feature>` idiom for turning off a
  default-on feature (`--no-color`, `--no-verify`, `--no-cache`). Plugin
  installation *is* a default-on feature of the installer; `--no-plugin`
  turns it off. It states exactly what is disabled and nothing more, and
  it is the convention every operator already knows.

Semantics: `--no-plugin` sets `SKIP_PLUGIN=1`. Steps 4/5/6 are skipped;
every other step runs. It is idempotent and order-independent (it is a
boolean, not a value flag). Unknown flags are a usage error (exit 2) with
a one-line usage string — a piped installer must not silently ignore a
misspelled `--no-plguin` and install the plugin the user asked to skip.

### D2 — Argument passing through `curl … | sh`

**Recommendation: `sh -s -- --no-plugin`, plus a POSIX arg-parse loop at
the top of `install.sh`.**

`sh` reads a script from stdin when given `-s`; `--` ends `sh`'s own
option parsing; everything after `--` becomes the script's positional
parameters (`$1`, `$2`, …). This is POSIX (`sh(1)`, "Command-line
Arguments") and works identically in `dash`, `bash --posix`, and BusyBox
`sh`. The full CLI-only install one-liner:

```sh
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/<ref>/install.sh | sh -s -- --no-plugin
```

The default one-liner is unchanged:

```sh
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/<ref>/install.sh | sh
```

`install.sh` gains a small parse loop before Step 1 (illustrative — the
implementation mission writes it):

```sh
for arg in "$@"; do
  case "$arg" in
    --no-plugin) SKIP_PLUGIN_REQUESTED=1 ;;
    -h|--help)   usage; exit 0 ;;
    *)           printf 'install.sh: unknown option: %s\n' "$arg" >&2; usage >&2; exit 2 ;;
  esac
done
```

The loop sets a *request* flag; Step 1 folds it into `SKIP_PLUGIN`
(D3 defines precedence). The README quick-start documents both one-liners; the
website install snippet gains the CLI-only variant beside the default.

### D3 — Environment-variable equivalent

**Recommendation: `ETHOS_NO_PLUGIN=1`, honored equally with the flag.**

Some contexts are argument-hostile: CI that templates a bare
`curl … | sh`, corporate proxies that mangle the pipeline, config
systems that set env but cannot append operands. An env var covers them:

```sh
curl -fsSL …/install.sh | ETHOS_NO_PLUGIN=1 sh
```

Value semantics: skip when `ETHOS_NO_PLUGIN` equals exactly `1`. This
matches the internal `SKIP_PLUGIN=0/1` convention — one
true value, no truthy-string guessing, no locale surprises. Any other
value (including empty, `0`, `true`, `yes`) does **not** skip; document
`=1` as the only accepted form.

Precedence: the flag and the env var both express the same intent (skip),
and there is deliberately **no** counter-flag to force the plugin on (see
Rejected alternatives). So the rule is a simple OR:

```text
SKIP_PLUGIN = 1  if  --no-plugin present
                 or  ETHOS_NO_PLUGIN = 1
                 or  claude absent            (existing auto-skip)
                 or  git absent               (existing auto-skip)
             = 0  otherwise
```

Explicit request (flag or env) and capability-absence auto-skip cannot
conflict — both drive the variable to 1. There is no path that forces the
plugin on over an auto-skip, which is correct: you cannot install a plugin
without `claude`.

### D4 — Auto-detect the policy block, or require the explicit flag?

**Recommendation: require the explicit flag/env. Do NOT probe
`claude plugin` to auto-detect an enterprise policy block.**

Capability-absence (no `claude`, no `git`) is a clean binary signal:
`command -v` is either present or not, and the meaning is unambiguous.
The installer already auto-skips on it, and that stays.

A policy block is not a clean signal. `claude plugin marketplace add`
returning non-zero is indistinguishable from a transient network failure,
a marketplace-repo outage, an auth hiccup, or a genuine bug. Auto-skipping
on *any* plugin-command failure would mask real install failures as
"probably policy" — the exact silent-absence anti-pattern DES-059 was
written to eliminate. The current Step 4/6 behavior (`fail` loudly on a
plugin error) is deliberate and correct for the non-policy audience.

The operator who works under a plugin-blocking policy *knows it* and says
so with `--no-plugin`. That is one word, reviewable, and unambiguous —
strictly better than the installer guessing from an error code.

**Second half — clearer auto-skip message: YES.** When `SKIP_PLUGIN=1`
for *any* reason (explicit or auto), the installer must not print the
plugin-centric success text. Unify the final message on the
`SKIP_PLUGIN` flag, not on the specific cause (D5). Today the
claude-absent path still prints `Restart Claude Code twice to activate
the plugin`, which is wrong; that line is emitted only when a
plugin was actually installed.

### D5 — Skip messaging

**Recommendation: a distinct CLI-only success block, gated on
`SKIP_PLUGIN=1`, replacing the plugin-centric tail.**

Accuracy is the whole point of this feature. On skip, the installer tells
the user (1) the CLI is fully installed and works, and (2) how to proceed
without the plugin. Proposed text (illustrative):

```text
✓ ethos CLI installed (CLI-only mode — Claude Code plugin skipped)

The CLI is fully functional — via the command line, MCP (`ethos serve`),
and the filesystem. To get started:

  ethos setup                                        # create your identity + repo config
  eval "$(ethos session start --persona <handle>)"   # open a session (any harness)
  ethos whoami                                        # confirm your identity

The Claude Code plugin was skipped. To add it later, re-run the installer
without --no-plugin.
```

Grounding in the settled session/setup design:

- `ethos setup` works standalone on a fresh machine — the installer ran
  `ethos seed` first (Step 6b), and DES-060 makes the seeded attributes
  resolve with no bundle active.
- `eval "$(ethos session start --persona <handle>)"` is the DES-061
  harness-neutral entry point: it mints a session, exports
  `ETHOS_SESSION` (and `ETHOS_AGENT_ID` with `--persona`) into the calling
  shell, and folds the first `iam`. This is how audience (a) and (b)
  open a session with no SessionStart hook.
- If the install ran inside a work tree, Step 6c already ran
  `ethos enable`, so the git hooks and audit seal are in place (DES-059);
  the message need not repeat it, but a one-line "hooks enabled in this
  repo" from `enable` itself already prints above.

The default (plugin-installed) success block is unchanged, including
`Restart Claude Code twice to activate the plugin`.

### D6 — Interaction with `ethos enable` and `ethos setup`

**Recommendation: neither `enable` nor `setup` needs a parallel skip.
`--no-plugin` is scoped to `install.sh`'s marketplace/plugin steps only;
Steps 6b/6c run unchanged.**

- **`setup`** writes config + identity and generates `.claude/agents/`
  files. Config and identity are harness-neutral. The `.claude/agents/`
  files are consumed only by Claude Code, but generating them is harmless
  for a non-Claude harness (inert files) and *required* for audience (b),
  the enterprise Claude user who has no plugin but still runs Claude Code.
  `setup` runs unchanged. No `setup --no-plugin`.
- **`enable`** deposits the vendored guide + `@`-import line, writes the
  `enabled` marker, and chains the DES-058 seal and DES-054 trailer git
  hooks. The **git hooks are the audit backbone** and are fully
  harness-neutral — they must run in CLI-only mode; skipping them would
  strip the audit trail the product exists to provide. The vendored guide
  and `@`-import are cheap and harness-agnostic (Claude Code reads the
  import; other harnesses ignore an unknown line in `CLAUDE.md`). `enable`
  runs unchanged. No `enable --no-plugin`.

`enable` and `setup` never install the plugin — only `install.sh` does —
so the flag has no home on them. It lives solely on `install.sh`.

**Flagged for follow-up (out of scope):** for a *pure* non-Claude harness
such as Codex, the `@`-import target is `CLAUDE.md`, but Codex reads
`AGENTS.md`. Making `enable`'s deposit harness-aware (e.g.
`enable --harness codex`) is a separate concern from the plugin-skip
flag and should be beaded independently. `--no-plugin` deliberately does
not touch it — narrow scope keeps the flag correct for audience (b), who
*do* run Claude Code.

## Punt-kit standard draft

This section is written to be lifted verbatim into
`punt-kit/standards/` (proposed: `standards/install-cli-only.md`, or a
section within `standards/shell.md`). It is the canonical shape every
punt tool's `install.sh` must implement identically.

---

### Standard: CLI-only install (`--no-plugin`)

Every punt tool whose `install.sh` installs both a CLI and a Claude Code
plugin MUST offer a way to install the CLI and skip the plugin. It is
implemented identically across tools so an operator learns it once.

**Flag.** The flag is `--no-plugin`. It skips **only** the Claude Code
marketplace-register and plugin-install steps. Every other step — binary
download/build, PATH setup, tool directories, seed content, per-repo
`enable`, and the final health check — runs unchanged. Unknown flags are
a usage error (exit 2).

**Environment variable.** The env var is `<TOOL>_NO_PLUGIN` (uppercased
tool name, e.g. `ETHOS_NO_PLUGIN`, `VOX_NO_PLUGIN`). It skips the plugin
when set to exactly `1`. Any other value is ignored.

**Piped invocation.** Because the tool is installed via
`curl … | sh`, the installer MUST parse arguments so both forms work:

```sh
curl -fsSL …/install.sh | sh -s -- --no-plugin      # flag form
curl -fsSL …/install.sh | <TOOL>_NO_PLUGIN=1 sh      # env form
```

The installer parses `"$@"` with a POSIX `case` loop before doing any
work.

**Skip resolution.** A single internal boolean gates the plugin steps.
It is set to "skip" when the flag is present, OR the env var equals `1`,
OR a required capability (`claude`, `git`) is absent. There is no
counter-flag to force the plugin on; you cannot install a plugin without
`claude`.

**Skip semantics.** Skipping is scoped to marketplace-register + plugin
-install. It MUST NOT skip the binary, PATH edits, directories, seed, or
per-repo enablement. A tool's per-repo `enable`/`setup` verbs are
unaffected and gain no parallel flag — the plugin flag lives only on
`install.sh`.

**Success messaging.** On skip, the final message MUST state that the CLI
is installed and works, and MUST NOT print plugin-specific instructions
(e.g. "Restart Claude Code to activate the plugin"). The message names
the next CLI steps (`<tool> setup`, session start) and how to add the
plugin later. The message is gated on the skip boolean, not on the reason
for skipping, so the capability-absent auto-skip and the explicit skip
print the same accurate block.

**No policy auto-detection.** The installer MUST NOT probe the plugin
command to guess an enterprise policy block and skip on failure — a
plugin-command error is indistinguishable from a transient failure, and
guessing masks real failures. Capability-absence (`command -v`) is the
only auto-skip signal; a policy block requires the explicit flag/env.

#### Conformance checklist

- [ ] `--no-plugin` flag parsed from `"$@"`; unknown flags exit 2 with usage.
- [ ] `<TOOL>_NO_PLUGIN=1` env var honored identically to the flag.
- [ ] `sh -s -- --no-plugin` and `<TOOL>_NO_PLUGIN=1 sh` both work over `curl … | sh`.
- [ ] Skip is scoped to marketplace + plugin steps only; binary, PATH, dirs, seed, enable, doctor all still run.
- [ ] Single boolean OR-combines flag, env, and capability-absence auto-skip.
- [ ] No counter-flag to force the plugin on.
- [ ] On skip, success message is CLI-only accurate; no "restart to activate plugin" line.
- [ ] Auto-skip (missing `claude`/`git`) prints the same CLI-only message as the explicit skip.
- [ ] No auto-detection of policy block via probing the plugin command.
- [ ] README/website document both the default and the `--no-plugin` one-liner.

---

## Rejected alternatives

- **A separate `install-cli.sh` script.** Two scripts sharing ~90% of
  their logic drift — the DES-059 "two copies drift" lesson (the v4.1.1
  seal-chain bug lived in a duplicated shell copy). One script with a
  boolean flag has one code path to test and maintain.
- **Post-install `<tool> plugin remove`.** Installs the plugin, then
  uninstalls it. Wasteful, racy, leaves the marketplace registered, and —
  fatally — requires `claude plugin install` to *succeed first*, which is
  exactly what fails for the enterprise-blocked audience. It solves
  audience (a) badly and audience (b) not at all.
- **Making CLI-only the default.** Breaks the happy path for the
  majority — Claude Code users who want the plugin — and would require
  *them* to pass a flag for the common case. The default one-liner must do
  the full install; opting out is the minority action, so it takes the
  flag.
- **Auto-detecting the policy block** by probing `claude plugin` and
  skipping on error. Fragile and masks real failures (D4). Capability
  -absence is the only signal clean enough to auto-skip on.
- **`--cli-only` as the flag name.** Overclaims: hooks, dirs, PATH, and
  `enable` still run, so "CLI only" is inaccurate. `--no-plugin` names the
  action, not the audience (D1).
- **A truthy env-var parser** (`true`/`yes`/`on`/non-empty). Locale- and
  convention-dependent, and inconsistent with the installer's existing
  `0/1` internal flag. One accepted value (`1`) is unambiguous.
