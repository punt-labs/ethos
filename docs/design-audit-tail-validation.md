# Bounded session audit appends with full verification at seal

**Status**: PROPOSED as DES-086. Design only; implementation requires operator review.

Read first: DES-058 (live audit and sealing), DES-079 (trace and blame tiers),
DES-080 (redaction), DES-083 (binary-owned hooks), DES-084 (storage repair),
and the [storage contract](storage-layout.md#reader-outcomes).

## 1. Problem and current behavior

Jim's 40-hour sample recorded 18,131 audit-log hooks at 1.4 s median,
4.5 s p90 and 800 minutes total. Long sessions had 1.6–2.0 s medians;
short sessions had 64–130 ms medians. This is the cost of an observation
hook on every tool call, not just at a commit boundary.

On the repository PostToolUse path, `appendPolicyAudit` holds the
shared-store stream lock, then calls `ReadStream`. The reader unions local
archive, published copies, supported legacy data, quarantine floor evidence
and complete live lines. It collapses equivalent copies and rejects
divergent identities before watermark filtering. `AppendMonotonic` then
reads the live file again to repair a torn tail and recover the max live
timestamp. The new row is guarded, stamped with its publication eligibility,
appended, and fsynced; the hook confirms directory durability separately.
Failures are visible but do not block the tool invocation.

## 2. Measurements

Times are median / p95 in milliseconds, 21 timed calls per size and
binary. The real shell wrapper received a synthetic Bash PostToolUse
payload, with a 90.36 MB transcript file, in an isolated repository and
HOME. The harness restored the same live JSONL fixture before each timed
call. For the paired [PR #688](https://github.com/punt-labs/ethos/pull/688)
run, it alternated the binaries after warmup; concurrent race tests added
noise, especially to the tail. The post-merge column is a separate baseline
at `10a9db60aed259bbaabcfff1babf39a4d6aaafb9`, not a second A/B comparison.

| Live rows | #688 before | #688 after | Post-merge main |
| ---: | ---: | ---: | ---: |
| 0 | 154.77 / 271.28 | 162.25 / 237.31 | 16.97 / 19.05 |
| 1,000 (0.47 MB) | 169.64 / 726.45 | 146.96 / 399.43 | 43.20 / 45.15 |
| 18,000 (8.48 MB) | 1,335.95 / 1,936.30 | 532.23 / 1,216.99 | 430.58 / 524.91 |

The post-merge `BenchmarkPolicyAuditAppendLongSession` CPU profile assigned
77% of samples to `ReadStream` (65% to `CanonicalPayload`, included in that
77%) and 11% to `AppendMonotonic`, via its reopen scan. The syscall trace read
16,956,000 live bytes for the 8,478,000-byte fixture and never opened the
transcript. Removing the transcript file left the 18,000-row median at
440.50 ms. Transcript size is a signal of session length, not an input to
this measured bottleneck.

## 3. Alternatives ruled out by the probes

- **Metadata plus a tail digest as proof of the entire history.** On
  Linux/ext4, an existing shared writable mapping changed an early row of
  an archived chunk without changing device, inode, size, mtime, ctime or
  the final 4 KiB. The full digest changed. The real hook had appended while
  the archived and live copies matched, then reported `audit identity conflict`
  and refused to append after the edit. A metadata-and-tail cache would have
  accepted the old validation result. Cheap tail evidence can help locate
  a boundary, but cannot certify an unchanged history.
- **Hash the entire file before reusing validation.** A warm full-file
  SHA-256 probe on the same row shape took 0.90 / 1.04 ms at 1,000 rows,
  16.43 / 16.66 ms at 18,000 rows and 45.46 / 46.71 ms at 50,000 rows
  (median / p95). This could save parsing but is still linear in bytes and
  excludes archive checks, locking and fsync. A stored hash alone cannot
  detect a change to bytes it never rereads.

## 4. Proposal

### Routine append

Scope this to repository-owned session audit logs. Mission event writes and
global fallback paths are unchanged. Keep the existing shared stream lock
across worktrees. Use a versioned, rebuildable, per-stream record under
`S/.punt-labs/local/ethos/` with the last known timestamp floor, the live file
identity and a byte offset and bounded digest anchoring the last confirmed
complete boundary. The first use or rebuild
gets the floor from the full verified union, including legacy and quarantine
evidence, not from the most recent chunk filename or last line alone.

Under the lock, inspect a bounded window at the end of the live file. Check
the expected complete boundary and validate any subsequent complete rows
(JSON object, unique keys, valid timestamp, and strictly increasing timestamps
above the saved floor). A disagreement is a fallback, not a cue to skip
records. If a final fragment lacks its newline, truncate only that fragment
after locating a proven boundary; the append fsync also flushes that repair.
An oversized tail, missing boundary, unexpected replacement or shrinkage,
unusable floor record, or unexpected writer takes the existing verified path.
Do not impose a new record-size limit or run an unbounded scan in the fast
path itself.

Mint the timestamp as `max(now, verifiedFloor + 1ns)`, where the floor
includes validated complete rows in the tail. Check for overflow. Apply the
same guard and recording consent as today. Preserve write/sync rollback and
the distinction between a dropped append and one with unconfirmed directory
durability. Advance the local record atomically only after file and directory
durability are confirmed. A crash or checkpoint-write failure can leave an
older record, not an authority for dropping data: recover a valid forward
suffix within the window or fall back. A durable row is not reported as
dropped just because a derived checkpoint could not be refreshed.

Supported operations that can change the floor or live boundary (including
seal, quarantine/repair and migration) must invalidate affected records
under the same lock, before mutating inputs, so interrupted operations also
force a rebuild. A sibling worktree may share the changed input. Git can
also replace published inputs at the same checkout path; a path match alone
does not establish that the verified checkout context still applies.
If a supported input change cannot be tracked safely, keep the verified path
for that case. The fast path is flat in historical size when its prerequisites
are met, not a promise that cold starts, reconciliation, or all appends are
flat.

### Seal, reads and recovery

Keep full-history checks in full readers and make pre-commit/seal the mandatory
verification boundary before new audit archival or publication. Reuse the
seal's existing reconciliation, redaction and full union: verify complete
live, retained and published evidence, compare overlaps before filtering,
check quarantine floors and preserve legacy identity rules. A seal checkpoint
can reuse work only with the existing byte verification, not the append floor.
Recompute the floor from verified evidence before re-enabling the fast path
after repair. Do not seal or publish candidates whose evidence was not verified.

Retain the existing failure distinction: the default blame-mode pre-commit
path reports archival errors without blocking unrelated source commits;
manual seal returns an error, and required publication failures still block.
If a seal budget expires, report deferred work and keep unverified data local
for retry. Explicit read and existing repair paths still surface or reconcile
historical conflicts. Neither a successful append nor a successful unrelated
source commit is a receipt for audit validation.

## 5. Tamper-detection tradeoff

Today, any detectable malformed historical row or unequal copy can make the
next repository append fail. With the proposal, an edit outside the checked
tail can go unnoticed by subsequent appends until a full read, repair, or
seal; if none runs, the interval is unbounded. An uncoordinated edit can also
introduce a higher historical timestamp that a cached floor does not yet
reflect. Strict timestamp allocation applies to coordinated writers and the
verified floor, not to arbitrary edits made after that verification. The
full check must refuse or reconcile conflicting history rather than publish
it as if it had been validated at append time.

Neither design authenticates the author or proves a syntactically valid,
non-conflicting rewrite of the sole local copy did not occur. The proposal
does not change Git's existing blame tier, squash-safe provenance manifests
or verification of the actual merged commit. It does change when local trace
can be trusted to have passed conflict checks. In full mode, only eligible
rows that pass the existing verification and publication flow reach Git.

## 6. Cost and verification plan

Expect bounded tail I/O plus an atomic local state update on a warm append,
with existing lock and fsync costs still paid. Cold and stale states still
pay a full read. Seal cost remains history-sized; measure the net cost at
pre-commit and manual seal, including lock hold time and budget retries,
instead of assuming the work moved there for free.

For a later implementation, use the existing Go benchmark for the profile:

```sh
mkdir -p .tmp
go test ./internal/hook -run '^$' -bench '^BenchmarkPolicyAuditAppendLongSession$' \
  -benchtime=8x -cpuprofile .tmp/audit-tail.pprof -o .tmp/audit-tail.test
go tool pprof -top -cum .tmp/audit-tail.test .tmp/audit-tail.pprof
```

For the CLI comparison, seed strictly increasing, representative JSONL rows
in the local sessions directory of an isolated Git repo. Use the same Bash
tool payload, transcript and wrapper (`/bin/sh -c` with the absolute wrapper
path, from the fixture repo). Run at least 21 samples
per binary at 0, 1,000, 18,000 and 50,000 rows with alternating order. Measure
restored fixtures for cold/fallback runs, and separately prewarm each binary's
own stream and then append without rewriting it for warm runs. Report median
and p95, bytes read and lock duration, under single and concurrent writers.
Compare seal cost with equivalent archived and live histories.

Record failing-before and passing-after outputs. The performance regression
test must count history reads or bytes, not rely on a wall-clock cutoff.
Through the real hook and seal entry points, test the new deferred-conflict
boundary, timestamp floors (including backwards clocks, quarantine and
worktrees), crash windows, durable rollback, tail repair, stale state recovery,
and refusal to publish unverified or private history. Keep existing
deduplication, redaction and reader tests; add a case for an early archive edit
undetectable by the tail fingerprint but visible at seal.

## 7. Questions for Jim

1. Is the delayed detection window acceptable for local audit trace, including
   sessions that never reach a seal? Should an explicit integrity check or
   periodic verification be required, and at what cadence?
2. Which mixed-version and externally imported history cases must stay on the
   verified append path? Is the extra machine-local state update per tool call
   acceptable to avoid a full rescan after normal restarts?
3. What live-tail byte budget, p95 target, and additional pre-commit or seal
   cost would make this trade worth taking?
