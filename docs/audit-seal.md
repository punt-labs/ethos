# Live audit write path and the sealed committed record

**Status**: Draft. Bead `ethos-t5b6` (P1). Amends DES-054 v5 storage.
Cross-repo request from the punt-kit agent (claude:tty16), 2026-07-20.

DES-054 made the session audit log both git-tracked and continuously
appended. Those two properties cannot hold for one file. This design
splits them: a live file in the repo's machine-local `.punt-labs/local/`
zone absorbs the continuous appends; a seal step copies complete lines
into a **new immutable tracked chunk file** at deliberate lifecycle
points. Between seals no tracked file changes, so a repo with an active
session has a clean tree, and because every tracked chunk is written once
and never modified, two branches can only ever add distinct chunk files
— git merges them with no conflict.

## Problem

DES-054 v5 stores the session audit log at

```text
<repo>/.punt-labs/ethos/sessions/<YYYY-MM-DD>-<session-id>/audit.jsonl
```

The file is git-tracked by design: it is shared history, its paths are
redacted for that reason, and commit trailers resolve against it. It is
also appended by the PreToolUse audit hook on **every** tool call. A
file cannot be both git-tracked and continuously appended by a live
process. Any repo with an active session has a permanently dirty tree.

This is not hypothetical. It blocks `punt release` preflight today: the
clean-tree gate over cross-repo siblings fails because a sibling has a
live session writing to its tracked `audit.jsonl`. The first line in the
offending file was a `git status` call — inspecting the repo dirtied it.

The mission tree has the same disease. `missions/<id>/log.jsonl` is
tracked and appended at each mission lifecycle event, and
`.create.lock` is a **tracked lock file** (confirmed:
`git ls-files .punt-labs/ethos/missions/.create.lock` lists it). A lock
file has no business in shared history.

## Non-solutions, already ruled out with the CEO

- **Gitignore the audit files.** Defeats the shared-history design —
  the audit record must travel with the work.
- **Exempt `.punt-labs/` from clean-tree checks in consumers.** Pushes
  ethos's storage decision into every consuming tool; a release gate
  that ignores a whole directory is a weaker gate.
- **Relax the check GitHub-side.** Branch protection is ref-scoped; it
  cannot express "ignore this path."
- **An audit-suppression sentinel.** An audit hook with an off switch is
  not an audit system.

## Requested shape

1. Live writes go to an untracked location.
2. A seal step snapshots the not-yet-sealed live lines into a tracked
   chunk at a deliberate lifecycle point — pre-commit primary, mission
   close secondary.
3. Between seals no tracked file changes, so trees are clean. A dirty
   `sessions/` path then **means** something: an unsealed record that
   should have been committed.

## Design

### Two zones in the repo: live is `local`, sealed is tracked

The append-heavy files split across two directories in the **same
checkout**, distinguished by whether git tracks them:

| Zone | Path | Git |
|------|------|-----|
| Live write path | `<repo>/.punt-labs/local/ethos/sessions/<session-id>.audit.jsonl` | gitignored |
| Sealed record | `<repo>/.punt-labs/ethos/sessions/<YYYY-MM-DD>-<session-id>/audit-<first>-<last>.jsonl` | tracked |

The live writer appends to the `local` file. The seal step writes the
not-yet-sealed lines into a new tracked chunk under the session's sealed
directory. Reads concatenate the sealed chunks and the live tail.

The live filename is flat (`<session-id>.audit.jsonl`) while the sealed
directory is dated (`<YYYY-MM-DD>-<session-id>/`). The date is the
**session start date**, taken from the roster and fixed for the session's
whole life — never wall-clock-at-seal. One session therefore maps to
exactly one sealed directory even when it commits after midnight, so the
per-session watermark scan (§Watermark) always lists a single directory.
This matches DES-054, where the dated directory is created once at
session start.

**When the roster entry is gone.** `ethos session purge` removes stale
roster entries — precisely the crashed sessions the orphan-recovery promise
(§Seal triggers) most needs to date. Purge does not outrun the seal: before
removing an entry it `stat`s the session's recorded live file, and if that
file still holds lines above the session's sealed watermark it **refuses**,
or with `--force` warns loudly and proceeds. An **absent** live file is
itself evidence, not a clean case: if the recorded live file is already gone,
purge warns and sets the tombstone flag in a distinct **"live file already
gone"** variant, so a checkout deleted before its lines sealed cannot slip
through purge unremarked (§Seal failure policy). The seal recovers the date
without the roster and never invents a wall-clock one. If a sealed directory
already exists for the session, its `<YYYY-MM-DD>` prefix is authoritative.
If not, `session purge` leaves a dated **tombstone**
(`~/.punt-labs/ethos/sessions/<session-id>.purged`, recording the start date,
the repo and recorded checkout path it was purged from, and an
**unsealed-lines flag** — set either when it was forced past pending lines or
when the live file was already gone) that the
seal and the vacuum cross-check (§Seal failure policy) both consult; failing
even that, the date is the day of the live file's **first-line `ts`**. All
three are fixed properties of the session, so none splits one session across
two dated directories, and an orphaned session is never silently skipped for
want of a date.

**Live-file location — `.punt-labs/local/` (machine-local zone).** The
`local` segment is the org's convention for machine-local state that git
never tracks — the same convention as `.envrc.local`, `vox.local.md`,
and `.claude/settings.local.json`. The gitignore rule for
`.punt-labs/local/` ships **once**, org-wide, via the punt-kit
`punt-labs-dir.md` standard (merged `e3ab9a3`) and is enforced by
`punt:init` / `punt:audit`. A repo does not carry its own bespoke ignore
line; it inherits the standard.

The live file is per-checkout by construction: it lives inside the
checkout it belongs to, so two checkouts of the same GitHub repo at
different paths have two distinct `local` files with no coordination.
This **removes** the *seal-time* checkout-path scoping — the seal has
nothing to disambiguate, because the filesystem already separates the
checkouts. The checkout path itself is deliberately **retained** where it
stays load-bearing: the purge tombstone records the repo and checkout path a
session was purged from, and the vacuum cross-check derives the live path from
it (§Seal failure policy).

The `local` zone also works in gitlink-mounted repos: `.punt-labs/local`
is a sibling of the `.punt-labs/ethos/` subtree, so it is reachable even
when `ethos/` is a submodule. (Sealed chunks under `.punt-labs/ethos/`
remain gitlink-blocked until bead `e29s` lifts that restriction — a
separate concern from where the live file lives.)

**Rejected — `TMPDIR` / `.tmp/`.** `.tmp/` is deletable scratch; the org
clears it freely. An unsealed audit record is not scratch — losing it
loses tool-call history that has not yet reached a commit. The live file
must survive a `.tmp` sweep.

**Rejected — `.punt-labs/local/<branch>/`.** A session spans branches:
the normal multi-PR workflow commits on more than one branch during one
session. A per-branch live path would fragment a single session's log
across directories and orphan files when a branch is deleted. Chunk
sealing (below) already makes cross-branch merges conflict-free, so
per-branch isolation buys nothing.

**Rejected — home-dir global tree.** An earlier draft put the live file
at `~/.punt-labs/ethos/`. The operator ruled for `.punt-labs/local/`:
the `local` convention already means "machine-local, never committed,"
the gitignore ships once via the standard, and per-checkout isolation is
automatic. This supersedes the structural-vs-conventional argument the
earlier draft made for the home tree.

### The live file holds redacted lines

Path redaction (DES-052 / docs/audited-delegation.md §Path redaction) rewrites
`$HOME/X → ~/X` and `<repoRoot>/X → <repo>/X` in every string of
`tool_input` before a line lands on disk, then computes
`tool_input_hash` **over the redacted form** so two machines making the
same logical call agree on the hash.

**Decision: the live file holds already-redacted lines.** The write path
is unchanged from DES-054: build raw `tool_input` → redact → hash →
truncate preview → append. The only change is *where* the finished line
lands (the `local` live file, not the tracked file).

The reasons compound:

1. **Redaction already precedes hashing.** The hash is over the redacted
   form and is computed at write time no matter what. Redaction is
   therefore already in the append path; leaving it there costs nothing.
2. **Seal becomes a byte copy.** Because the line is final when it hits
   the live file, the seal step neither parses, re-redacts, nor re-hashes
   — it copies bytes into a new chunk. A transformation-free seal cannot
   corrupt a line or double-redact one, and it keeps redaction logic in
   exactly one place.
3. **No leak at rest.** A raw live file would spill absolute paths — the
   operator's username and machine layout — into `.punt-labs/local/`, and
   would surface them through `ethos audit show` whenever the reader
   falls back to the live tail. Redacted-at-write keeps the live file as
   clean as the committed chunks.

`tool_input_hash` is unchanged: still computed over the redacted form,
still the DES-052 Stat-Write collision key. Per-line `f.Sync()` on the
live file and the partial-line-tolerant reader carry over verbatim.

### Every live line carries a strictly-monotonic per-session timestamp

Ordering and line identity come from the timestamp, not a schema field.
The writer enforces strict monotonicity per session at append time under
the flock that already serializes appends (I10-audit-atomic): read the
last timestamp, take `ts = max(now, last_ts + 1ns)`, write the line,
record `ts` as the new last. Because allocation and append happen under
one lock, `ts` is strictly increasing and never repeats within a session.

This addresses the collision the first draft feared without a schema
field. A coarse or NTP-stepped clock can hand two flock-serialized
appends the same `time.Now()` (a retry loop issuing the same input
microseconds apart) or step backward. Monotonic-clock discipline — bump
by 1ns whenever `now <= last_ts` — makes the stored `ts` a per-session
total order regardless of what the wall clock does.

`ts` is the line's identity where identity is needed: a line is
`(session, ts)`. No `seq` field, no dedup tuple.

**Ordering across the sealed watermark.** On a session's first append
after (re)start, the writer initializes its last timestamp to the **seal
watermark**, computed from the *same source set* the seal uses (§Watermark):
the max over the session's existing sealed chunk timestamps, every covering
`.quarantine` marker's **verified** `<last>`, and a frozen legacy file's max
`ts` where present — so a new live line is always strictly greater than every
already-sealed line *and* every line a quarantine marker records. Seeding from
chunk timestamps alone would leave a gap: a partial quarantine's marker can
record a `<last>` above the max chunk `ts` (the corrupt bytes reached lines the
re-seal could not recover), and under a clock regression the writer could then
mint a `ts` in that gap — below the watermark, so the seal never seals it and
the read never shows it. Sharing the watermark's source set closes the gap.
This is what lets the seal select the unsealed tail by timestamp alone.
On restart the last timestamp is also recovered from the live file's last
complete line; the writer takes the max of the two. On this open, under the
flock, the writer first **truncates a non-newline-terminated tail** before
appending. Such a fragment is a partial write whose writer died mid-line:
its `f.Sync()` never completed, so the full line is unrecoverable no matter
what, and truncating it stops the next append from gluing a new complete
line onto the fragment and producing one unparseable line. The reader's
skip-torn-tail rule still covers a live file not yet reopened by a writer.

One further live-file rule covers a line that *is* newline-terminated yet
still does not parse — an OS crash with out-of-order page writeback can
persist a later slice of a line while losing an earlier one, leaving a
terminated garbage line in the file's interior. Every consumer of the live
file — this writer's `last_ts` recovery, the seal's tail selection
(§Chunk sealing), and the read (§`ethos audit show`) — **skips such a line
and counts it on stderr**, never exiting 2 and never dropping it silently.
The event's own `f.Sync()` never completed, so the tool call it would record
almost certainly never ran; the count mirrors the torn-tail tolerance and
keeps the skip visible without bricking the session.

The three consumers do not deliver the count equally. The writer's `last_ts`
recovery runs inside the PreToolUse hook, whose exit-0 stderr Claude Code
does not surface in the normal UI, so there the count is **best-effort**. The
**load-bearing** channel is the seal's and the read's re-emission of the same
skip count: both run in visible contexts (the committing terminal, the
`audit show` caller), so a garbage line the writer skipped without a visible
count is counted again where a human sees it.

**The timestamp is branch-rewind-robust; the seal watermark is not.**
The two must not be conflated. The writer recovers `last_ts` from the
live file's own tail, and the live file is in `.punt-labs/local/` — git
never touches it, so a `git checkout` cannot regress the writer's clock;
timestamps stay strictly monotonic across any branch switch. The seal
watermark (§Chunk sealing) is derived from the *tracked* chunk set, which
`git checkout` **does** rewrite, so it can regress on a rewound branch and
cause a re-seal of already-sealed lines. That overlap is real and
intended (§Cross-branch re-seal); it is handled at read time by
`(session, ts)` dedup, not by the timestamp discipline. Monotonic `ts`
solves clock collisions, not branch rewind.

**Legacy lines.** Lines written before this design (frozen committed
history, §Migration) carry whatever `ts` they were written with; they are
never rewritten. Their max `ts` seeds the watermark for the first seal of
a continuing session, so new lines sort after them.

### Chunk sealing: each seal writes one new immutable file

A seal never modifies a tracked file. It writes a **new** chunk
containing exactly the session's not-yet-sealed lines, then stages it.
Tracked chunks are immutable after creation; two branches can only add
distinct chunk files, so a merge or rebase is conflict-free with stock
git — there is no merge driver, no `.gitattributes`, no prefix invariant,
and no resolution rule to remember.

**Chunk filename.** A chunk is named

```text
audit-<first>-<last>.jsonl
```

where `<first>` and `<last>` are the first and last line timestamps in
the chunk, each formatted as Unix nanoseconds zero-padded to 19 digits.
The encoding satisfies three requirements:

- **Sorts chronologically.** Fixed-width zero-padded integers sort
  lexically in numeric order, so a plain directory listing sorted by name
  is the chunks in time order. (19 digits covers nanosecond timestamps
  through the year 2262.)
- **Collision-free.** `<first>` is unique within a session because
  per-session timestamps are strictly monotonic, so no two chunks share a
  first timestamp — even two seals in the same wall-clock second get
  distinct nanosecond firsts. Across sessions, chunks live in different
  session directories, so names never collide.
- **Filesystem-safe.** Digits and hyphens only.

Putting `<last>` in the name lets the read and seal paths compute a
session's watermark from the directory listing alone, without opening any
chunk (§Watermark).

**Watermark.** The seal watermark for a session is the maximum line
timestamp already sealed. It is computed by listing the session's sealed
directory and taking the max `<last>` across chunk filenames. Two more
contributors join the chunk names. A frozen legacy `audit.jsonl`
(§Migration) has no timestamp in its name, so it is scanned once and the
**max `ts` over all its lines** contributes — not its last line, because the
legacy file predates the monotonic-ts discipline (§timestamp) and a coarse
or NTP-stepped clock may leave its last line below its max. A quarantine
marker `audit-<first>-<last>.quarantine` (§Seal failure policy) contributes
the **verified** `<last>` it records — the max ts the corrupt chunk's bytes
actually reached and of any lines quarantine re-sealed from the live file,
never the filename `<last>` on faith — so retiring a corrupt chunk neither
regresses the watermark nor, via an inflated filename, silently suppresses
the seal of every later line. The live writer seeds its per-session
monotonic floor (§timestamp) from this same three-way max, so no `ts` it can
mint ever sits below the watermark. The seal then
copies the live lines with `ts > watermark` into a new chunk; if none exceed
the watermark, it writes nothing (but still stages any orphan chunks, §Write
atomicity) and exits 0.

The malformed-name check is **scoped to the chunk namespace, per directory
shape**. The chunk name is one grammar in two namespaces: a session
directory holds `audit-<19digits>-<19digits>.jsonl`; a mission directory
holds `log-<session-id>-<19digits>-<19digits>.jsonl` (§Mission-tree churn).
A file whose name begins with its directory's chunk prefix — `audit-` in a
session dir, `log-` in a mission dir — is a candidate chunk, with three
recognized exceptions per shape — the quarantine artifacts
`<chunk>.jsonl.corrupt`, a second event's `<chunk>.jsonl.corrupt-<hash>`
(§Seal failure policy), and the `<chunk>.quarantine` marker (§Seal failure
policy), where `<chunk>` is the namespace's stem (`audit-<first>-<last>` or
`log-<session-id>-<first>-<last>`). Both `.corrupt`
forms are recognized **only while a covering `.quarantine` marker exists** — a
marker *covers* an artifact when the marker's named range contains the
artifact's named range, the same range-containment rule in both namespaces: a
`.corrupt` with no marker is a quarantine that crashed mid-verb (§Seal
failure policy), so the seal treats it as an error (exit 2) prompting the
resume rather than skipping it silently. Any other name carrying the
directory's chunk prefix that fails to parse as that namespace's full shape
is a *near-miss* and **fails the seal (exit 2)** rather than being silently
skipped: a skipped chunk would drop its `<last>` from the watermark, regress
it, and trigger a re-seal of already-sealed lines. Every sibling **outside**
the chunk namespace — the frozen `audit.jsonl` or `log.jsonl`, a mission's
`contract.yaml` or `results.yaml`, any unrelated file — is ignored by the
watermark and by staging and draws no error; a mission directory legitimately
holds such files. This does not reopen the regression hole the exit 2 closes:
only a near-miss carrying a chunk prefix could ever have been a chunk, and
those still fail loud.

Beyond the name, the seal **verifies each chunk's content** when it scans:
a chunk that does not parse to completion, or whose last line's `ts` does
not equal the `<last>` in its filename, is corruption. I11-chunk writes
chunks whole (temp + fsync + rename), so a torn or mismatched sealed chunk
cannot arise from normal operation; if one appears the store is damaged.
The seal fails (exit 2) naming the chunk, exactly as the read does
(§`ethos audit show`) — it trusts filenames for the watermark, but must not
trust a filename whose bytes contradict it. The specified escape from that
exit 2 is `ethos audit quarantine`, never `--no-verify` (§Seal failure
policy): fail-closed must never leave bypass as the only exit.

**Write atomicity — temp + rename.** The seal writes the chunk to a temp
file in the sealed directory (same filesystem, so rename is atomic),
`f.Sync()`s it, then renames it to `audit-<first>-<last>.jsonl`. A crash
before the rename leaves a stale temp file. Temp names embed the chunk
range (`.audit-<first>-<last>.jsonl.tmp`), and after the crash more appends
widen the tail, so the next seal writes a **different** temp name — the
stale temp is never overwritten naturally. The seal therefore deletes any
of **its own** stale temps — files matching `.audit-<first>-<last>.jsonl.tmp`
older than itself — in the session directory, under the flock, before writing
its own; the tracked chunk never exists in a partial state, and no abandoned
temp accumulates in the tracked tree. A foreign file that merely ends in
`.tmp` is not the seal's to destroy: it falls under the sibling rule
(§Watermark) — ignored, never deleted — so the seal never silently removes a
file it did not create. A crash after the rename but
before staging leaves a **complete, untracked**
chunk. Staging must recover it, but the watermark scan already counts its
`<last>` (it is on disk), so the next seal's tail is empty and never
reaches the write step. Therefore the seal's final act is unconditional:
it `git add`s **every** untracked chunk in the session's sealed directory,
whether or not this run wrote one. Without that, an orphan chunk would sit
untracked forever — a permanently dirty tree, the exact disease this
design removes. With it, no line is lost, no chunk is ever partial, and no
orphan survives the next commit.

```text
seal(session):
    acquire flock(<repo>/.punt-labs/local/ethos/sessions/<session-id>.lock)
    delete the seal's own stale .audit-*.jsonl.tmp        # foreign *.tmp left untouched
    verify each sealed chunk parses and last ts == filename <last>   # else exit 2 (escape: quarantine)
    watermark = max ts over sealed chunks (names; legacy file scanned; .quarantine markers)
                # a marker present but unparseable does not contribute; its .corrupt is orphan, not covered
    tail = [ line in live : ts(line) > watermark ]        # complete lines; skip torn tail + terminated unparseable (stderr count)
    if tail is not empty:
        first = ts(tail[0]); last = ts(tail[-1])
        write tail to sessions/<dir>/.audit-<first>-<last>.jsonl.tmp
        f.Sync(tmp)
        rename tmp -> sessions/<dir>/audit-<first>-<last>.jsonl
    release flock
    git add every untracked chunk in sessions/<dir>/   # outside the lock; audit-<..>-<..>.jsonl, or log-<sid>-<..>-<..>.jsonl in a mission dir
```

The flock serializes the whole watermark-scan-and-write against live
appends and against a concurrent seal of the same session, so two
overlapping seals in **one tree** cannot select overlapping tails: the
first advances the watermark by writing its chunk, and the second,
entering the lock after, seals only what follows. Within a single branch
lineage the chunks are therefore disjoint, contiguous timestamp ranges
with no overlap and no gap. The flock does **not** serialize across
branches, and the watermark is tree-derived, so a branch rewind can
produce overlapping chunks in the merged history (§Cross-branch re-seal);
that overlap is resolved at read time.

### Cross-branch re-seal produces overlapping chunks, resolved at read

One session routinely commits on more than one branch — the multi-PR
workflow this design is built around (§per-branch rejected). That workflow
makes chunk overlap not just possible but expected:

1. Session S accumulates live lines `t1..t100`.
2. A commit on branch A seals `audit-<t1>-<t100>.jsonl`, committed on A.
   The live file still holds `t1..t100` — the seal does not truncate it.
3. `git checkout B` (B forked before A merged). A's chunk is not on B, so
   B's sealed directory for S is empty. The live file is untouched (it is
   in `.punt-labs/local/`); more appends grow it to `t150`.
4. A commit on B scans an empty sealed directory → watermark `0` → seals
   `t1..t150` into `audit-<t1>-<t150>.jsonl`, committed on B.
5. A and B both merge to main. The session directory now holds **both**
   `audit-<t1>-<t100>` and `audit-<t1>-<t150>`, overlapping on `t1..t100`.

**Re-sealing on B is correct and must not be suppressed.** If B is the
branch that merges and A is abandoned, only B's chunk carries `t1..t100`;
a high-water mark that stopped B from re-sealing them would leave the
merged history missing those lines — the audit record would not travel
with the work that landed. The overlap is the price of never losing the
record, and it is cheap to pay at read time.

**The read resolves it by `(session, ts)` dedup.** Because both copies of
line `t_i` came from the *same* append-only live file, equal `ts` means a
byte-identical line, so collapsing to one line per `(session, ts)` cannot
drop a distinct event — the monotonic-ts discipline already gives distinct
events distinct timestamps. This holds only for lines written under that
discipline: post-upgrade chunk and live lines dedup on `(session, ts)`.
Frozen legacy lines (§Migration) are **not deduped at all** — no mechanism
ever copies a legacy line into two places (the seal selects its tail from
the live file only, never from a frozen chunk), so the legacy pool has no
duplicate to collapse, and deduping it could only drop a distinct event
whose coarse-clock `ts` (or byte content) happened to match another's
(§`ethos audit show`). This keys on the ruled `(session, ts)` identity; it
is a read-time collapse, not a `seq` field and not a merge driver.
Disjointness (I11-chunk) therefore holds only *within a single branch
lineage*; across a merged history the read tolerates the overlap.

### Seal triggers: pre-commit repo-wide, mission close secondary

**Primary — pre-commit.** A `pre-commit` hook runs `ethos audit seal`. It
visits **every** session directory in the repo — the union of those under
`.punt-labs/local/ethos/sessions/` (live lines to seal) and those under the
tracked `.punt-labs/ethos/sessions/` (chunks to stage) — seals each
session's not-yet-sealed live lines, and `git add`s **every** untracked
chunk it finds, whether this run wrote it or a prior crashed seal left it
(§Write atomicity). Visiting the sealed tree too, not only sessions with
pending live lines, is what makes orphan recovery reachable: a session whose
crash left an untracked chunk but no pending live lines — or whose live file
is already gone — is still visited and its orphan staged. Because pre-commit
runs *before* git snapshots the index, the freshly staged chunks land in the
**same commit** as the work they document.

The seal is repo-wide, not scoped to the committing session. In a
squash-merge world, attributing which commit carried which session's
lines is a non-goal: a PR is squashed to one commit before it lands, so
per-commit audit attribution has no consumer. Sealing every pending live
line at each commit means an orphaned or crashed session's lines land at
the next commit automatically, with no liveness probe and no
session-attribution logic. Any commit in the repo drains the repo's
pending audit lines into tracked chunks.

Pre-commit — not commit-msg — because commit-msg runs after the index
snapshot is taken; a chunk staged there is too late to enter the commit.
The commit-msg trailer hook (DES-054) stays as it is; sealing needs the
earlier hook. Both install through the same `install.sh` path that
already places `commit-msg`.

**Secondary — mission close.** `ethos mission close` (Tier B) seals the
closing mission's session and its `log.jsonl` so a closed mission's
record is complete on disk even if no commit immediately follows. Mission
close is a deliberate lifecycle boundary; it is the natural second seal.

**Session end — courtesy flush, and the worktree-teardown mitigation.** A
`SessionEnd` hook MAY call `ethos audit seal`. For a long-lived checkout it
is pure courtesy — a crashed session's lines sit in the out-of-tree live
file until the next commit's repo-wide seal picks them up, clean tree, no
loss. For a **worktree or checkout that is about to be deleted** it is the
mitigation for a real loss window (§Migration, live-file loss): the live
file lives inside the checkout, so removing the checkout destroys any
unsealed lines with it.

In a normally-mounted repo the flush seals the tail into the tracked tree
before teardown, so no lines are lost. In a **gitlink-mounted** repo (a
consuming repo before bead `e29s`) the sealed tree is the wrong target, so
the flush defers exactly like every other seal there — a one-line stderr
notice, nothing written (§Seal failure policy). Deleting such a checkout,
whether by hook-driven cleanup or a hookless `rm -rf`, therefore destroys
its unsealed live lines.

The design accepts this as a **bounded pre-`e29s` limitation**. In a gitlink
mount the sealed tree is unreachable, so there is nowhere in the repo to seal
to; a home-tree transit copy was weighed and rejected as a write-only
subsystem (§Rejected alternatives). The org rule is to land `e29s` — vendor a
repo's
`ethos` subtree — before relying on that repo's audit trail, and the
`punt-4yy` campaign is the vehicle for that rollout. Once `e29s` lifts the
gitlink restriction the teardown flush seals directly into the tracked tree
and the loss window closes.

### Concurrency

**Seal versus a live append mid-seal.** The seal acquires the very lock
the live writer serializes on — the per-session flock that already gates
appends in DES-054 (I10-audit-atomic) and now also gates the
monotonic-timestamp allocation. While the seal holds it, no live append
can interleave. A line being written concurrently is simply not yet in
the file when the seal reads it, so it falls into the next seal's tail. No
line is dropped (every complete line eventually has `ts > watermark` for
exactly one seal) and none is duplicated (each seal's tail starts strictly
past the previous watermark). Appends are one line plus an fsync, so the
seal blocks the writer only briefly.

**Two seals at once (concurrent commits, same repo).** Different sessions
seal into different session directories under different per-session
flocks — disjoint, no contention. Two seals of the *same* session
serialize on that session's flock: the first writes a chunk and advances
the watermark, the second seals only the lines past it. The `git add`
runs after the lock is released and only ever adds a whole new file, so a
concurrent `git add` never sees a partial chunk.

### `ethos audit show`: sealed chunks plus live tail

`ethos audit show --delegation <id>` must see both sealed and
not-yet-sealed entries. This read *replaces* DES-054's early-return
("the repo file exists, return it") with a chunk union: the two
post-discipline pools (sealed chunks and the live tail) dedup on
`(session, ts)`, and the frozen legacy pool passes through undeduped:

```text
read_audit(session):
    monotonic   = sealed chunks named audit-<first>-<last>.jsonl   # any order; mission dir: log-<session-id>-<first>-<last>.jsonl
    legacy      = frozen audit.jsonl if present                    # no ts in its name; mission dir: frozen log.jsonl
    quarantined = ranges named by <chunk>.quarantine markers       # <chunk> = the namespace's stem
    orphan      = .corrupt files with no covering parseable .quarantine marker   # torn marker reads absent; quarantine crashed mid-verb
    if orphan: error naming them (exit 2)     # resume the quarantine, never skip silently
    for c in monotonic:                       # I11-chunk writes chunks whole, so
        if c does not parse to completion     #   a torn or mismatched sealed chunk
        or last ts(c) != filename <last>(c):  #   is corruption, not a skippable tail
            error naming c (exit 2)           #   escape is `ethos audit quarantine`
    Sm = concat(read(c) for c in monotonic)
    Sl = read(legacy) if present else []      # drop torn final line; skip a terminated
                                              #   unparseable line with a stderr count
    watermark = max line ts in (Sm ++ Sl) or in quarantined ranges   # 0 if none
    L = read(live)                            # local tree; [] if absent; drop torn tail;
                                              #   skip a terminated unparseable line (stderr count)
    tail = [ line in L : ts(line) > watermark ]
    # post-discipline lines collapse on (session, ts); legacy lines pass through
    return stable_sort_by_ts( dedup_by_ts(Sm ++ tail) ++ Sl )   # stable: legacy equal-ts lines keep file order; + a gap marker per unrecovered quarantined range
```

**Dedup — `(session, ts)` for post-discipline lines, none for legacy.**
Cross-branch re-seal (§above) can leave two chunks whose ranges overlap, so
the read collapses duplicates. For lines written under the monotonic-ts
discipline (post-upgrade chunks and the live tail) this is a `(session, ts)`
collapse and is loss-free by construction: both copies of a given `ts` came
from the *same* append-only live file, so equal `ts` means a byte-identical
line, and the discipline gives distinct events distinct timestamps — dedup
can never merge two different events. The frozen legacy `audit.jsonl`
(§Migration) is **not deduped at all**. No mechanism ever duplicates a legacy
line: `Sl` is one frozen file read once, and the seal never copies a legacy
line into a chunk (its tail comes from the live file only), so the legacy
pool holds no duplicate to collapse. A legacy dedup could therefore only
*drop* a distinct event — the pre-discipline clock can hand two real tool
calls the same `ts`, and with identical redacted input those two lines are
byte-identical, so neither a `ts` collapse nor a byte collapse is safe. Legacy
lines pass through untouched. The final merge sorts by `ts` **stably**, so two
legacy lines the pre-discipline clock stamped with the same `ts` keep their
original file order — without that guarantee the "output identical to the
old single-file read" acceptance criterion would not hold for an equal-ts
legacy pair. The two pools never mix: a continuing session
seeds its live writer's clock above the legacy max (§monotonic timestamp), so
every legacy `ts` sits strictly below every post-discipline `ts`, and the
post-discipline dedup never reaches a legacy line. Where no overlap exists
(the common single-branch case) the dedup is a no-op and the result is
exactly the ordered stream the single tracked file returned before this
change. If a chunk is absent because a seal crashed before `git add` but its
lines are still in the live file, they reappear in the tail (their `ts` still
exceeds the last *sealed* watermark), so the read is complete even mid-seal.

**Corruption is surfaced, not skipped.** I11-chunk writes every monotonic
chunk whole (temp + fsync + rename), so a torn final line inside such a
chunk, or a last line whose `ts` disagrees with the `<last>` in the chunk's
name, is corruption evidence, not a recoverable partial write. The read
**errors, naming the chunk** (exit 2), rather than dropping the line and
leaving the read silently short; the seal makes the same check when it scans
(§Watermark). The escape from that exit 2 is `ethos audit quarantine`
(§Seal failure policy): once a corrupt chunk is quarantined, the read no
longer errors on it — the lines quarantine could re-seal from the live file
reappear as an ordinary chunk, and only the **unrecovered** sub-range the
marker records surfaces as an **explicit gap marker** in the output, so the
reader sees which lines were truly lost to corruption and when, never a
silent hole. A `.corrupt` file with **no** covering marker is a quarantine
that crashed mid-verb (§Seal failure policy); the read errors on it (exit 2)
exactly as the seal does, prompting the resume rather than passing over the
half-retired chunk in silence. Only a legacy
`audit.jsonl`, whose name carries no timestamp to contradict, keeps the
tolerant drop-a-torn-final-line rule.

**Deferred lines are flagged, not hidden.** In a gitlink-mounted repo the
seal is a deferred no-op (§Seal failure policy), so the live file can hold
lines past the sealed watermark that no chunk yet records. `ethos audit
show` detects this — a live tail above the watermark in a gitlink-mounted
`.punt-labs/ethos/` — and flags the session on stderr (`N unsealed lines,
sealing deferred until vendored`) so the reader knows the sealed record is
deliberately, temporarily incomplete rather than silently short.

### Mission-tree churn: `log.jsonl` and `.lock`

`missions/<id>/log.jsonl` and `.create.lock` (and any per-mission
`.lock`) have the same tracked-plus-live character as `audit.jsonl`.
They are handled by the same principles, split by kind.

**`log.jsonl` — live-write and chunk-seal, primary seal at mission
close.** The mission log is tracked shared history like the audit log,
but it is appended only at operator-driven lifecycle events (create,
dispatch, result, reflect, close), not per tool call. It takes the same
treatment: the live writer appends to the `local` mission log
(`<repo>/.punt-labs/local/ethos/missions/<id>.jsonl`), and each seal
writes a new immutable chunk under
`<repo>/.punt-labs/ethos/missions/<id>/log-<session-id>-<first>-<last>.jsonl`.
The **mission close** is the authoritative seal, because it is the point
at which the mission's record is complete; the repo-wide pre-commit seal
is the clean-tree backstop that also drains pending mission-log lines. One
mechanism (the chunk seal), two triggers.

**Why the chunk name carries the sealing session id.** An audit chunk is
collision-free across sessions for free: each session seals into its **own**
dated `sessions/<dir>/`, so two sessions' chunks are never siblings and can
never share a name (§Chunk sealing). A mission chunk has no such isolation —
**every** session that touches mission `<id>` seals into the one shared
`missions/<id>/` directory. The per-session monotonic-ts discipline
(§timestamp) makes `<first>` unique only *within* a session; it does not span
checkouts. Two checkouts running two sessions, each appending different
mission events, could therefore mint identically named
`log-<first>-<last>.jsonl` chunks holding **different content** — an add/add
merge conflict, the exact disease chunk sealing exists to remove. The
`<session-id>` segment restores the guarantee: two chunks can share a name
only when they share a session, where strictly-monotonic `ts` makes `<first>`
unique, so no collision is possible; chunks from different sessions have
distinct names and merge additively. The session-id segment supplies what
the shared mission directory does not and the audit chunk's dated directory
does.

**Read and watermark are per-session.** The mission-log read is the union
across **all** sessions' chunks in `missions/<id>/`, stable-sorted by `ts`
with line identity `(session, ts)` — the same rule the audit read applies
(§`ethos audit show`). The seal watermark is **per-session**: a session's
watermark is the max `<last>` over the chunk names carrying **its own**
`<session-id>`, so one session's seal neither counts another session's chunks
as already sealed nor skips its own lines for want of a watermark it cannot
derive. This mirrors the audit read and watermark exactly; only the source
directory is shared rather than per-session, which is precisely why the name
must name the session.

**`.lock` — never tracked; move to the global tree.** A lock file is not
content and must not enter shared history. DES-054 placed the
per-mission shared `.lock` in the repo tree; the confirmed consequence is
`.create.lock` sitting in `git ls-files`. This design moves every
per-mission lock to the global tree at
`~/.punt-labs/ethos/missions/<id>.lock` and the create fence to
`~/.punt-labs/ethos/missions/.create.lock`, joining the session and
per-delegation flocks that already live there (DES-054 concurrency
table). No lock file remains in any repo tree, so no lock file can dirty
one. DES-054 justified the in-repo lock by cross-checkout inode sharing
for delegation-ID allocation; a global lock keyed by the globally-unique
mission ID is strictly *more* correct for that, because two checkouts of
the same repo then contend on one lock inode instead of one-per-checkout.
Existing tracked `.create.lock` files are untracked **and removed from
disk** by the migration below, and the code stops writing any in-repo
lock — untracking alone would leave an untracked file that re-fails the
clean-tree gate. This lock relocation is unchanged from the earlier
draft; the chunk rulings do not touch it.

### Invariants

Preserved unchanged:

- **Every tool call is logged; no gaps, no off switch.** The write path
  is byte-for-byte the DES-054 path; only the destination file moves. No
  suppression sentinel is introduced. A seal that cannot run **fails the
  commit** (below) rather than dropping the record.
- **Redaction before shared history.** Redaction fires at write time,
  before the line reaches the live file, and therefore before it can ever
  reach a sealed chunk. The live file holds redacted lines (§ above).
- **`tool_input_hash` over the redacted form.** Unchanged; DES-052
  Stat-Write detection is untouched.
- **Crash tolerance.** Per-line `f.Sync()` on the live file; the writer
  truncates a torn tail on reopen and the reader skips a torn tail in a live
  file not yet reopened; the seal skips a torn live tail and writes chunks
  via temp + rename. A torn final line *inside a sealed chunk* is not
  tolerated — it is corruption, surfaced as an error (§`ethos audit show`,
  §Watermark).

Amended and added (proposed for the DES-054 invariant block):

```text
-- I10-audit-atomic (AMENDED): appends now target the LIVE session log,
-- and each append allocates a strictly-monotonic per-session ts under
-- the SAME flock (ts = max(now, last_ts + 1ns)). Sealed chunks are never
-- appended by a live writer, only written whole by the seal step.
I10-audit-atomic: forall e1, e2 appended to
        <repo>/.punt-labs/local/ethos/sessions/<session-id>.audit.jsonl:
    flock(<session-id>.lock) held during each append
    /\ write_order(e1, e2) = file_position_order(e1, e2)
    /\ (file_position_order(e1, e2) <-> ts(e1) < ts(e2))
    /\ ts strictly monotonic per session, allocated under the flock
    /\ ts(e) > max ts of the session's sealed chunks at append time

-- I11-chunk (NEW): every sealed chunk is written exactly once and never
-- modified WHILE IT REMAINS NAMED IN THE CHUNK NAMESPACE. `ethos audit
-- quarantine` retires a corrupt chunk by renaming it OUT of that namespace
-- (to <name>.corrupt) -- a bookkeeping move that leaves the bytes unchanged
-- and is the mechanism by which a corrupt file stops being "a chunk" under
-- this invariant, not a content rewrite. Within a single branch lineage the
-- chunks of a session are disjoint, contiguous ts ranges (the watermark is
-- tree-derived, so the flock serializes seals only within one tree). A
-- branch rewind can produce chunks that overlap in a merged history; that
-- overlap is resolved at read (I12-merge), not forbidden here.
I11-chunk: forall chunk C of session:
    C is created by one seal via temp+rename and, while named a chunk, never
        rewritten (quarantine renames C out of the namespace, bytes intact)
    /\ forall lines l in C: watermark_before(C) < ts(l) <= last(C)
    /\ forall chunks C1 != C2 in ONE tree state:
           ts-range(C1) disjoint ts-range(C2)

-- I11-idem (NEW): sealing is lossless — every complete live line is
-- sealed into at least one chunk after a seal that follows its write.
-- Re-seal on a rewound branch may place a line in more than one chunk;
-- the read collapses those by (session, ts).
I11-idem: forall complete line L in live(session):
    (a seal runs after L was written -> L is in >= 1 sealed chunk)
    /\ (duplicate copies of L share (session, ts) and are byte-identical)

-- I12-merge (NEW): audit show reconstructs the full stream as the union
-- of the sealed chunks and the live tail past the sealed watermark. Lines
-- written under the monotonic-ts discipline (post-upgrade chunks + live)
-- dedup on (session, ts) -- loss-free, since equal ts implies a
-- byte-identical line from the same append-only source and distinct events
-- get distinct ts. Frozen legacy lines predate the discipline (two distinct
-- events may share a ts) AND have no duplication source (the seal never
-- copies a legacy line into a chunk), so they are NOT deduped -- any legacy
-- collapse could only drop a distinct event. The two pools never mix (every
-- legacy ts < every post-upgrade ts). A monotonic chunk that does not parse
-- whole, or whose last ts != its filename <last>, is corruption and surfaces
-- as an error, never a drop.
I12-merge: read_audit(session)
         = stable_sort_by_ts(                 -- stable: legacy equal-ts lines keep file order
               dedup_by_ts( monotonic_chunks(session)
                            ++ { l in live(session) : ts(l) > max sealed ts } )
               ++ legacy_chunk(session) )
```

### Seal failure policy

A commit that should carry an audit record but cannot seal it is exactly
the gap the audit system exists to prevent. But "cannot seal" and
"nothing to seal" must not be confused, or an unwritable audit store would
hard-block every unrelated commit on the machine. Three classes, three
behaviors:

**Nothing to seal — exit 0.** If the repo has no
`.punt-labs/local/ethos/sessions/` directory (`ENOENT`), or it exists but
holds no unsealed live lines, the hook touches nothing and exits 0. A repo
with no ethos sessions and a commit outside any session are clean no-ops.
One cross-check guards against a silent vacuum, and it is **per session**,
not per zone. It iterates two sources: each session the roster records as
active and bound to this repo — by its recorded checkout path, any path
(DES-054) — **and** each purge tombstone whose recorded repo is the
committing repo and that carries the unsealed-lines flag (§Two zones). The
tombstone records the repo and recorded checkout path it was purged from, so
the check scopes tombstones to the committing repo and derives the live-file
path from that recorded path and the session id — without it the tombstone
branch has nothing to `stat` and nothing to scope by, and would either revert
to dead code or warn in every repo forever. For each source it `stat`s the
session's recorded live file and
**warns on stderr naming any session whose live file is absent** (still exit
0 — a missing live file must not block an unrelated commit, but it must not
pass unremarked either, because it can mean a checkout was deleted, or a
single session's live file removed, with unsealed lines, §Migration). A
tombstone warning **repeats at every commit** in that repo until the operator
acknowledges it with `ethos session purge --ack <session-id>`, which **renames**
the tombstone to `<session-id>.purged.acked` — a retained record that is never
warned on again. Like the `.corrupt` rename, this is a per-exact-filename
never-overwrite: one session id can be purged twice — a force-purged session
re-registers in the roster and a later purge writes a fresh
`<session-id>.purged` at the freed name — so acking when a
`<session-id>.purged.acked` already exists must not `rename(2)` over it and
destroy the first loss record; the ack instead retires under a content-derived
`<session-id>.purged.acked-<hash-of-tombstone-bytes>` suffix — a stable,
collision-averse name, not a cross-checkout merge key (the tombstone lives in
the home tree and never crosses checkouts). Acking onto an existing identical
`.acked-<hash>` retires the tombstone under the first free name in a
deterministic sequence — `.acked-<hash>-2`, `-3`, … — rather than refusing, so
two acks of byte-identical tombstones stay two records, event multiplicity
survives, and the warning-bearing tombstone is always retirable. The
acknowledgment retires
the signal without discarding the
evidence: the loss record survives the ack, so the signal neither extinguishes
itself nor becomes permanent cross-repo noise — the sanctioned ack can never
refuse forever, so an unsanctioned hand-rename is never the only exit — and
acknowledging the loss does not erase the fact that lines were lost.
Iterating tombstones is what keeps the crash → purge → checkout-deleted →
commit sequence from going silent: purge removes the roster entry the
per-session check would have visited, so without the flagged tombstone the
one guard built to catch a lost live file would never look at it. The order
**crash → checkout-deleted → purge → commit** is caught the same way: purge
finds the live file already gone, warns, and sets the flag in its "live file
already gone" variant (§Two zones), so the tombstone still carries the
evidence. A per-zone
check ("the local zone is missing") would miss both cases: a surviving
checkout always has its own zone, and a single deleted live file leaves the
zone present.

**Gitlink-mounted seal target — exit 0 with notice.** In a consuming repo
`.punt-labs/ethos/` is a submodule (gitlink) until bead `e29s` lands, so
the sealed-chunk target `<repo>/.punt-labs/ethos/sessions/<dir>/` is the
wrong tree and writes there are blocked. In that configuration the seal is
a deliberate **no-op exit 0**: it detects the gitlink mount and skips
sealing, leaving the live lines in `.punt-labs/local/`. It is not silent —
it **prints a one-line stderr notice** naming the repo and the deferral
reason (`sealing deferred: .punt-labs/ethos is a gitlink mount, pending
e29s`), and `ethos audit show` independently flags any session whose live
file holds lines past the sealed watermark in such a repo (§`ethos audit
show`). The clean-tree goal is already met without sealing — the live zone
is gitignored, so `punt release` preflight passes — and the sealed record
defers until `e29s` provides the correct target, at which point the deferred
lines seal on the next commit. This deferral is distinct from the I/O class
below: a gitlink mount is an expected deployment state, not a failure, so it
must not exit 2 and block every commit in every consuming repo. What it must
not be is *unbounded silence*: the per-commit notice and the `audit show`
flag keep the deferred window visible until `e29s` closes it.

The **teardown** invocation of this deferral is the one place the deferral
notice reaches no one, and that is part of the accepted limitation, not a loud
case. When the deferring seal is the `SessionEnd` worktree-teardown flush
(§Seal triggers) in a gitlink mount, its one-line notice rides the exit-0
`SessionEnd`-hook stderr — the channel Claude Code does not surface (§timestamp,
exit-0 hook stderr) — so unlike the pre-commit deferral it has no visible
backstop: the `audit show` flag reads the live tail, and the live tail is
destroyed with the checkout moments later. The deferral is therefore
**unannounced and post-hoc undetectable**: after deletion nothing records that
the lines existed. This is exactly the bounded pre-`e29s` limitation stated in
§Seal triggers, not a recoverable case: the gitlink mount has no in-repo target
to seal to, a durable deferral record was rejected as a write-only subsystem
(§Rejected alternatives), so the org rule is to vendor the repo (`e29s`, the
`punt-4yy` campaign) before relying on its audit trail.

**Cannot seal — exit 2.** An I/O error — reading the live tree, writing a
chunk, or the final `git add` fails (`EACCES`, `EIO`) — is fail-closed in
the DES-055 make-check hook shape (stderr + exit 2, blocking the commit).
The message names the session and the failing path and is self-contained: it
does not re-read the tree that just failed to explain itself, so it is still
legible when the audit store is the thing that is broken. Three more
conditions join this class: a malformed chunk filename in the sealed
directory (§Watermark), a **corrupt sealed chunk** — one that does not parse
to completion, or whose last line `ts` disagrees with its filename `<last>`
(§Watermark, §`ethos audit show`) — and a **`git add` failure** staging a
new or orphan chunk or a quarantine artifact. A corrupt sealed chunk *is* the divergence an earlier
draft claimed could not exist: I11-chunk makes it unreachable by normal
operation, so if one appears the store is damaged and the seal must fail
loudly rather than seal past it or silently drop its lines.

**The specified escape is `ethos audit quarantine`, never `--no-verify`.**
Fail-closed must never leave bypass as the only exit — an operator faced with
"every commit in this repo is blocked" and no sanctioned recovery will reach
for `git commit --no-verify`, which disables the seal wholesale and recreates
the exact no-audit gap the system forbids. A corrupt sealed chunk is
committed history and I11-chunk forbids rewriting a file while it is named a
chunk, so the recovery is not repair but explicit, recorded retirement —
renaming the corrupt bytes out of the chunk namespace (I11-chunk).

`ethos audit quarantine <chunk>` does four things, in order. The order is
load-bearing: retirement precedes recovery so the re-seal cannot clobber the
chunk it is recovering, and the marker is the last durable artifact so a crash
before it leaves a resumable, loud state (below), never a silent hole.

1. **Retires the corrupt chunk first.** It renames
   `audit-<first>-<last>.jsonl` to `audit-<first>-<last>.jsonl.corrupt`, which
   leaves the `audit-<…>.jsonl` glob so seal and read stop treating the name
   as a chunk. The `.corrupt` file is **committed** — a recognized exception
   to the name check (§Watermark): clones carry the bytes as evidence.
   Retiring first **frees the chunk's name on disk**, so a re-sealed chunk that
   takes that name cannot clobber a still-named corrupt chunk. The re-seal
   (step 2) writes an **ordinary content-named chunk**: its
   `audit-<first>-<last>.jsonl` name is derived from the actual first and last
   `ts` of the lines it re-seals, so on a **partial** recovery — the live file
   still holds only `[first, C]` with `C < last` — the chunk is named
   `audit-<first>-<C>`, and it coincides with the retired chunk's name only when
   recovery is **full** (`C == last`). Naming from content is what keeps the
   content-vs-name corruption check (a chunk's last line `ts` must equal its
   filename `<last>`) from ever firing on a re-sealed chunk; a chunk minted under
   the retired `<last>` on a partial recovery would carry a last `ts` of `C` and
   re-brick the store. As defense in depth a fresh run's re-seal still refuses if
   its target name already exists; the resume path (below) is the sole exception,
   where an existing target that parses whole and matches the recovery range is
   the completed re-seal, not a collision.
2. **Re-seals what is still readable.** From the live file it re-seals every
   line of the corrupt chunk's `[first, last]` range still present there (the
   live file is never truncated, §Migration). Those lines become an ordinary
   new chunk; the read's `(session, ts)` dedup already tolerates the overlap.
   This step determines the **actually-unrecoverable** sub-range — the part of
   `[first, last]` the live file no longer holds.
3. **Writes a tracked quarantine marker** `audit-<first>-<last>.quarantine`
   with **deterministic content only**: the corrupt chunk's name, the
   **verified** content-derived `<last>` (the max of the corrupt bytes' own
   last `ts` and the ts of the lines just re-sealed), the unrecovered
   sub-range, and the corruption reason. It carries **no wall-clock
   timestamp** — "when" lives in the git commit metadata. Deterministic
   content is what lets two checkouts that quarantine the same chunk from the
   same state produce byte-identical markers and re-sealed chunks, so their
   commits merge with no conflict (below). The marker is written with the same
   temp + `f.Sync()` + rename discipline as a chunk (§Write atomicity), so it
   never appears in a torn state; a marker that nonetheless fails to parse is
   treated as **absent** by every consumer — seal, read, and the resume state
   machine alike — so a half-written marker uniformly reads as the resume
   state, never as completion. The marker is an explicit, visible audit event,
   not a silent skip.
4. **Stages everything itself.** Like the lock-relocation migration
   (§Mission-tree churn), the verb performs the git operations so the tree is
   clean and committable with no hand-staging: `git mv` semantics for the
   `.corrupt` rename and `git add` for the marker, the re-sealed chunk, and
   every other quarantine artifact the marker covers, including any
   `.corrupt-<hash>` a prior event left.
   After the verb runs, `git status` shows only staged changes — the operator
   stages nothing separately. The marker contributes to the watermark
   (§Watermark) its **verified** `<last>`, never the filename `<last>` on
   faith: were the marker to inherit a bogus inflated filename `<last>` larger
   than anything ever written, every future live line up to it would silently
   never seal.

`ethos audit show` then surfaces each unrecovered sub-range as an **explicit
gap marker** in its output (§`ethos audit show`) — the reader sees exactly
which lines were lost to corruption, rather than an error or a silent hole.
Retiring the chunk therefore does not regress the watermark and no phantom
re-seal of the covered range fires.

**Resume state machine.** Because the marker is the last durable artifact,
quarantine is defined by which of the three artifacts exist, so any crash
mid-verb resumes deterministically:

- **Chunk present, no `.corrupt`** — a fresh run: retire, re-seal, marker,
  stage, as above.
- **`.corrupt` present, no covering marker** — a crash retired the chunk but
  the re-seal, marker, or stage did not finish. A marker present on disk but
  unparseable counts as **absent** here, so a crash during the marker write
  itself lands in this same state. This is a **resume**: re-running
  `quarantine` completes the re-seal, writes the marker, and stages every
  quarantine artifact the marker covers — the `.corrupt`, the marker, the
  re-sealed chunk, and any `.corrupt-<hash>` a resume-window retirement
  produced. On resume the freed name may already hold the crashed run's own
  re-sealed chunk; a chunk standing there that parses whole and matches the
  recovery range **is** that completed re-seal, so the resume verifies it and
  proceeds to the marker and stage rather than refusing — the
  refuse-if-target-exists guard is a fresh-run rule only. A chunk standing
  there that **fails** verification — parses short of the recovery range, or
  its last `ts` disagrees — is fresh damage during the resume window, retired
  under the same content-derived `<name>.corrupt-<hash-of-corrupt-bytes>`
  suffix as the complete state (per exact filename, so it never collides with
  the first event's `.corrupt`); the resume then re-seals, writes the marker,
  and stages. Seal and read do
  **not** pass silently over this state — a `.corrupt` with no covering marker
  is corruption bookkeeping left half-written, so they treat it as an error
  (exit 2) that prompts the resume, exactly as the original corrupt chunk did.
  The mid-verb window is loud, not a silent hole.
- **`.corrupt` present, covering marker present** — the quarantine is
  complete: an **idempotent no-op** that also verifies every quarantine
  artifact the marker covers — the `.corrupt` file, the marker, the re-sealed
  chunk, and any `.corrupt-<hash>` — are all **staged**, staging any a crash
  left on disk but unstaged, so a re-run never leaves the tree dirty. The
  no-op is not blind to fresh damage: it **content-verifies any chunk now
  standing at a name the marker covers**, and if that chunk is itself corrupt
  this is a **new** quarantine event, not a completed one. The verb retires it
  under a content-derived suffix — `<name>.corrupt-<hash-of-corrupt-bytes>`,
  deterministic across checkouts that see identical damage and distinct across
  different damage — re-seals what the live file still holds, and updates the
  marker under the existing smaller-unrecovered-range/union merge rule (below).
  The no-op also **requires a chunk to stand for the marker's recovered
  range**: where none does while the live file still holds lines of that range
  outside the recorded unrecovered sub-range — the window a crash between
  retirement and re-seal leaves — it re-runs the re-seal (step 2) before
  declaring completion, so the recovered lines cannot sink below the watermark
  unsealed and vanish from the read.

A second `quarantine` over a range no marker covers is an error. A rename onto
an existing `<name>.corrupt` never overwrites — the verb refuses rather than
destroy the first quarantine's evidence — and that never-overwrite is **per
exact filename**, so a second event's `<name>.corrupt-<hash>` never collides
with the first event's `<name>.corrupt`; a byte-identical `<name>.corrupt-<hash>`
already on disk is retired under the first free name in a deterministic sequence
(`<name>.corrupt-<hash>-2`, `-3`, …) rather than refusing — the sequence *is*
the never-overwrite, so no compound-coincidence state ever leaves a corrupt
chunk standing with `--no-verify` as the only exit.

**Cross-checkout.** Recovery is **repo-wide only once the quarantine commit
merges**: until it propagates, every other checkout and every fresh clone
still hits the corrupt chunk and exits 2 with the same escape notice —
bounded and loud, never silent. Two checkouts that quarantine the same chunk
from the **same** state produce byte-identical `.corrupt`, marker, and
re-sealed chunk, so their add/add merges clean (§Acceptance criteria). Two
that quarantine from **divergent** states — one live file still held more of
the range than the other — can produce an ordinary marker conflict; it is
resolved by keeping the marker with the **smaller** unrecovered range plus the
union of the re-sealed chunks, so the checkout that recovered more wins. The
exit 2 on first detection stays: the store fails loud, and quarantine is the
loud, recorded, self-staging way back to a committable tree.

## Migration

Existing tracked `audit.jsonl` and `log.jsonl` files are **frozen
historical chunks** and stay exactly as they are. Each is read as the
oldest chunk of its session, sorting before any timestamped chunk. The
write path moves to the `local` zone; nothing rewrites history. The
migration is idempotent, supports `--dry-run`, and follows the shape of
`ethos audit migrate` (atomic per-unit, partial-failure resume).

- **`ethos audit seal [--dry-run] [--verbose]`** is the new verb the
  pre-commit hook and mission close both call. It visits **every** session
  directory in the repo — the union of those under
  `.punt-labs/local/ethos/sessions/` (live lines) and those under the
  tracked `.punt-labs/ethos/sessions/` (existing chunks) — seals each
  session's unsealed live lines into one new chunk, and stages **every**
  untracked chunk it finds, including an orphan a prior crashed seal left in
  a session that now has no pending live lines (§Write atomicity). Visiting
  the sealed tree, not only sessions with pending live lines, is what keeps
  orphan recovery reachable. Idempotent: a session whose live lines are all
  at or below its watermark and whose chunks are all tracked seals nothing
  and exits 0. Atomic per session (write chunk via temp + rename under the
  flock, then `git add`); a failure mid-run leaves earlier sessions sealed
  and the failing one untouched, so the next run resumes. `--dry-run` prints
  the per-session line counts it would seal without writing.
- **`ethos audit quarantine <chunk>`** retires a corrupt sealed chunk that
  fails the seal/read content check (§Seal failure policy): it re-seals any
  still-readable lines from the live file, renames the corrupt chunk to
  `<name>.corrupt`, writes a tracked `.quarantine` marker holding the
  watermark at the **verified** loss point, and **stages both the rename and
  the marker itself** (`git mv` + `git add`), so after the verb the repo is
  clean and committable without `--no-verify` and the loss becomes a visible
  audit event and an explicit read-time gap marker, never a silent skip. A
  second run over a covered range is a no-op; it never overwrites an existing
  `.corrupt`.
- **No seeding of the live file.** Chunks are additive: a session that
  already has committed chunks starts its watermark at the max `ts` of
  those chunks (§Watermark), and new live lines are written with `ts`
  strictly greater (§monotonic timestamp). A session alive across the
  upgrade needs no byte copy — its pre-upgrade lines stay in the frozen
  chunk, its post-upgrade lines go to the fresh live file, and the read
  concatenates the two in order.
- **One-time reconciliation of the dirty `audit.jsonl`.** A repo that carries
  a dirty tracked `audit.jsonl` at upgrade holds the old code's uncommitted
  appends. The seal never rewrites a frozen file, so the migration does not
  touch these bytes; they are flushed by a **documented one-time operator
  commit** of the file's final state (`git add audit.jsonl && git commit`),
  after which the writer has already moved to the `local` zone and the tree
  stays clean. It is a reconciliation of history, not a seal — no verb reaches
  into frozen bytes.
- **No deletion of the live file.** Sealing does not delete or truncate the
  live file — it is the append-only, machine-local record under
  `.punt-labs/local/`. It is **discardable only once its lines are sealed**;
  an unsealed live file is the sole copy of those tool calls, so deleting the
  checkout it lives in (worktree removal, `git clean -fdx`) destroys them.
  §Seal triggers names the worktree-teardown seal that mitigates this. The
  seal never deletes the live file either way.
- **Mission-tree `log.jsonl`.** The existing tracked `log.jsonl` is a
  frozen chunk too; new mission-log lines go to the `local` mission log
  and seal into `log-<session-id>-<first>-<last>.jsonl` chunks
  (§Mission-tree churn).
- **Lock relocation — remove from disk, stop writing.** Unchanged by the
  chunk rulings. Two halves, both required. (1) The per-mission `.lock`
  and `.create.lock` are removed from the working tree **on disk**, not
  merely untracked: `git rm --cached` alone leaves an untracked file,
  which re-fails the clean-tree gate this design exists to unblock. The
  step untracks them in the index **and** deletes the working-tree copy
  (moved aside per the org's prefer-`mv` rule, then removed once the
  global-tree lock is confirmed in place). (2) The code **stops creating**
  any lock under `.punt-labs/ethos/` in the repo — the create fence and
  every per-mission lock move to the global tree (§Mission-tree churn).
  Both halves are idempotent (an already-removed lock is skipped) and
  `--dry-run`-able.

## Acceptance criteria satisfied

- **A repo with an active session and no uncommitted work shows clean
  `git status`.** Live appends land in the gitignored `local` zone; the
  tracked tree changes only when a seal stages a new chunk into the
  commit, so the post-commit tree is clean.
- **`punt release` preflight passes against siblings with live sessions,
  no punt-kit change, no exemptions.** No sibling tracked tree is dirtied
  by a live audit, because the live file is gitignored under
  `.punt-labs/local/`. Consumers are unchanged.
- **`ethos audit show` output is identical before and after.** The union
  of sealed chunks and live tail, ordered by `ts` and deduped
  (`(session, ts)` for post-discipline lines; the frozen legacy chunk passes
  through undeduped), reconstructs the same ordered line set the single
  tracked file held — the dedup is a no-op in the common single-branch case
  and collapses only the overlap a cross-branch re-seal leaves.
- **Sealed audit records land in the same PR as the work.** The
  pre-commit seal `git add`s the new chunk into the same commit as the
  staged work.
- **No merge conflicts on the normal write path.** Tracked chunks are
  immutable after creation, so two branches only add distinct files; git
  merges them with no conflict and no driver. Concurrent quarantine of the
  same chunk from identical states also merges cleanly, because the verb's
  artifacts are deterministic (§Seal failure policy); only quarantine from
  divergent states can produce an ordinary marker conflict, resolved by
  keeping the smaller unrecovered range plus the union of re-sealed chunks.

## Rolling-upgrade / transition

Ethos is one binary per machine (installed at `~/.local/bin`), so the
write path flips for every session on a machine when that machine's ethos
is upgraded — there is no per-repo split. Consistent with DES-054's
two-minor-version convention:

- **vX.Y.0** ships the live-write redirect to `.punt-labs/local/`, the
  strictly-monotonic per-session timestamp, the `pre-commit` chunk-seal
  hook (installed by `install.sh` alongside `commit-msg`), the
  mission-close seal, and the `ethos audit seal` verb. From this version
  on, new appends go to the `local` live file and seals write immutable
  chunks into the tracked tree. ethos vX.Y.0 reaches a consuming repo only
  **after** that repo carries the canonical `.punt-labs/local/` gitignore block
  (the `punt-4yy` rollout of the `punt-labs-dir.md` standard) — the live-zone
  sequencing gate that mirrors the `e29s` gate for the sealed tree; flipping the
  write path into a repo that lacks the ignore rule would leave the live file
  untracked-and-unignored and dirty the tree, the disease this design cures
  merely relocated.
- **vX.(Y+1).0** relocates the per-mission `.lock` and `.create.lock` to
  the global tree and removes the tracked-and-on-disk copies (§Migration),
  closing the last in-tree lock file. The gap between the two minors is
  the safety window, matching the DES-054 phase-1/phase-3 spacing.

Three transition properties are explicit:

- **Mid-session upgrade — no seeding needed.** A session alive across the
  vX.Y.0 flip has its pre-upgrade lines in the frozen tracked chunk and
  writes its first post-upgrade append to a fresh `local` live file. The
  writer initializes its monotonic timestamp from the max `ts` of the
  existing chunks (§monotonic timestamp), so new lines sort strictly
  after the frozen history and the read concatenates chunk-then-tail in
  order. No byte copy, no reconciling commit for the audit file: the old
  chunk is already committed history and the new lines seal into a new
  chunk on the next commit.
- **The already-dirty file today.** The repo `audit.jsonl` that is dirty
  *right now* (the bug being fixed) holds uncommitted appends written by
  the old code. After the upgrade the writer stops appending to it; those
  bytes are committed **once** by a **documented one-time operator commit** of
  the file's final state (§Migration) — a reconciliation of history, not a
  seal, since no verb rewrites a frozen file — and from that point the tree is
  clean. New lines go to the `local` file and seal into fresh chunks.
- **Multi-machine repo skew.** "One binary per machine" does not cover two
  machines *sharing a repo* over git. If machine 1 upgrades and machine 2
  has not, machine 2 keeps appending to the tracked `audit.jsonl` (its
  DES-054 primary target), so machine 2's tree stays dirty during the
  window. There is no data loss and — because machine 1 now writes new
  *chunks* rather than appending the same file — no merge conflict either;
  the clean-tree guarantee simply does not hold on machine 2 until it
  upgrades. Those machine-2 appends land in the frozen `audit.jsonl`, so
  they are **legacy lines** and inherit the legacy read rule: machine 1
  passes them through **undeduped**, because machine 2's pre-upgrade clock
  can hand two distinct events one `ts` — deduping the legacy pool could only
  drop a real line, and nothing duplicates a legacy line in the first place
  (§`ethos audit show`). The rule: **all machines sharing a repo should cross
  the boundary together.**

Already-committed audit and mission logs are untouched across the window;
only the append destination and the lock location move.

## Rejected alternatives

- **Live file in the home-dir global tree.** An earlier draft put the live
  file at `~/.punt-labs/ethos/sessions/<session-id>.audit.jsonl`. Operator
  ruling (2026-07-20): use `.punt-labs/local/` — the `local` convention
  already means machine-local-never-committed, the gitignore ships once
  via the punt-kit `punt-labs-dir.md` standard, and per-checkout isolation
  is automatic. The `local` zone also reaches gitlink-mounted repos.
- **Gitignored in-repo sibling under `.punt-labs/ethos/`.** An earlier
  draft weighed a per-session `audit.live.jsonl` sibling and rejected it
  for needing a bespoke per-repo `.gitignore` rollout. The `local` zone
  answers that objection: the ignore rule ships once, org-wide, via the
  standard — not N times per repo.
- **`TMPDIR` / `.tmp/` for the live file.** `.tmp/` is deletable scratch;
  an unsealed audit record is not scratch and must survive a `.tmp` sweep.
- **`.punt-labs/local/<branch>/` per-branch live path.** Sessions span
  branches; a per-branch path fragments one session's log and orphans
  files on branch deletion. Chunk sealing already makes cross-branch
  merges conflict-free, removing the motivation.
- **Append-to-one-tracked-file sealing with a merge driver.** The earlier
  draft sealed by appending to a single tracked `audit.jsonl`, which two
  branches would both grow and conflict on, requiring a `merge=ethos-seal`
  git driver, a `.gitattributes` entry, a byte-prefix invariant, and a
  resume-offset verification. Operator ruling (2026-07-20): "Merge
  conflicts is going to be a headache and waste of time, come up with a
  solution where this does not happen." Replaced by chunk sealing — each
  seal writes a new immutable file, so tracked files are never modified
  and merges are structurally conflict-free. The merge driver,
  `.gitattributes`, byte-prefix invariant, divergence detection, the
  `ethos audit reseal` verb, and the resume-offset machinery are all
  deleted.
- **Per-session `seq` field for line identity.** The earlier draft added a
  monotonic per-session `seq` allocated under the flock. Operator ruling
  (2026-07-20): "Would not put sequence numbers would put timestamp — more
  useless conflicts come from sequence numbers." Replaced by a
  strictly-monotonic per-session `ts` (bump by 1ns when `now <= last_ts`),
  which gives the same collision-free per-session total order from a field
  the line already carries. Line identity is `(session, ts)`; the read
  dedups on it to collapse cross-branch re-seal overlap (§Cross-branch
  re-seal), which is a read-time collapse on the ruled identity, not a
  `seq` field.
- **Suppressing re-seal on a rewound branch via a `local` high-water
  mark.** A persistent watermark in the `local` zone would stop a branch
  that lacks an earlier chunk from re-sealing those lines, avoiding
  overlap. Rejected: it trades overlap for **data loss** — if that branch
  is the one that merges and the other is abandoned, the re-sealed lines
  would be missing from the merged history and the audit record would not
  travel with the work that landed. Re-sealing is correct; the read
  tolerates the overlap by `(session, ts)` dedup.
- **Committing-session-only pre-commit seal.** The earlier draft sealed
  only the committing session, resolved from the pre-commit's process
  tree, so a stranger's commit would not absorb another session's lines,
  keeping each session's record in its own PR. Operator ruling
  (2026-07-20): "too complicated and fancy and basically does not matter.
  Your intent to keep PRs pure is a waste of time in a squash merge world.
  Useless complexity." Replaced by a repo-wide seal: the pre-commit drains
  every pending live line in the repo. This also deletes the
  session-attribution and orphan-detection (`kill(pid, 0)`) machinery —
  orphaned lines simply land at the next commit.
- **Committing-session-plus-orphan-recovery sweep.** A variant that sealed
  the committing session on pre-commit and swept orphans via a separate
  `ethos audit seal --all`. Superseded by the same ruling: the repo-wide
  pre-commit already seals orphaned sessions' lines at the next commit, so
  the separate sweep and its liveness probe are unnecessary.
- **Checkout-path roster scoping.** The earlier draft disambiguated
  sessions by the roster `repo` absolute-path field so two checkouts of
  one GitHub repo never sealed each other. Deleted: the live file now lives
  inside its own checkout under `.punt-labs/local/`, so the filesystem
  separates checkouts and there is nothing to disambiguate.
- **Seeding the live file from the committed file on upgrade.** The
  append-and-prefix design needed the live file seeded with a byte copy of
  the committed file so the prefix invariant held across the upgrade.
  Chunks are additive, so there is nothing to seed: the committed file is a
  frozen chunk and new lines seal into a new chunk. Deleted.
- **`size(sealed)` as a blind byte offset / verified prefix anchor.** Both
  the naive blind-offset resume and the R1 verified-prefix anchor existed
  only for append-to-one-file sealing. Immutable chunks have no prefix to
  anchor or verify. Deleted.
- **Seal at commit-msg instead of pre-commit.** commit-msg runs after the
  index snapshot; a chunk staged there misses the commit. The audit record
  would trail its work by one commit.
- **Seal at session end as primary.** Sessions outlive PRs and may never
  end cleanly; the record would land long after the work merged, or never.
  Kept only as an optional best-effort flush.
- **Raw (unredacted) live lines, redact at seal.** Turns the seal into a
  second redaction pass — a second failure site and a drift risk — and
  leaks absolute paths into the `local` zone at rest. Redact once, at
  write.
- **Keep the per-mission `.lock` in the repo and gitignore it.** A lock
  file in the tree at all is an invitation to re-track it. Move it to the
  global tree.
- **Byte-equality dedup of the legacy pool.** An earlier fix collapsed two
  byte-identical legacy lines, on the premise that identical bytes must be
  the same event. Rejected: no mechanism ever duplicates a legacy line — the
  frozen `audit.jsonl` is read once and the seal never copies a legacy line
  into a chunk — so the legacy pool holds no duplicate to collapse, and every
  collapse the dedup could perform is therefore a *drop* of a distinct event
  (the pre-discipline clock can hand two real tool calls the same `ts`, and
  with identical redacted input those two lines are byte-identical). The
  legacy pool passes through undeduped; only the post-discipline pools, which
  a cross-branch re-seal genuinely duplicates, dedup (on `(session, ts)`).
- **Home-tree spool for the gitlink teardown flush.** An earlier fix copied
  the unsealed tail of a soon-to-be-deleted gitlink-mounted checkout to a
  home-tree spool at `~/.punt-labs/ethos/spool/<repo-ident>/`, meaning to
  drain it once a vendored checkout sealed. Rejected as a **write-only
  subsystem**: nothing in the design drained it, read it, dated its drained
  chunk, derived its `<repo-ident>` key, or flagged an undrained file, and a
  failed spool copy at teardown — the one moment the copy is the sole
  surviving record — was unsignaled. It was a transitional gitlink-era
  mechanism that would outlive its `e29s` window as unbounded home-tree
  residue. The gitlink teardown now defers like every other seal, and losing
  a deleted gitlink checkout's unsealed tail is accepted as the bounded
  pre-`e29s` limitation (§Seal triggers).
- **Durable deferral record for the gitlink teardown notice.** To make the
  teardown deferral loud, an earlier fix proposed recording the unannounced
  deferral in a durable home-tree file that the next `ethos audit show` or
  `doctor` would report. Rejected as **spool-lite creep**: a home-tree record
  of soon-to-be-lost lines is the home-tree spool (above) under another name
  and inherits the same write-only-subsystem objection. The teardown deferral
  in a gitlink mount is accepted as unannounced and post-hoc undetectable —
  the bounded pre-`e29s` limitation (§Seal triggers).

## Proposed DESIGN.md entry

Insert after DES-057. Next free number is **DES-058**.

```markdown
## DES-058: Live audit write path and sealed committed record (amends DES-054) (DRAFT)

**Status**: Draft. Bead `ethos-t5b6`. Amends DES-054 v5 storage.
Cross-repo request from punt-kit (clean-tree gate on `punt release`
preflight failing against siblings with live sessions). Full design:
`docs/audit-seal.md`.

### Problem

DES-054 stores the session audit log at
`<repo>/.punt-labs/ethos/sessions/<YYYY-MM-DD>-<session-id>/audit.jsonl`,
git-tracked (shared history, path redaction, commit-trailer resolution)
AND appended by the PreToolUse hook on every tool call. A file cannot be
both git-tracked and continuously appended: any repo with an active
session has a permanently dirty tree, observed blocking `punt release`
preflight. `missions/<id>/log.jsonl` and the tracked `.create.lock` share
the disease.

### Design

Split the append-heavy files across two zones in the same checkout: the
gitignored `.punt-labs/local/` zone is the live write path; the tracked
`.punt-labs/ethos/` tree holds the sealed record as immutable chunk files.

- **Live location — `.punt-labs/local/` (machine-local zone).** The live
  file is `<repo>/.punt-labs/local/ethos/sessions/<session-id>.audit.jsonl`.
  `local` is the org convention for machine-local-never-committed state
  (`.envrc.local`, `vox.local.md`); its gitignore rule ships once, org-wide,
  via the punt-kit `punt-labs-dir.md` standard (merged `e3ab9a3`) and
  `punt:init`/`punt:audit` enforcement. Per-checkout isolation is automatic
  (the file lives inside its checkout), which removes the *seal-time*
  checkout-path scoping — the path itself is retained in the purge tombstone
  and vacuum cross-check (§Seal failure). Rejected: the home-dir global tree (operator
  ruling for `local`), `TMPDIR`/`.tmp/` (deletable scratch), and
  `.punt-labs/local/<branch>/` (sessions span branches).
- **Redacted live lines.** The write path is unchanged (build → redact →
  hash → preview → append); only the destination moves. The live file
  holds redacted lines so the seal is a transformation-free byte copy,
  redaction stays in one place, and nothing leaks at rest.
  `tool_input_hash` is still computed over the redacted form (DES-052
  unchanged).
- **Strictly-monotonic per-session timestamp.** No `seq` field. Every
  append allocates `ts = max(now, last_ts + 1ns)` under the session flock,
  giving a per-session total order that is collision-free regardless of a
  coarse or NTP-stepped clock. Line identity is `(session, ts)`. The
  writer initializes `last_ts` from the **seal watermark's own source set** —
  the max over existing sealed chunk timestamps, every covering `.quarantine`
  marker's verified `<last>`, and a frozen legacy file's max ts — so new lines
  sort strictly after frozen history and no ts it mints under a clock regression
  can sink into the gap a partial quarantine's marker opens above the max chunk
  ts (below the watermark, never sealed, never shown). On live-file reopen the
  writer **truncates a non-newline-terminated tail** under the flock before
  appending: that fragment is an un-synced partial write, unrecoverable
  regardless, and truncating it prevents a new complete line from being
  glued onto it into one unparseable line (the reader still skips a torn
  tail in a file not yet reopened). A newline-terminated line that still
  fails to parse (an out-of-order page writeback losing an earlier slice) is
  **skipped with a stderr count** by every consumer — writer recovery, seal,
  and read — never exit 2 and never silent, since its own `f.Sync()` never
  completed. The writer's count rides PreToolUse-hook stderr, which Claude
  Code does not surface on exit 0, so there it is best-effort; the
  load-bearing channel is the seal's and read's re-emission of the same count
  in visible contexts.
- **Chunk sealing.** A seal never modifies a tracked file: it writes a
  **new immutable chunk** `audit-<first>-<last>.jsonl` (Unix-nanosecond
  first/last timestamps, zero-padded to 19 digits — sorts chronologically,
  collision-free, filesystem-safe) holding the live lines with
  `ts > watermark`. The watermark is the max `<last>` across the session's
  existing chunk names (a frozen legacy `audit.jsonl` is scanned and
  contributes the max ts over its lines; a `.quarantine` marker contributes
  the **verified** `<last>` it records — the max ts the corrupt bytes reached
  and of any lines quarantine re-sealed, never the filename `<last>` on
  faith); the live writer seeds its monotonic floor from this same set
  (§strictly-monotonic per-session timestamp), so no mintable ts sits below it.
  The malformed-name exit 2 is **scoped to the chunk namespace, per
  directory shape**: a near-miss carrying a chunk prefix that fails its
  namespace's full parse — `audit-<19digits>-<19digits>.jsonl` in a session
  dir, `log-<session-id>-<19digits>-<19digits>.jsonl` in a mission dir — fails
  the seal (a skipped chunk would regress the watermark), while every
  non-chunk sibling (the frozen `audit.jsonl` or `log.jsonl`, a `.quarantine`
  marker or a `.corrupt`/`.corrupt-<hash>` artifact under a covering marker
  whose named range contains the artifact's, in either namespace, a mission's
  `contract.yaml`/`results.yaml`, any unrelated file) is ignored and draws no
  error. When it scans, the seal also **verifies each chunk's content** — a
  chunk that does not parse whole or whose last line `ts` != its filename
  `<last>` is corruption and fails the seal (exit 2), the same check the read
  makes; the specified escape is `ethos audit quarantine`, never
  `--no-verify`. The sealed directory is dated by the **session start date**
  (from the roster; when `session purge` has removed the entry, from an
  existing sealed dir's date prefix, a purge tombstone, or the live file's
  first-line ts — never wall-clock, so one session never splits across two
  dirs). Written via temp + rename for atomicity; temp names embed the chunk
  range, so a widened tail after a crash yields a new temp name rather than an
  overwrite — the seal deletes only **its own** stale `.audit-*.jsonl.tmp`
  under the flock before writing its own, and leaves any foreign `*.tmp`
  untouched (sibling rule). The seal's final act unconditionally `git add`s
  **every** untracked
  chunk in the session dir (not only the one it wrote), so a crash after
  rename but before staging cannot leave an orphan dirtying the tree. Two
  branches only ever add distinct chunk files, so merges are conflict-free
  with stock git — no merge driver, no `.gitattributes`, no prefix
  invariant, no divergence/reseal.
- **Seal triggers.** Primary at **pre-commit**: `ethos audit seal` visits
  **every** session directory in the repo — the union of the live zone
  (lines to seal) and the tracked sealed tree (chunks to stage) — seals each
  session's pending live lines and `git add`s **every** untracked chunk it
  finds, so the record lands in the same commit/PR as the work and a crashed
  seal's orphan chunk is recovered even when that session has no pending live
  lines. Repo-wide, not committing-session-scoped: in a squash-merge world
  per-commit attribution is a non-goal, so orphaned/crashed sessions' lines
  simply land at the next commit — no liveness probe, no session-attribution
  logic. Not commit-msg (runs after the index snapshot). Secondary at
  **mission close** for Tier B. Session-end sealing is a courtesy flush for a
  long-lived checkout, but the load-bearing mitigation for a worktree about
  to be deleted: the live file lives inside the checkout, so worktree removal
  or `git clean -fdx` destroys any unsealed lines, and the SessionEnd flush is
  what preserves them. In a **gitlink-mounted** repo (a consuming repo before
  bead `e29s`)
  the sealed tree is unreachable, so the teardown flush defers like every
  other seal there — one-line notice, nothing written — and deleting the
  checkout (hook-driven or a hookless `rm -rf`) destroys its unsealed tail.
  The design accepts this as a **bounded pre-`e29s` limitation**: there is no
  in-repo target to seal to, a home-tree transit copy was rejected as a
  write-only subsystem (§Rejected alternatives), and the org rule is to vendor
  a repo
  (`e29s`, the `punt-4yy` campaign) before relying on its audit trail.
- **Merged read.** `ethos audit show` unions the session's sealed chunks
  and the live lines with `ts` past the sealed watermark, orders by `ts`
  (a **stable** sort, so two legacy lines sharing a pre-discipline `ts` keep
  their file order and the "output identical" criterion holds),
  and dedups the two post-discipline pools while passing the legacy pool
  through. Post-discipline lines (post-upgrade chunks + live) dedup on
  `(session, ts)` — loss-free, since equal ts implies a byte-identical line
  from the same append-only live file and distinct events get distinct ts —
  collapsing the overlap a cross-branch re-seal leaves. Frozen legacy lines
  are **not deduped at all**: nothing ever duplicates a legacy line (the seal
  never copies one into a chunk), so the pool has no duplicate to collapse and
  any collapse could only drop a distinct event the pre-discipline clock gave
  a colliding ts; the two pools never mix because every legacy ts sits below
  every post-upgrade ts. A monotonic chunk that does not parse whole, or whose
  last ts != its filename `<last>`, is corruption and surfaces as an **error
  naming the chunk** (exit 2, escape `ethos audit quarantine`), never a silent
  drop; a quarantined chunk's unrecovered sub-range shows in the output as an
  **explicit gap marker** (the lines quarantine re-sealed from the live file
  reappear as an ordinary chunk),
  and only a legacy file, whose name has no ts to contradict, keeps the
  tolerant torn-tail drop. A newline-terminated but unparseable live line is
  skipped with a stderr count, never exit 2. In a gitlink-mounted repo (seal
  deferred, below) `audit show` **flags** a session whose live tail sits past
  the sealed watermark (`N unsealed lines, sealing deferred until vendored`).
  In the common single-branch case the dedup is a no-op and output is
  identical to the pre-change single-file read; the DES-054 early-return read
  path is **replaced** by this union.
- **Cross-branch re-seal.** The live file is in `.punt-labs/local/` and
  survives `git checkout`, but the watermark is derived from the tracked
  chunk set, which a branch switch rewrites. A branch lacking an earlier
  chunk therefore re-seals already-sealed lines into a wider chunk; after
  merge both chunks coexist and overlap. Re-sealing is **desired** —
  suppressing it would lose the record on whichever branch actually merges
  — so the overlap is resolved at read by the `(session, ts)` dedup, not
  prevented. The monotonic-ts subsystem is separately rewind-robust (the
  writer recovers `last_ts` from the live file's own tail), so timestamps
  never regress across a branch switch even though the seal watermark can.
- **Mission tree.** `log.jsonl` gets the same live-write/chunk-seal
  treatment, but the chunk name carries the sealing session id —
  `log-<session-id>-<first>-<last>.jsonl` — because every session seals into
  the one shared `missions/<id>/` directory (unlike an audit chunk's own
  per-session dated dir), so the per-session monotonic-ts guarantee does not
  span sessions: without the id segment two checkouts appending different
  mission events could mint identically named chunks with different content,
  an add/add conflict. The read unions **all** sessions' chunks in the
  directory (stable-sorted by `ts`, identity `(session, ts)`) and the seal
  watermark is **per-session** (max `<last>` over that session's own
  `<session-id>` chunks). Authoritative seal at mission close, pre-commit as
  the clean-tree backstop. All `.lock` files
  move to the global tree (`~/.punt-labs/ethos/missions/<id>.lock`,
  `.create.lock`) — a lock file never belongs in shared history; migration
  untracks the existing `.create.lock` **and removes it from disk** (a bare
  `git rm --cached` leaves an untracked file that re-fails the clean-tree
  gate), and the code stops writing any in-repo lock. Lock relocation is
  unchanged by the chunk rulings.
- **Seal failure — three classes.** The pre-commit hook exits 2 (fail
  closed, DES-055 shape) on an I/O error (`EACCES`/`EIO`), a malformed chunk
  filename, a **corrupt sealed chunk** (does not parse whole, or last ts !=
  filename `<last>`), or a **`git add` failure** staging a new or orphan
  chunk or a quarantine artifact — with a self-contained remedy message. The specified escape from a
  corrupt-chunk exit 2 is **`ethos audit quarantine <chunk>`**, never
  `--no-verify`. It **retires the corrupt chunk first** — renaming it out of
  the namespace to `.corrupt` (committed as evidence; seal and read ignore the
  name) frees the chunk's name so a re-sealed chunk that takes it cannot clobber
  a still-named corrupt chunk. It then re-seals any still-readable lines of the
  range from the live file into an **ordinary content-named chunk** (named
  `audit-<first>-<last>` from the re-sealed lines' own first/last `ts`, so a
  **partial** recovery yields `audit-<first>-<C>`, `C < last`, coinciding with
  the retired name only on **full** recovery — which keeps the content-vs-name
  check from ever firing on a re-sealed chunk; `(session, ts)` dedup tolerates
  the overlap), writes a tracked `.quarantine` marker with **deterministic content
  only** (chunk name, verified content-derived `<last>`, unrecovered sub-range,
  reason — **no wall-clock timestamp**, so two checkouts quarantining the same
  chunk from the same state produce byte-identical artifacts that merge clean),
  and **stages every quarantine artifact the marker covers** — the `.corrupt`,
  the marker, the re-sealed chunk, and any `.corrupt-<hash>` — itself
  (`git mv` + `git add`) so the tree is clean with no hand-staging. The marker contributes to the watermark its
  **verified** `<last>`, never the filename `<last>` on faith, which would
  silently suppress every later line's seal. `audit show` then shows only the
  unrecovered sub-range as an explicit gap. Because the marker is the last
  durable artifact and is itself written via temp + `f.Sync()` + rename (a torn
  marker reads as **absent** everywhere), a crash mid-verb resumes
  deterministically by artifact state: chunk present and no `.corrupt` → fresh
  run; a `.corrupt` with no covering marker → **resume** (finish the re-seal,
  marker, and stage — on resume a chunk already at the freed target that parses
  whole and matches the recovery range **is** the completed re-seal, verified
  and kept, since the refuse-if-target-exists guard is a fresh-run rule; a
  chunk there that **fails** verification is fresh damage, retired under the
  same `<name>.corrupt-<hash>` suffix before the resume proceeds), and
  seal and read treat this state as an error prompting the resume, never
  silence; a `.corrupt` with a covering marker → idempotent no-op that also
  verifies every covered quarantine artifact (including any `.corrupt-<hash>`)
  is staged, **requires a chunk to stand for the marker's recovered range**
  (re-running the re-seal if none does while the live file still holds lines of
  that range outside the recorded unrecovered sub-range — the
  crash-between-retirement-and-re-seal window), **and content-verifies any
  chunk now
  at a covered name** — fresh corruption there is a **new** event, retired under
  a deterministic `<name>.corrupt-<hash-of-corrupt-bytes>` suffix (the
  never-overwrite is per exact filename, so it never collides with the first
  `.corrupt`, and a byte-identical suffix already on disk is retired under the
  first free name in a deterministic sequence, `-2`, `-3`, …, rather than
  refusing — the sequence is the never-overwrite),
  re-sealed, with the marker updated by the smaller-range/union rule. It never
  overwrites an existing `.corrupt`; recovery is repo-wide
  only once the quarantine commit merges, and until it propagates other
  checkouts still exit 2 — bounded and loud. Two checkouts quarantining the
  same chunk from divergent states can produce an ordinary marker conflict,
  resolved by keeping the marker with the **smaller** unrecovered range plus
  the union of the re-sealed chunks (the checkout that recovered more wins).
  Fail-closed must never leave bypass as the only exit. A commit with no
  sessions dir (`ENOENT`) or no pending live lines is a no-op exit 0, but the
  vacuum cross-check is **per session** and iterates two sources: each
  roster-active session bound to this repo (any checkout path) **and** each
  purge tombstone whose recorded repo is the committing repo and that carries
  an unsealed-lines flag (so the crash → purge → checkout-deleted → commit
  sequence still warns). The tombstone records the repo and recorded checkout
  path it was purged from, so the check scopes tombstones to the committing
  repo and derives the live path from that path and the session id; the
  warning repeats at each commit until the operator acknowledges it with
  `ethos session purge --ack <session-id>`, which **renames** the tombstone to
  `<session-id>.purged.acked` — a retained record, never warned on again, so
  the loss survives the ack. Like the `.corrupt` rename this never overwrites
  per exact filename: a second ack for a re-purged id retires under a
  content-derived `<session-id>.purged.acked-<hash>` suffix — a stable,
  collision-averse name — and acking onto an existing identical `.acked-<hash>`
  retires under the first free name in a deterministic sequence (`-2`, `-3`, …)
  rather than refusing, so two byte-identical tombstones stay two records and
  the warning is always retirable. For
  each the hook `stat`s
  the recorded live file and **warns on stderr naming any session whose live
  file is absent** (still exit 0 — it may mean a checkout was deleted, or one
  session's live file removed, with unsealed lines). `session purge` itself
  refuses (or with `--force` warns and sets the tombstone flag) when the live
  file still holds lines above the watermark, and sets the flag in a distinct
  "live file already gone" variant when the recorded live file is already
  absent (so the crash → checkout-deleted → purge → commit order is caught
  too). A
  **gitlink-mounted** `.punt-labs/ethos/` (a consuming repo before bead
  `e29s`) is also a no-op exit 0, but **not silent**: it prints a one-line
  stderr notice and `audit show` flags the deferred session. The seal target
  is the wrong tree, so sealing defers — the live lines stay in
  `.punt-labs/local/`, the clean-tree goal is already met, and the record
  seals once `e29s` lands. The SessionEnd teardown flush in a gitlink mount
  defers the same way, but its notice rides exit-0 SessionEnd stderr (reaching
  no one) and the checkout — with the live tail — is deleted moments later, so
  the teardown deferral is unannounced and post-hoc undetectable: a bounded
  pre-`e29s` limitation (§Seal triggers), not a recoverable case.
  This deferral must not exit 2, or it would block every commit in every
  consuming repo; the notice plus the flag keep the window from being
  unbounded silence. A corrupt
  sealed chunk *is* the divergence an earlier draft claimed impossible:
  I11-chunk makes it unreachable normally, so if it appears the store is
  damaged and the seal fails loudly rather than sealing past it.

### Invariant changes

`I10-audit-atomic` amended: appends target the live session log under the
session flock, which also allocates the strictly-monotonic per-session
`ts` (`> max sealed chunk ts`); sealed chunks are written only, whole, by
the seal step. New `I11-chunk` (each chunk written once via temp+rename,
never modified; chunks hold disjoint contiguous ts ranges *within one
tree state* — a branch rewind can overlap, resolved at read), `I11-idem`
(each complete live line sealed into at least one chunk after a following
seal; duplicate copies share `(session, ts)` and are byte-identical),
`I12-merge` (read = union of sealed chunks and live tail past the sealed
watermark, ordered by ts (a **stable** sort, so legacy equal-ts lines keep
file order); post-discipline lines dedup on `(session, ts)`,
frozen legacy lines pass through undeduped since nothing duplicates a legacy
line and a legacy dedup could only drop a distinct event, and a corrupt
monotonic chunk surfaces as an error, not a drop).

### Migration

Existing tracked `audit.jsonl`/`log.jsonl` are frozen historical chunks
and stay — each reads as its session's oldest chunk. Only the write path
moves to `.punt-labs/local/`. New verb `ethos audit seal [--dry-run]`
(idempotent, atomic per session via temp+rename, partial-failure resume;
visits every session dir under both the live zone and the tracked sealed
tree so orphan chunks are staged even when a session has no pending live
lines; does not delete or truncate the live file, which is discardable
**only once its lines are sealed** — an unsealed live file is the sole copy
of those tool calls). Companion verb `ethos audit quarantine <chunk>` retires
a corrupt sealed chunk — re-seals still-readable lines from the live file,
renames the chunk to `.corrupt`, writes a tracked `.quarantine` marker
holding the watermark at the verified loss point, and self-stages both
(`git mv` + `git add`) — the sanctioned alternative to `--no-verify`.
No live-file seeding — chunks are additive, so a continuing session starts its
watermark at the max ts of its committed chunks and writes new lines strictly
after. Lock relocation untracks **and disk-removes** the tracked
`.create.lock`/per-mission `.lock` and stops writing in-repo locks.

### Transition

Two minor versions (DES-054 convention). vX.Y.0: live-write redirect to
`.punt-labs/local/` + strictly-monotonic ts + pre-commit chunk-seal hook
(via `install.sh`) + mission-close seal + `audit seal`. vX.(Y+1).0:
relocate `.lock`/`.create.lock` to the global tree, untrack and disk-remove
the repo copies. One ethos binary per machine, so the write path flips on
upgrade. vX.Y.0 reaches a consuming repo only **after** it carries the
canonical `.punt-labs/local/` gitignore block (`punt-4yy`) — the live-zone
sequencing gate mirroring the `e29s` gate for chunks; without it the live file
lands untracked-and-unignored and dirties the repo, the disease relocated.
Three window properties: (1) a session alive across the flip has
pre-upgrade lines in the frozen chunk and writes new lines to a fresh live
file — no seeding, the writer initializes its monotonic ts from the
chunks' max ts so new lines sort after; (2) the file dirty today is flushed by
a **one-time operator reconciliation commit** of its final state (no verb
rewrites a frozen file), then clean; (3) machines
sharing a repo should cross the boundary together — no data loss and no
conflict (new chunks, not appends), but the un-upgraded machine keeps
dirtying its tree and its appends land in the frozen `audit.jsonl` as legacy
lines, which the upgraded machine passes through **undeduped** (its
pre-upgrade clock can still collide two distinct events on one ts, and
nothing duplicates a legacy line, so a legacy dedup could only drop a real
line).

### Rejected alternatives

Home-dir global live tree (operator ruling for `.punt-labs/local/`);
gitignored in-repo sibling needing a per-repo rollout (the `local` ignore
ships once via the standard); `TMPDIR`/`.tmp/` (deletable scratch);
per-branch live path (sessions span branches); append-to-one-tracked-file
sealing with a `merge=ethos-seal` driver, `.gitattributes`, byte-prefix
invariant, divergence detection, and `audit reseal` (operator ruling —
replaced by immutable chunks, structurally conflict-free); per-session
`seq` field (operator ruling — replaced by strictly-monotonic ts);
committing-session-only and committing-session-plus-orphan-sweep seals
(operator ruling — repo-wide seal, orphans land at the next commit, no
liveness probe); checkout-path roster scoping (filesystem separates
per-checkout `local` files); live-file seeding on upgrade (chunks are
additive); suppressing re-seal on a rewound branch via a `local`
high-water mark (would lose the record on the branch that merges — the
read tolerates the overlap instead); `size(sealed)` blind offset and
verified prefix anchor (no prefix in an immutable-chunk model); seal at
commit-msg (too late for the index); seal at session end as primary
(sessions outlive PRs); raw live lines redacted at seal (second failure
site, leaks at rest); keep the lock in-repo and gitignore it (a tree lock
invites re-tracking); byte-equality dedup of the legacy pool (nothing
duplicates a legacy line, so every collapse could only drop a distinct
event — the legacy pool passes through undeduped); home-tree spool for the
gitlink teardown flush (a write-only subsystem nothing drained, read, dated,
keyed, or flagged, with an unsignaled copy failure — the gitlink teardown
now defers like every other seal and the deleted-checkout tail loss is
accepted as the bounded pre-`e29s` limitation); a durable home-tree deferral
record to make the teardown notice loud (spool-lite creep — the same
write-only subsystem under another name; the teardown deferral is accepted as
unannounced and post-hoc undetectable).
```
