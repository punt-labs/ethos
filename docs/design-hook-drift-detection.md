# Detecting stale git hooks: `ethos doctor` and content drift

**Status**: Design. Bead `ethos-r2f9`. Mission `m-2026-08-09-012`.
Design only — no implementation in this document or this mission.

## Problem

Hooks (`commit-msg`, `pre-commit`) are deployed as static file copies at
`ethos enable` time (`internal/enable`, `internal/githook.Chain`). When a
later release fixes a bug inside a hook script's own shell logic, nothing
re-syncs that fix into a repo that already ran `enable`. The repo's `.git/`
directory has no record of which ethos version — or which content —
wrote the hook it is running.

This is not hypothetical. PR #415 (`5b80bff`, `ethos-pobi`, merged
2026-07-31) replaced `hooks/commit-msg.sh`'s buggy reverse-name-sort
session fallback with a call to `ethos hook commit-trailers`, which
resolves the committing session correctly. Nine days later,
`punt-kit/.git/hooks/commit-msg` was confirmed still running the
pre-#415 script verbatim — the old `find ... | sort -r` loop, embedded
directly in shell, is still there:

```sh
# --- BEGIN ETHOS DES-054 TRAILER ---
...
  for d in $(find "$HOME/.punt-labs/ethos/sessions" -maxdepth 1 -type d 2>/dev/null | sort -r); do
    if [ -f "$d/delegation-binding" ]; then
      binding_file="$d/delegation-binding"
      break
    fi
  done
...
# --- END ETHOS DES-054 TRAILER ---
```

Nothing about this hook is *missing* or *inactive*. The marker section is
present, well-formed, carries the correct ident fingerprint on line 2,
and successfully adds `Mission:`/`Delegation:` trailers on every commit —
just by running nine-day-old logic instead of delegating to `ethos hook
commit-trailers`. `ethos doctor` has no signal anywhere that this repo
has drifted from what the current binary would install.

## Reading the adjacent beads

**`ethos-hy40`** — doctor checks only the seal (pre-commit) hook today;
`CheckSealHook` in `internal/doctor/doctor.go` has no counterpart for the
commit-msg/trailer hook at all. A hand-removed or host-clobbered trailer
hook on an enabled repo means trailers silently stop landing, forever,
with `doctor` green. hy40's job is *presence and activity parity*: give
commit-msg the same four-state check (`FAIL` missing/inactive on an
enabled repo, `PASS` not-enabled, `WARN` gated-but-unenabled) that
`CheckSealHook` already gives pre-commit. hy40 is not built yet.

**`ethos-kcbv`** — `CheckSealHook`'s `hasActiveSealCall` is a lexical
scanner over shell text, and it has needed four rounds of refinement
across two branches to close successive false-negative corners (substring
match, inline comments, separator boundaries, heredocs). The leader
declared a stop-loss: the next lexical corner converts the check from
*"does the text look like a call"* to *"does the call actually happen"* —
run the hook in a sandbox with a stub identity binding and env, and
assert the stub observed the call. kcbv is about **reachability**: is the
seal invocation really wired up and not trapped behind dead code,
`eval`, or an aliased wrapper the lexical scanner can't see through.

**This mission (`ethos-r2f9`)** is neither. It is not about whether a
hook is *present* (hy40) or whether its call is *reachable* (kcbv). It is
about whether a hook that is unambiguously present and unambiguously
reachable is running **stale content**. The punt-kit `commit-msg` hook
above is the proof these are three different axes: it would `PASS`
hy40's presence check (the section exists, correctly formed) and it
would `PASS` kcbv's execution probe (the trailer really does get added —
the fallback logic runs and produces `Mission:`/`Delegation:` output).
Neither check would ever flag it. Only a check that looks at *what the
installed script actually says*, compared to what today's binary would
install, catches this.

## Recommendation

**Detect drift by comparing the on-disk marker section, byte-for-byte
(after line-terminator normalization), against the section the running
`ethos` binary would install today — computed from the same `go:embed`
source `internal/enable` already uses. No version stamp, no hash file,
no state written at `enable` time. The comparison is entirely a `doctor`
read, and it does not fold into kcbv's execution-based mechanism.**

### Why not fold into kcbv

kcbv answers "is the call reachable" by *running* the hook. That proves
invocation, not content. A stale script can pass a reachability probe
completely — the punt-kit hook does, today, on every real commit. To
catch content drift via execution you would need a **behavioral fixture
per historical bug**: rig the sandbox so the pre-#415 sort logic and the
post-#415 `ethos hook commit-trailers` delegation produce distinguishably
different stub observations, then keep writing a new fixture for every
future hook change, forever. That is not bounded, and it is not
bug-agnostic — a change to the hook that doesn't happen to touch the one
behavior a fixture probes for would slip through undetected, which is
exactly the silent-drift failure mode this bead exists to close. A
byte-level content compare needs no fixture, catches *any* future edit to
the script automatically, and is cheaper: an in-memory comparison on
every `doctor` run, versus spawning a subprocess with a rigged
environment. kcbv's execution-based mechanism should stay scoped to what
it was proposed for — proving the invocation is reachable, not stale.
Whatever kcbv's `hasActiveSealCall` replacement ends up looking like, the
currency check below runs independently of it, before or after it lands,
because it operates on raw section bytes, never on execution behavior.

### Why not version-stamp the hook at enable time

The alternative the bead text raises directly: write a version marker
into the chained section at `enable` time (`# --- BEGIN ETHOS DES-058
SEAL v4.11.0 ---`) or into a sidecar manifest, and have `doctor` compare
the stamped version against the running binary's own version.

Rejected. A version number is the wrong comparison key because it does
not track 1:1 with hook-script content: most releases touch nothing
under `hooks/*.sh`, so a version-string compare would false-positive
"stale" on every repo enabled under an older release even when its hook
content is byte-identical to what the current release ships. The inverse
failure is worse: a stamped version is only as good as `enable`
remembering to bump it, by hand, on the *specific* releases that do touch
a hook script — an easy step to forget, and forgetting it produces a
silent false negative, which is the exact failure this bead is closing.
Comparing actual bytes against the actual embedded source has no such
gap: it derives correctness from `go:embed`, not from a maintained
side-channel that can itself drift out of sync with the thing it
describes.

A persisted content-hash manifest (`.punt-labs/ethos/hooks-installed.json`,
written at `enable` time) was considered and rejected for the same
reason in a different shape: it is a second source of truth for
something the binary already carries losslessly via `go:embed`. A
manifest can itself go stale (hand-edited, or left behind by a partial
`enable` that crashed after the manifest write but before the hook
chain — see the marker-last invariant in `docs/enable-disable.md`).
Comparing directly against the current binary's embedded source needs no
persisted state, so there is nothing to keep in sync and nothing that can
go stale independently of the hook file itself.

## Mechanism

### What "the section" is, precisely

`internal/githook.Chain` already knows how to build the canonical section
for a tag from source bytes — that's `sectionBytes(tag, src)`, today
unexported: the `# --- BEGIN <tag> ---` line, `src` with its shebang
stripped, the `# --- END <tag> ---` line. It also already knows how to
find and remove an on-disk section for a tag — that's `stripSection`,
used by both `Chain` (idempotent re-append) and `Unchain`. The drift
check needs exactly the "find" half of `stripSection` without the
"remove," and exactly `sectionBytes` exported. Both already exist inside
`githook`; nothing new needs inventing, only exposing.

Two additions to `internal/githook`:

```go
// ExpectedSection returns the exact bytes Chain would write for tag's
// section from src today: the marker lines plus src with its shebang
// stripped. Exported so a caller other than Chain (doctor's currency
// check) can compute the canonical section without a second
// implementation of the same transform.
func ExpectedSection(tag string, src []byte) []byte

// InstalledSection returns the on-disk section for tag in data — the
// marker lines through the matching END, inclusive. ok is false when no
// BEGIN for tag exists: nothing installed, nothing to compare (a
// presence question, hy40's job, not this one). A non-nil error means a
// BEGIN was found but failed a check Chain and Unchain already enforce
// on this data: no matching END (truncated section), or the line after
// BEGIN does not carry ident (not an ethos-written section — the same
// defense-in-depth stripSection already applies before deleting
// anything).
func InstalledSection(data []byte, tag, ident string) (body []byte, ok bool, err error)
```

`InstalledSection` factors out of the existing `stripSection` loop (which
already walks BEGIN/END pairs heredoc-aware via `textscan.HeredocMask`
and already verifies the ident fingerprint) so `stripSection` becomes a
one-line wrapper: find the range, and either keep it (`InstalledSection`)
or delete it (`stripSection`). One scanner, two callers — the same
"single authoritative copy" discipline `hooks/embed.go`'s own doc comment
states for the scripts themselves, applied to the code that reads them.

### Normalizing before comparing

`Chain` rewrites the section's line endings to match a foreign host's
convention when chaining onto a CRLF file (`githook.go`, the
`bytes.ReplaceAll(section, []byte("\n"), eol)` step). A currently-current
CRLF-chained hook is therefore not byte-identical to the LF-only embedded
source even when its content is unchanged. The comparison must normalize
line terminators on both sides before comparing — split into lines with
`textscan.SplitKeepEnds`, strip each terminator with
`textscan.StripTerminator`, rejoin with `\n`. This is the same
terminator-insensitive comparison `internal/claudemd` already uses for
the import line, applied here to a multi-line section instead of one
line.

After normalization, hash both sides with SHA-256 and compare the
digests. A hash (not a raw diff) keeps the `doctor` detail line short — a
truncated hex prefix on each side, not the hook's full body — and keeps
the comparison O(1) to report regardless of script size.

### Where the tag/ident constants live

Today `sealTag`, `sealIdent`, `trailerTag`, `trailerIdent` are unexported
constants inside `internal/enable`. The currency check in `internal/doctor`
needs the same four strings, and hy40's future presence/active check for
the trailer hook will need them too. Duplicating them a second time
inside `doctor` is the exact two-copies-drift pattern this codebase
already burned itself on once — `ethos-2ol1`, the v4.1.1 seal-chain bug
that lived only in the shell copy of the chaining logic while the Go port
was already correct (`docs/enable-disable.md`, "Why three packages, not
one"). The fix is the same one applied there: one copy. Move the four
constants into the `hooks` package — `hooks.SealTag`, `hooks.SealIdent`,
`hooks.TrailerTag`, `hooks.TrailerIdent` — beside `hooks.PreCommit` and
`hooks.CommitMsg`, since `hooks` is already documented as the home for
"a single authoritative copy" of everything about these scripts
(`hooks/embed.go`'s package comment).
`internal/enable` references the exported constants instead of its own;
`internal/doctor` does too. This is a small mechanical move, not new
logic, and it is the only "deploy-side" change this design asks for — it
touches where a string constant lives, not what `enable` writes to disk
or when.

### The new doctor check

```go
// HookSpec names one ethos-managed git hook for the currency check.
type HookSpec struct {
    Name      string // report label, e.g. "Seal hook"
    File      string // hook filename inside the hooks dir: "pre-commit"
    Tag       string // hooks.SealTag / hooks.TrailerTag
    Ident     string // hooks.SealIdent / hooks.TrailerIdent
    Canonical []byte // hooks.PreCommit / hooks.CommitMsg
}

// CheckHookCurrency compares the installed section for spec against what
// this ethos build would install today. It is independent of the
// enabled marker and of hy40's presence/active states: a section that
// doesn't exist is not this check's concern (PASS, nothing installed);
// a section that exists is checked for currency regardless of whether
// ethos is enabled in this repo right now, because `ethos enable` is
// the remedy for both problems and the check should stay cheap and
// unconditional.
func CheckHookCurrency(repoRoot string, spec HookSpec) doctor.Result
```

Four states, mirroring the report shape `CheckSealHook` already
established (`doctor.go:245`):

| State | Condition | Status | Detail |
|---|---|---|---|
| Nothing installed | no BEGIN for `spec.Tag` anywhere in the hook file (or no hook file) | PASS | `"no <name> section installed"` |
| Not ours | BEGIN found, ident fingerprint on the next line fails | FAIL | ``"<name> section present but does not carry ethos's fingerprint — not a recognized ethos section; remove it and run `ethos enable`"`` |
| Truncated | BEGIN found, no matching END | FAIL | `"<name> section has a BEGIN with no matching END — hand-truncated; fix it by hand"` (same wording `githook.Chain`/`Unchain` already use for this case) |
| Current | fingerprint OK, normalized hash matches `ExpectedSection(spec.Tag, spec.Canonical)` | PASS | `"<name> section matches this ethos build (sha256:a1b2c3d4…)"` |
| Stale | fingerprint OK, normalized hash differs | WARN | ``"<name> section content differs from what this ethos build would install (installed sha256:a1b2c3d4…, current sha256:e5f6a7b8…) — run `ethos enable` to refresh"`` |

**Stale is `WARN`, not `FAIL`.** `Result.Passed()` already treats `WARN`
as advisory (`doctor.go:30`) — the same tier as "gated-but-unenabled."
Staleness is not necessarily brokenness: most releases don't touch hook
content, and a repo running last month's — still-correct — hook body is
not in a faulted state. Making this `FAIL` would turn every unrelated
release into a fleet-wide red `doctor` the moment it ships, for repos
whose hooks are working fine; that is disproportionate to what the check
actually knows (content differs), versus what it doesn't know (whether
the difference matters to this repo). `WARN` surfaces it with a remedy
without blocking. "Not ours" and "truncated" stay `FAIL` because those
are not staleness — they are a section that isn't safely ethos-managed
at all, the same severity `githook`'s own refuse-on-ambiguity guards
already assign.

`RunAll` (`doctor.go:51`) gains two calls, run unconditionally alongside
the existing `CheckSealHook`:

```go
results = append(results, CheckHookCurrency(repoRoot, sealHookSpec))
results = append(results, CheckHookCurrency(repoRoot, trailerHookSpec))
```

The trailer-hook call ships in **this** mission's follow-on
implementation, ahead of hy40. It does not need hy40's presence/active
states to exist first — `InstalledSection`'s `ok=false` return already
degrades cleanly to `PASS "no section installed"` for a repo with no
trailer hook at all, which is a correct (if less informative than hy40's
eventual `FAIL`) answer for that state. When hy40 lands its own
`CheckTrailerHook` (the `FAIL`-on-missing-when-enabled four-state check,
parity with `CheckSealHook`), it should build its `HookSpec` values from
the same `hooks.TrailerTag`/`hooks.TrailerIdent` constants this design
moves into the `hooks` package, and the two checks run as separate
`Result` lines — "Trailer hook" (hy40: presence/active) and "Trailer hook
currency" (this design: content) are different questions about the same
file and should stay two lines, not merged into one `Result`'s `Detail`,
for the same reason `CheckSealHook` and this design's currency check stay
separate: each `Result` answers one question.

### Sample `doctor` output

Today (`cmd/ethos/main.go:248`, `"  %-24s %s  %s\n"`):

```text
  Audit seal hook          PASS   chained seal section active
```

With this design landed (pre-commit currency line; a trailer-hook line
follows once the constant move and second `CheckHookCurrency` call ship):

```text
  Audit seal hook          PASS   chained seal section active
  Seal hook currency       WARN   Seal hook section content differs from what this ethos build would install (installed sha256:a1b2c3d4, current sha256:e5f6a7b8) — run `ethos enable` to refresh
  Trailer hook currency    WARN   Trailer hook section content differs from what this ethos build would install (installed sha256:9f8e7d6c, current sha256:e5f6a7b8) — run `ethos enable` to refresh
```

`punt-kit` today would show exactly the second `WARN` line for its
trailer hook — the incident this bead traces back to becomes visible the
next time anyone runs `ethos doctor` there, with a remedy that is already
idempotent (`enable` re-chains in place, `docs/enable-disable.md`
"Idempotent upgrade").

## Testing strategy (for the implementation mission)

Table-driven, one fixture per state, in `internal/githook` and
`internal/doctor`:

- `InstalledSection` / `ExpectedSection`: standalone hook (shebang +
  section only) and chained hook (foreign content before/after the
  section) both extract the same body; a BEGIN with no END returns the
  truncated error `Chain`/`Unchain` already produce; a BEGIN whose next
  line lacks `ident` returns the fingerprint error; no BEGIN at all
  returns `ok=false`, no error.
- **CRLF regression** (the case a naive byte-compare gets wrong): chain
  onto a CRLF host, then run the currency check against the unmodified
  LF-embedded source — must report Current, not Stale. This is the one
  test that specifically defends the terminator-normalization step.
- **Drift positive**: hand-edit one byte inside an otherwise-valid
  section (simulating an old release's content) — reports Stale with
  both hash prefixes present in `Detail`.
- **Punt-kit regression fixture**: embed the actual pre-#415
  `commit-msg` body (already captured above) as a table case, current
  source as `hooks.CommitMsg` — must report Stale. This is the concrete
  incident, turned into a test that fails if the mechanism regresses.
- `CheckHookCurrency`: no hook file present → PASS "no section
  installed"; hook file present, no matching tag → PASS; matching tag,
  current → PASS; matching tag, stale → WARN; tampered fingerprint →
  FAIL; truncated section → FAIL.
- `doctor.RunAll` wiring: both currency `Result`s appear in the slice,
  independent of the `enabled` marker's state (dormant repo with a
  leftover chained-but-current hook still reports PASS, not skipped).

## Rollout

No fleet-wide manual convergence step is required, unlike the
`enable`/`disable` marker rollout (`docs/enable-disable.md` §Migration).
`WARN` is non-blocking and the remedy is the already-idempotent `ethos
enable`, so this check can ship and immediately surface every drifted
repo in the fleet — including `punt-kit` — without anyone having
pre-staged anything. There is no residual-window problem the way the
marker rollout had one: the check needs no prior `enable` run to become
meaningful: it is correct against a chained-but-dormant hook exactly as
it is against an enabled one, because it never reads the `enabled`
marker.

## Explicitly out of scope

- **hy40's presence/active states for the trailer hook** — `FAIL` when
  the trailer hook is missing/inactive on an enabled repo, `WARN` for
  gated-but-unenabled. This design's trailer-hook `CheckHookCurrency`
  call is not a substitute for that; it only answers "is what's there
  current," never "is anything there at all, and is it enabled." hy40
  should build on the `hooks.TrailerTag`/`hooks.TrailerIdent` constants
  this design relocates, not redefine them.
- **kcbv's execution-based verification** of the seal invocation. Not
  touched, not required by this design, does not touch it in either
  direction.
- **Auto-remediation.** `doctor` reports; it does not run `ethos enable`
  on the operator's behalf. Consistent with every other check in
  `internal/doctor/doctor.go` today.
