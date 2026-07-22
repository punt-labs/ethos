# `ethos enable` / `ethos disable`: mapping the tool-enable-disable standard onto ethos

**Status**: Draft. Bead `ethos-ik4j`. Mission `m-2026-07-22-001`.
Conforms to punt-kit `standards/tool-enable-disable.md` (§2.1–2.13) and
`standards/punt-labs-dir.md` (§7 zones, §9 migration). Section numbers
below cite those two standards unless prefixed with a doc name.

This is a **conformance** design. The external contract — the import
line, the `enabled` marker, the vendored user guide, the write rules — is
fixed by the standard. The design decisions here are the *ethos-internal*
mapping: which paths are carved into which zone, which package owns which
correctness rule, how the v4.1.1 hook-chaining logic ports from shell into
the binary, and how `disable` unwinds it without touching repo-owned data.

## Problem

The org standard says every repo-scoped CLI ships `enable` / `disable`
(cli.md § Required Subcommands; §2.3). `enable` deposits a vendored
`.punt-labs/<tool>/CLAUDE.md`, writes the `enabled` marker, appends one
canonical `@`-import line to the repo `CLAUDE.md`, and registers any
repo-scoped hooks; `disable` reverses all four, non-destructively (§2.9).

Ethos does not have these verbs. Today three things stand in for them,
each wrong under the new standard:

1. **`install.sh` owns per-repo enablement.** When run inside a work tree,
   the v4.1.1 installer chains the DES-058 seal and DES-054 trailer git
   hooks into the repo's hooks directory (`install.sh:441–471`). This is
   the exact anti-pattern the standard rejects: machine-scoped `install`
   reaching into a specific repo's per-repo state. §2.13 makes `enable`
   the per-repo verb; `install` is machine scope only.
2. **No vendored guide, no import line, no marker.** Ethos ships no
   agent-facing `CLAUDE.md` (§2.5), registers no `@.punt-labs/ethos/CLAUDE.md`
   line (§2.4), and writes no `enabled` marker (§2.7). A repo cannot signal
   "ethos is on here" except by the presence of hooks — which the standard
   forbids as the enabled signal (§2.7).
3. **`doctor` fails every repo without the seal hook.** `CheckSealHook`
   (`internal/doctor/doctor.go:140`) returns FAIL for any repo whose
   pre-commit hook lacks an active seal call — including repos where ethos
   is not enabled at all. §2.11 requires hook checks to key on the
   `enabled` marker, so a dormant or never-enabled repo must pass.

The ethos-specific hazard that makes this more than a mechanical port:
**`.punt-labs/ethos/` already holds repo-owned content.** In this repo it
carries `identities/`, `teams/`, `roles/`, `agents/`, `personalities/`,
`talents/`, `missions/`, `missions.jsonl`, `sessions/` (sealed audit
chunks), and `ethos.yaml`. The standard's model — the tool owns
`.punt-labs/<tool>/` and overwrites it **wholesale** on every enable
(§2.2) — would destroy all of that. The vendored zone must be carved down
to the two files ethos actually deposits, and everything else must be
provably out of reach of the overwrite. punt-labs-dir §7 provides the
carve-out mechanism (four zones + a vendored-zone manifest); this design
applies it to ethos's dense subtree.

## Chosen approach

`enable` and `disable` are thin cobra commands in `cmd/ethos/` over three
new internal packages, each owning one correctness contract. Nothing in
the enable path reads, merges, or overwrites repo config or seal-managed
data; the vendored zone is a two-file manifest and the collision rule
(§7) guards everything else.

### Package layout

| Package | Responsibility | Ports / reuses |
|---------|---------------|----------------|
| `internal/claudemd/` | The §2.4 import-line writer: exclusive lock, atomic temp+rename, byte-preserving host-EOL append, terminator-insensitive idempotent match, fenced/indented code-block exclusion, symlink-resolving, mode-preserving. Pure, host-file-agnostic. | Ports the *correctness* of vox `GlobalClaudeImports` (`punt-labs/vox`, `src/punt_vox/claude_md.py`) into Go (§2.4). Adds the exclusive lock vox lacks. |
| `internal/githook/` | Git-hook chaining: marker sections, line-2 wholly-ours ID, non-shell skip-and-warn, unterminated-marker abort, symlink-target resolve, mktemp-fail-loud, host-status preservation, hooksPath/worktree resolution. `Chain(dest, src, tag, ident)` and `Unchain(dest, tag)`. | Ports `install.sh`'s `install_hook` / `emit_section` / `write_marker_form` / `is_shell_hook` / `resolve_hooks_dir` into Go; **shares** the resolver with `doctor.gitHooksDir` (§8 below). Embeds `hooks/pre-commit.sh` and `hooks/commit-msg.sh` via `go:embed`. |
| `internal/enable/` | Orchestration: deposit the vendored zone from the manifest, write/delete the `enabled` marker, drive `claudemd` for the import line, drive `githook` for the two chained hooks, merge/remove the `.claude/settings.json` entries. Embeds the vendored user guide via `go:embed`. | New. Depends on the two packages above. |

CLI surface: `cmd/ethos/enable.go` and `cmd/ethos/disable.go`, both in the
`admin` group (root.go:30), Pattern-1 direct delegation (cli.md). Global
`--json` reports the deposited paths, marker state, import-line action,
and per-hook chain result.

### Why three packages, not one

The import-line writer and the hook-chainer are independent correctness
contracts with distinct edge cases (code-block false positives vs.
truncated-marker aborts). Each is portable and testable in isolation, and
`githook`'s resolver is already needed by `doctor`. Folding them into one
`enable` package would couple two unrelated failure surfaces and block the
doctor refactor. The output contract of each stage matches the next:
`claudemd` returns "line added / already present / removed"; `githook`
returns "installed / chained / refreshed / skipped-non-shell / aborted";
`enable` composes those into one result.

## Zone map

This is the load-bearing table. It carves `.punt-labs/ethos/` per §7 so
`enable` / `disable` touch only the Vendored and Marker zones and provably
never touch Config, Local, or seal-managed data.

| Path | Zone (§7) | Owner | `enable`/`disable` behavior |
|------|-----------|-------|-----------------------------|
| `.punt-labs/ethos/CLAUDE.md` | Vendored | ethos | Deposited wholesale by `enable`; left dormant by `disable` |
| `.punt-labs/ethos/.vendored-manifest` | Vendored | ethos | The §7 vendored-zone manifest (lists exactly the two vendored paths); deposited by `enable` |
| `.punt-labs/ethos/enabled` | Marker | ethos | Written by `enable`, deleted by `disable` (§2.7) |
| `.punt-labs/ethos/identities/` | Config | repo | **Never touched** |
| `.punt-labs/ethos/teams/` | Config | repo | **Never touched** |
| `.punt-labs/ethos/roles/` | Config | repo | **Never touched** |
| `.punt-labs/ethos/agents/` | Config | repo | **Never touched** |
| `.punt-labs/ethos/personalities/`, `talents/`, `writing-styles/` | Config | repo | **Never touched** |
| `.punt-labs/ethos.yaml` (today) → `.punt-labs/ethos/config.yaml` (planned, punt-labs-dir §9/§10 identity-pointer row = *Planned*) | Config | repo (via `setup`) | **Never touched** — enable interacts with neither the bare pointer today nor its future config-zone home |
| `.punt-labs/ethos/sessions/<date>-<sid>/audit-*.jsonl` | Seal-managed (audit-seal.md) | ethos audit | **Never touched** — sealed chunks are add-only, written by the seal, not by enable |
| `.punt-labs/ethos/missions/<id>/log-*.jsonl`, `missions.jsonl` | Seal-managed / tracked | ethos mission | **Never touched** |
| `.punt-labs/ethos/local/`, `*.local`, `*.local.*` | Local | user | **Never touched** |
| `.punt-labs/local/ethos/` | top-level local zone (punt-labs-dir §2) | ethos runtime | **Never touched** — live audit/mission writes, gitignored |
| `<repo>/CLAUDE.md` | host file | user | One import line added/removed (§2.4) |
| git hooks dir `pre-commit`, `commit-msg` | outside subtree | shared | Marker section chained/unchained (§2.8, DES-058/DES-054) |

`.claude/settings.json` is deliberately **absent** from this table: ethos
registers no repo-scoped settings entry today, so `enable`/`disable` do not
touch it. §2.8 remains available as future work — see "Deferred: `.claude/settings.json`" below.

**The carve-out is enforced by the manifest, not by good intentions.** Per
§7, `enable` writes **exactly** the vendored-zone-manifest set
(`CLAUDE.md` + `.vendored-manifest`) and removes any path in the previous
manifest but not the current one. The §7 collision rule makes this safe:
`enable` may overwrite only a path the *previous* manifest also listed; a
new-manifest path that already exists but is **not** in the previous
manifest — every identity, team, role, sealed chunk — is a **collision**
that errors and names both paths, depositing nothing, rather than
clobbering. Because the ethos manifest lists only two files and never an
identity/team/seal path, the overwrite is bounded to those two by
construction.

**Substantive issue flagged for leader review (see Open Questions #1):**
in consuming repos `.punt-labs/ethos` is a **gitlink** (submodule, mode
`160000`; punt-labs-dir §10 ethos(registry), status *Deprecating*). You
cannot deposit `.punt-labs/ethos/CLAUDE.md` or the `enabled` marker into a
foreign git repo. `enable` in a gitlinked repo cannot write the vendored
zone at all — the same `ethos-e29s` vendor-first dependency the audit seal
already carries. In *this* repo the subtree is inline-vendored, so ethos's
own repo is unaffected; the hazard is fleet-wide.

## Verb specifications

### `ethos enable`

Run from inside a repo. Idempotent; re-running is the upgrade path (§2.3).
Steps, in order:

1. **Resolve the repo root** (`resolve.FindRepoRoot`). Not in a work tree
   → exit 2 with `ethos: enable: not in a git repository`.
2. **Guard the gitlink case.** If `.punt-labs/ethos` is a gitlink
   (mode `160000` / a submodule), the vendored zone is unwritable. Per
   Open Question #1's recommended ruling: error with a vendor-first remedy
   (`ethos-e29s`) rather than silently writing nothing. (Alternative:
   defer like the seal — leader decides.)
3. **Deposit the vendored zone** from the embedded manifest, §7 semantics:
   write `.punt-labs/ethos/CLAUDE.md` and `.punt-labs/ethos/.vendored-manifest`
   wholesale; apply old-manifest-minus-new removal; **collision-error** on
   any new-manifest path outside the previous manifest (bootstrap: on the
   first manifest-aware run with no previous manifest, treat the new
   manifest's own paths as the previous set, but a collision with a
   Config-zone path errors unconditionally, §7 bootstrap).
4. **Write the marker** `.punt-labs/ethos/enabled` (§2.7). A zero-byte
   file is sufficient; the standard defines presence, not content.
5. **Add the import line** via `internal/claudemd`: append the canonical
   `@.punt-labs/ethos/CLAUDE.md` to `<repo>/CLAUDE.md` if absent, under the
   full §2.4 write contract (below). Never twice.
6. **Chain the git hooks** via `internal/githook`: chain the DES-058 seal
   section into `pre-commit` and the DES-054 trailer section into
   `commit-msg`, carrying all v4.1.1 protections (below). Both embedded
   scripts carry the §2.7 marker gate (see "The §2.7 marker gate" below).

Ethos registers no `.claude/settings.json` entry, so `enable` has no §2.8
step (see "Deferred: `.claude/settings.json`"). Exit 0 on success or clean
re-run. `--json` emits the per-step result.

#### The §2.4 import-line write contract (ported from vox `GlobalClaudeImports`)

`internal/claudemd` implements every clause of §2.4. The vox reference
(`claude_md.py`) already satisfies atomic / symlink / byte-preserving /
deterministic; the port adds the exclusive lock and the code-block scan.

- **Canonical string.** Exactly `@.punt-labs/ethos/CLAUDE.md` — forward
  slashes, no `./`, no trailing slash, no surrounding whitespace, one
  physical line. This is what `enable` writes, `disable` matches, and
  `punt audit` greps; it must be byte-identical across all 15 CLIs (§2.4).
- **Terminator-insensitive idempotent match.** Presence is decided by
  comparing the canonical line against each host line **net of its
  terminator** (strip trailing `\r`, `\n`, `\r\n` before comparing), so a
  CRLF host does not carry a spurious `\r` that defeats a byte-exact
  compare. `enable` appends only if absent; `disable` removes every match
  (collapsing an accidental duplicate to zero) (§2.4).
- **Code-block exclusion.** Both the presence scan and the removal ignore
  a matching line inside a fenced block (odd count of preceding fence
  delimiters — three or more backticks or tildes, optional info string,
  run need not span the line) or an indented block (line begins with a tab
  or ≥4 spaces). The canonical line is always written at column 0 with no
  info string, so it is top-level by construction (§2.4).
- **Exclusive lock.** Hold `flock` on the target (or a sibling lock file)
  for the whole read-modify-write. Atomic rename prevents a torn file; the
  lock prevents the lost-update race two parallel `enable` runs would
  otherwise hit (§2.4, and §2.8 for the shared-file rule that also covers
  `settings.json`).
- **Atomic, byte-preserving, host-EOL append.** Write a temp file in the
  target's own directory, then rename over the target; never
  truncate-in-place. Every byte outside the single import line is
  identical before and after across LF/CRLF/lone-CR (read and write with
  no newline translation). If the host does not end in a newline, add one
  before appending so the import is never glued to the user's last line.
  The appended line uses the host's existing EOL convention so it stays
  terminator-insensitively matchable on re-run (§2.4).
- **Symlink-resolving, mode-preserving.** If the target is a symlink
  (dotfile managers), write the real target and keep the link. Preserve an
  existing file's mode; a new file gets `0644` (§2.4).

#### The git-hook chaining contract (ported from `install.sh`)

`internal/githook.Chain` ports `install_hook` (`install.sh:73–155`) and
its helpers into Go, carrying **all** v4.1.1 protections. Nothing is
dropped in the port:

- **Marker sections.** Fresh install writes a shebang plus a
  `# --- BEGIN <tag> ---` … `# --- END <tag> ---` section (`emit_section`,
  `write_marker_form`). Tags: `ETHOS DES-058 SEAL` (pre-commit),
  `ETHOS DES-054 TRAILER` (commit-msg).
- **Idempotent upgrade.** Our section present → strip and re-append in
  place.
- **Positional line-2 wholly-ours ID.** A pre-marker standalone is
  positively identified by its header IDENT on **line 2** (not anywhere),
  which distinguishes our standalone from a `cat hook.sh >> hook` hybrid
  whose host content pushes our header mid-file. Standalone → replace with
  the marker form; hybrid → fall through to chain, preserving the host
  (`install.sh:98–109`).
- **Non-shell host skip-and-warn.** Chain only into a shell-family host
  (`sh`/`bash`/`dash`/`ksh`/`zsh`/`mksh`/`ash`, or no shebang — git runs
  it via `sh`). A Python/Node/binary host is left untouched with a
  warning; doctor then FAILs on the missing seal, the correct signal
  (`install.sh:116–119`).
- **Unterminated-marker abort.** A BEGIN with no matching END
  (hand-truncated) aborts rather than letting the strip pass delete
  everything after BEGIN (`install.sh:124–126`).
- **Symlink target resolve.** Operate on a symlinked hook's target so `mv`
  does not flatten the link; an unresolvable target aborts
  (`install.sh:79–88`).
- **mktemp-fail-loud.** A temp-file failure next to the target fails the
  install with a named error, never a silent skip (`install.sh:49–50, 128`).
- **Host-status preservation.** The appended section runs after the host's
  content falls through and preserves the host's fall-through exit status;
  the embedded `pre-commit.sh` / `commit-msg.sh` already capture `$?` and
  return it on passthrough. An unconditional trailing `exit`/`exec` on the
  host's last effective line (past trailing comments) bypasses the section
  and is warned (`install.sh:140–148`).
- **hooksPath / worktree resolution parity with doctor.** The hooks
  directory is git's own answer — `git rev-parse --git-path hooks` — which
  resolves a worktree's common dir and honors `core.hooksPath`, warning
  when that points inside the work tree (tracked file dirtied) or outside
  the repo (shared across repos) (`install.sh:173–190`). This is the same
  resolver `doctor.gitHooksDir` uses (`doctor.go:270–296`); the port makes
  it one shared function, so installer, `enable`, and `doctor` cannot drift.
  **The unified resolver keeps doctor's manual fallback, not install.sh's
  git-required form (REC-2).** `install.sh:174` returns empty when `git` is
  not on `PATH`; `doctor.go:274–295` falls back to reading `.git` /
  `commondir` by hand and still resolves a worktree's common `hooks`. The
  shared function keeps the manual fallback, so `enable` resolves the hooks
  directory in a git-less environment where doctor already succeeds —
  adopting the git-required form would regress that.

#### The §2.7 marker gate in the embedded hook scripts

Per §2.7, "hook shell gates test the marker, not the directory." Both
embedded scripts — `hooks/pre-commit.sh` and `hooks/commit-msg.sh` — gain
the normative gate as their first executable step, immediately after the
`$?` capture that preserves the host's fall-through status:

```sh
_host_status=$?
REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || exit "$_host_status"
[ -f "$REPO_ROOT/.punt-labs/ethos/enabled" ] || exit "$_host_status"
```

`REPO_ROOT` is resolved **inside the hook** via `git rev-parse
--show-toplevel` (worktree-safe: a worktree resolves to its own work-tree
root, and the marker lives in that root's `.punt-labs/ethos/`), not
baked in at install time. Gate absent-marker → exit with the captured host
status (`$?`), never a bare `exit 0`, so chaining after a host hook that
signals failure by fall-through is preserved even when ethos is dormant.
The gate sits before the `ethos` binary is ever resolved or invoked, so a
dormant repo does no ethos work at commit time.

This gate collides with the v4.1.1 convergence case (repos with hooks
chained but no marker); the migration section resolves it — SessionStart
writes the marker before the gate can suppress a seal.

### `ethos disable`

Run from inside a repo. Non-destructive (§2.9). Steps:

1. **Seal first, then strip.** Before removing the seal hook, run the
   equivalent of `ethos audit seal` so pending live lines are captured into
   tracked chunks. This resolves the tension between §2.9 ("deregister
   hooks") and the DES-058 invariant "every tool call is logged; no gaps,
   no off switch": disabling the seal hook is an off switch for *future*
   sealing, but it must not strand *already-written* live lines. See Open
   Question #2.
2. **Remove the import line** from `<repo>/CLAUDE.md` via `internal/claudemd`
   — remove every terminator-insensitive match, code-block-excluded, under
   the same write contract (§2.9 step 1).
3. **Delete the marker** `.punt-labs/ethos/enabled` (§2.9 step 2).
4. **Unchain the git hooks** via `internal/githook.Unchain`: strip our
   `ETHOS DES-058 SEAL` and `ETHOS DES-054 TRAILER` marker sections,
   preserving all host content. If stripping leaves a hook that is only our
   standalone (shebang + our section, nothing else), remove the file; if it
   leaves host content, keep the reduced host hook (§2.9 step 3). This is
   the inverse of `Chain` and is new — `install.sh` only ever appends.
5. **Leave the rest of `.punt-labs/ethos/` dormant** — the vendored guide,
   the manifest, and all Config/seal data stay on disk (§2.9). `--purge` is
   out of scope for this design; if added later it deletes only the
   Vendored and Marker zones, never Config or seal data.

Exit 0 on success or clean re-run (already-disabled is a no-op).

### The vendored user guide (§2.5)

`enable` deposits `.punt-labs/ethos/CLAUDE.md` — ethos's **agent-facing**
manual, "how an agent drives ethos, not how to develop ethos itself"
(§2.5, vox's opening-line precedent). It is **static content shipped with
the binary** (`go:embed` in `internal/enable`), the same guide everywhere,
no per-repo rendering. It is **not** this repo's developer `CLAUDE.md` and
**not** a reference dump. Imports stay shallow — no deep `@`-chains from
the guide (§2.5).

Proposed content outline, sourced from `README.md` and `AGENTS.md` usage
material (agent-facing, condensed):

```text
# Ethos

How an agent drives ethos — not how to develop ethos itself.

## Who am I
- `ethos whoami` / `ethos iam <persona>` — resolve and declare identity
- Session hooks inject your persona; restart Claude Code to regenerate
  `.claude/agents/<handle>.md` after a team change

## Delegation (missions)
- `ethos mission dispatch --worker <h> --evaluator <h> --write-set … --criteria …`
- `ethos mission show|log|results <id>`; `ethos mission close <id>`
- `ethos mission pipeline list|show|instantiate <name>`
- Commit-per-step; write-set is enforced at runtime

## Audit
- `ethos audit show --delegation <id>` — reconstruct a delegation's trail
- `ethos audit seal` runs at pre-commit; sealed chunks travel with the work
- `ethos audit quarantine` — the specified recovery for a corrupt chunk

## Session
- `ethos session` — current roster; `ethos session purge` — clear stale

## Gotchas
- Never run `make install` from inside Claude Code (running binary)
- Agent types are discovered at SessionStart — restart after adding one
- `ethos doctor` checks the seal hook only when ethos is enabled here
```

Final prose is a follow-up implementation task; this outline fixes scope
and sourcing.

## Three-state model and what doctor reports

Per §2.7, the marker `.punt-labs/ethos/enabled` is the enabled signal —
directory presence is not. The embedded hook scripts gate on the marker
(`[ -f "$REPO_ROOT/.punt-labs/ethos/enabled" ] || exit "$_host_status"`,
see "The §2.7 marker gate" above).

| State | `enabled` marker | Hook chained | Import line | `doctor` seal check |
|-------|------------------|--------------|-------------|---------------------|
| Enabled | present | yes | present | FAIL if seal hook missing/inactive |
| Dormant (disabled) | absent | no | absent | PASS (not enabled here) |
| Absent | absent | no | absent | PASS (not enabled here) |
| Gated-but-unenabled (transient) | absent | yes | absent | WARN (see below) |

**Required doctor change (conformance).** `CheckSealHook`
(`doctor.go:140`) currently FAILs any repo whose pre-commit hook lacks an
active seal call, including never-enabled repos. §2.11 requires the check
to key on the marker: FAIL only when `.punt-labs/ethos/enabled` is present
and the seal is missing/inactive; PASS (with "not enabled here") when the
marker is absent and no ethos hook is chained. Without this, every dormant
or unadopted repo fails doctor forever — the exact false-positive §2.7's
three-state model exists to prevent.

**Gated-but-unenabled: doctor WARNs.** A fourth state is reachable only
transiently — the seal hook is chained but the marker is absent. It occurs
mid-migration (v4.1.1 interim repos before their first post-adoption
session, resolved by SessionStart migration below) or after manual surgery
(someone deleted the marker but left the hook, or a partial `disable`). In
this state the gate makes the chained hook a no-op — it exits at the marker
check and never seals — so a green PASS would hide a hook that is present
but inert, while a FAIL would over-report a repo that is simply
converging. `doctor` therefore **WARNs**: "seal hook chained but ethos not
enabled here — run `ethos enable` to converge, or remove the stale hook."

**Residual window, stated plainly.** A v4.1.1-interim repo that has the
hooks chained but never runs another ethos session never writes the marker
(SessionStart is the trigger) and so, once the gate lands, never seals
again. This is **acceptable**: no session means no new audit lines are
produced, so there is nothing to seal — the seal only ever had work to do
when a session was active. The window closes the moment any session starts
(SessionStart migration writes the marker) or an operator runs
`ethos enable`. The only lines at risk are those a *prior* session left
unsealed in the local zone before the gate landed; those seal on the next
commit of a session that has since written the marker, and are covered by
`disable`'s seal-first step and the SessionEnd courtesy flush (DES-058).

## `install.sh` delegation

`install.sh` reverts to **machine scope only** (§2.13,
distribution.md § Installation Scope): download/build the binary, register
the marketplace plugin, seed global starter content, run `doctor`. The
per-repo hook install (`install.sh:421–471`) and its helpers
(`install_hook`, `resolve_hooks_dir`, `is_shell_hook`, `emit_section`,
`write_marker_form`, lines 18–190) are **deleted**. When `install.sh` runs
inside a work tree it calls `"$INSTALL_DIR/ethos" enable` for the cwd — the
binary already exists by that point (Step 2 installs it before the hook
steps run today), so the delegation is safe.

**Recommendation: delete the shell functions; do not keep them for
curl-bootstrap parity.** The drift risk is the deciding factor: two
implementations of marker-section chaining (shell in `install.sh`, Go in
`internal/githook`) will diverge — the v4.1.1 seal-chain fix (`ethos-2ol1`,
commit `01a90ec`) is precisely a bug that lived in the shell copy. One
implementation in Go, called by both `install.sh` and `ethos enable`,
removes the drift surface entirely. Curl-bootstrap still works: the binary
is on disk before `enable` is invoked.

## `enable` versus `init` and `setup`

§2.13: `enable`/`disable` toggle guidance + hooks; `init` creates and
populates the tool's repo config. Ethos's config-writer is `ethos setup`
(`cmd/ethos/setup.go`) — it writes identities, `.punt-labs/ethos.yaml`, the
active bundle, and agent files. `setup` and `enable` stay **distinct
roles**:

- `setup` populates Config-zone data (identities, bundle, `ethos.yaml`).
- `enable` deposits the Vendored zone, the marker, the import line, and the
  hooks.

`setup` **may call** `enable` as its final step (§2.13 "`enable` may call
`init` when enabling requires config" — here the direction is reversed but
the separation is the same), so a fresh `ethos setup` leaves the repo
enabled. `enable` never writes identity config; `disable` never removes it.
The repo config file is **no longer** an enabled signal — the marker is.

## `punt audit` checks ethos must satisfy (acceptance criteria)

From §2.11 (and punt-labs-dir §8, keyed by the marker):

- **AC1 — import present iff enabled.** For a repo with
  `.punt-labs/ethos/enabled`, `<repo>/CLAUDE.md` contains exactly one
  `@.punt-labs/ethos/CLAUDE.md` line; with the marker absent, the line is
  absent (§2.11).
- **AC2 — no orphan import.** No `@.punt-labs/ethos/CLAUDE.md` line without
  the corresponding `.punt-labs/ethos/CLAUDE.md` file (§2.11).
- **AC3 — no stale enabled tool.** An enabled ethos is on `PATH` /
  installed (§2.11).
- **AC4 — no duplicate import.** The line appears at most once (§2.11).
- **AC5 — no legacy markers.** No `<!-- punt:begin … -->` managed sections
  in any user-owned `CLAUDE.md` (§2.11). Ethos never wrote these; nothing
  to strip.
- **AC6 — well-formed line.** The line is the exact canonical string,
  top-level, no trailing whitespace (§2.11).
- **AC7 — zone carve-out holds (punt-labs-dir §8).** The vendored-zone
  manifest write never clobbers a Config-zone or seal path; the gitignore
  probes pass for `.punt-labs/ethos/local/x`, `foo.local`,
  `config.local.yaml` (ignored) and `CLAUDE.md`, `config.yaml`,
  `locales/en.yaml` (tracked).

## Deferred: `.claude/settings.json` (§2.8)

§2.8 **permits** `enable` to register additive `.claude/settings.json`
entries ("may register"), it does not require it. Ethos has no repo-scoped
settings entry today, so this design ships **no** settings.json write path
— not even an empty-set merge (REC-1). An empty merge/remove path is dead
code that would still need the exclusive lock, the deterministic-set
computation, the order-preserving merge, and the exact-value-match removal,
all tested, to register zero entries.

**Rule for adding it later:** the settings.json path is added only when the
first concrete repo-scoped entry exists, and it ships with that entry —
never speculatively. When it lands it must take the same exclusive lock as
the §2.4 import-line write (§2.8 extends the lock requirement to every
shared host-file mutation) and use the permissions.md §6 idempotent
merge / exact-value-match removal.

## Migration (§2.12)

Forward integration, no compat shim (PL-PP-1). Ethos never shipped a
legacy `~/.claude/CLAUDE.md` marker block or a repo-root sentinel, so there
is nothing to retire — the migration is pure convergence:

- **Repos enabled by the v4.1.1 interim** have the seal + trailer hooks
  chained but **no marker and no import line** (the installer never wrote
  them). The first `ethos enable` run converges such a repo: it deposits
  the guide, writes the marker, adds the import line, and re-chains the
  hooks idempotently (the existing marker sections are stripped and
  re-appended in place, `install.sh:96–97` semantics, ported). No hook
  content is lost; the run is safe to repeat.
- **SessionStart hook performs the first-run migration (REQ-1 resolution).**
  Relying on an operator to run `ethos enable` is not enough: once the §2.7
  gate lands in the embedded hooks, an interim repo whose marker is absent
  stops sealing until the marker exists. §2.12 explicitly blesses the
  SessionStart path ("`enable` — or the tool's SessionStart hook — detects
  the legacy state and, in one operation, deposits `.punt-labs/<tool>/` +
  the `enabled` marker"). The ethos SessionStart hook therefore detects the
  **chained-hooks-present + marker-absent** state — legacy-enabled — and
  converges it in one step: deposit the vendored zone (guide + manifest) and
  write the marker, exactly as `enable` does (the SessionStart hook calls
  the same `internal/enable` deposit path). This runs **before** any tool
  call in the session, so the marker exists before the first commit's seal
  gate is evaluated — the gate never suppresses a seal for a genuinely-active
  interim session. In a gitlink-mounted repo the deposit defers (Open
  Question #1), and the migration is a no-op there until `ethos-e29s`.
  Detection is conservative: it fires only when an ethos marker section is
  present in the chained hook (an unambiguous ethos fingerprint), never on a
  bare foreign hook, so it cannot mis-enable a repo that merely has some
  other pre-commit hook.
- **No legacy sentinel to retire.** Ethos never used `.biff`/`.quarry.toml`
  style presence dotfiles; the `enabled` marker is new, not a replacement.
- **User-scope closure (§2.6).** Ethos is **repo-scoped only**: its guidance
  is per-repo (team identities, missions, the audit trail all live in a
  repo), so it registers no `@~/.punt-labs/ethos/CLAUDE.md` user-scope import
  and ships no `~/.claude/CLAUDE.md` line. §2.6's mandatory-and-symmetric
  user-scope install/teardown therefore **does not apply** — there is no
  user-scope line to add at `install` or remove at `uninstall`. This closes
  the §2.6 obligation affirmatively rather than leaving it open.
- **Ordering dependency.** The gitlink → inline-vendored registry
  migration (`ethos-e29s`, punt-labs-dir §10 ethos(registry)) must precede
  reliance on `enable` in consuming repos — you cannot deposit the guide
  into a submodule (Open Question #1). The `.punt-labs/ethos.yaml` →
  `config.yaml` move (punt-labs-dir §9) is sequenced behind the same
  de-gitlink and is **not** enable's job — it is a separate config-zone
  migration.

## Rejected alternatives

- **Hooks-only `enable` verb** (the leader's rejected first bead). A verb
  that only chains git hooks and skips the guide, marker, and import line
  fails §2.3 items 1–3 and §2.7. The marker is the load-bearing signal for
  `punt audit` and the hook gates; without it there is no three-state
  model. Rejected.
- **`install.sh`-owned per-repo enablement** (the v4.1.1 arrangement). A
  machine-scoped installer reaching into a specific repo's hooks conflates
  `install` (machine) with `enable` (repo) — §2.13's exact anti-pattern. It
  also cannot write a marker or import line without duplicating the whole
  enable path in shell. Rejected; this design deletes it.
- **Managed CLAUDE.md sections.** A `<!-- ethos:begin -->` … `end` block
  inside the user's `CLAUDE.md`, merged on enable. §2.1 forbids tooling
  from owning any bytes inside the user's `CLAUDE.md` beyond a single
  `@`-import line; composition happens at read time via the import, never a
  managed section. Rejected.
- **One combined `internal/enable` package.** Folding the import-line
  writer and the hook-chainer into one package couples two independent
  failure surfaces and blocks sharing the hooks resolver with `doctor`.
  Rejected in favor of three single-responsibility packages.
- **Keeping the shell hook-chaining in `install.sh` alongside the Go port.**
  Two copies of marker-section logic drift; the v4.1.1 `ethos-2ol1` bug is
  the precedent. Rejected (see `install.sh` delegation).

## Test strategy

### `internal/claudemd` (§2.4 writer)

Table-driven, one host fixture per edge case; assert byte-exact output.

- **EOL preservation.** LF, CRLF, lone-CR hosts: the appended line matches
  the host EOL; every other byte is unchanged.
- **No trailing newline.** Host ending mid-line gets one newline inserted
  before the append; the import is never glued to the last line.
- **Idempotent match, terminator-insensitive.** A CRLF host already
  carrying the line (with `\r`) is detected — no duplicate appended.
- **Code-block false positives.** The canonical line inside a fenced block
  (backtick and tilde, with and without info string) and inside an
  indented block is **not** matched — `enable` still appends a top-level
  line, `disable` does **not** remove the code-block line.
- **Duplicate collapse.** Two top-level copies → `disable` removes both.
- **Symlink.** A symlinked host is written through to its target; the link
  survives.
- **Mode.** Existing mode preserved; new file `0644`.
- **Lock contention.** Two concurrent `register` calls (goroutines) both
  observe the lock; no lost update — the final file has exactly one line.

### `internal/githook` (chaining)

Port the `.tmp/test-hook-chain.sh` scenario suite into Go table tests:

- Empty slot → standalone marker form written, executable.
- Our section present → stripped and re-appended (idempotent upgrade).
- Our pre-marker standalone (IDENT on line 2) → replaced with marker form.
- Hybrid (host content, then our script) → chained, host preserved.
- Foreign shell host → chained after host; host fall-through status
  preserved.
- Non-shell host (Python/Node shebang) → skipped with warning, untouched.
- BEGIN with no END → abort, host untouched.
- Symlinked hook → target updated, link intact; unresolvable target →
  abort.
- Unconditional trailing `exit`/`exec` → chained but warned; `exec 3>&1`
  fd-redirection → not warned.
- **`Unchain` round-trip** (new): `Chain` then `Unchain` restores the host
  byte-for-byte; unchaining a standalone removes the file; unchaining a
  chained host leaves the reduced host.
- **hooksPath/worktree parity.** Resolver agrees with `doctor` on a
  worktree (common `.git/hooks`, not the dead per-worktree dir) and a
  `core.hooksPath` repo.

### `internal/enable` (orchestration) and CLI

- **enable/disable round-trip.** `enable` then `disable` on a clean repo:
  marker gone, import line gone, hook sections stripped, vendored guide
  still on disk (dormant). Re-`enable` restores all four.
- **Zone carve-out.** `enable` on a repo with populated `identities/`,
  `teams/`, `sessions/<date>-<sid>/audit-*.jsonl` leaves every one of them
  byte-identical; the manifest write collision-errors if a manifest path
  ever names a Config/seal path.
- **Idempotent re-enable (upgrade).** Two `enable` runs: one import line,
  one marker, hooks re-chained in place, no duplicate sections.
- **v4.1.1 interim convergence.** A repo with chained hooks but no
  marker/import → first `enable` adds the marker and import, re-chains
  hooks, loses no host content.
- **SessionStart migration.** A repo with the ethos seal section chained but
  no marker → the SessionStart hook deposits the vendored zone and writes
  the marker before the session's first tool call; a repo with only a
  foreign pre-commit hook (no ethos section) → SessionStart does **not**
  mis-enable it.
- **Marker gate in the embedded hooks.** With the marker absent the chained
  `pre-commit` exits at the gate and never invokes `ethos audit seal`
  (verify the binary is not called); with the marker present it seals. The
  gate exits with the captured host status, not a bare `exit 0`, so a
  chained host's failing fall-through still blocks the commit when ethos is
  dormant.
- **doctor states.** `CheckSealHook` PASSes a repo with no marker and no
  chained hook; FAILs a repo with the marker but no active seal; WARNs a
  repo with the seal chained but the marker absent (gated-but-unenabled).
- **gitlink guard.** `enable` in a repo where `.punt-labs/ethos` is a
  gitlink errors (or defers) per the leader's Open Question #1 ruling.
- **not-in-a-repo.** `enable` outside a work tree exits 2 with a clear
  message.

### Dogfood (CLAUDE.md requirement)

Build the binary, run `ethos enable` in a scratch clone, inspect the four
artifacts, run a commit to confirm the seal fires, run `ethos disable`,
confirm the tree is clean and the guide is dormant. `make check` passing is
necessary but not sufficient.

## Open questions for the leader

1. **Gitlink-mounted repos: error or defer?** In consuming repos
   `.punt-labs/ethos` is a submodule (gitlink); `enable` cannot deposit the
   guide or marker into it. **Recommend: error** with a vendor-first remedy
   (`ethos-e29s`), symmetric with how the audit seal already gates on
   vendoring, so a user gets a clear "vendor first" signal rather than a
   silent partial enable. Alternative: defer with a signaled notice like
   the seal. Decision needed before implementation.
2. **`disable` and the audit no-off-switch invariant.** DES-058: "every
   tool call is logged; no gaps, no off switch." `disable` removing the
   seal hook is an off switch for future sealing. **Recommend: `disable`
   runs a final `audit seal` before stripping the section** (captures
   pending live lines), then warns that future commits in this repo will
   not seal until re-enabled. Confirm this is the intended semantics, or
   rule that `disable` should refuse when unsealed live lines exist.
3. **Does `setup` call `enable`?** §2.13 keeps them distinct but permits
   one to call the other. **Recommend: `setup` calls `enable` as its final
   step** so a fresh setup leaves the repo enabled (guide, marker, import
   line, and hooks), matching user expectation. Confirm.
4. **`--purge` scope.** §2.9 lists `disable --purge` as optional.
   **Recommend: out of scope for this design**, specified as a follow-up
   that deletes only the Vendored and Marker zones (never Config or seal
   data). Confirm deferral.

Round-2 note: the round-1 settings.json open question is closed — settings
plumbing is dropped per REC-1 (see "Deferred: `.claude/settings.json`"),
and REQ-1's marker-gate + SessionStart-migration decisions are folded into
the three-state and migration sections above, no longer open.
