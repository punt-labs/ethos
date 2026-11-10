# Phase 7: PR, Review Cycles, and Merge

This phase took **7 minutes 36 seconds** — from commit to merged PR.
Most of that was CI + Bugbot latency.

## Commit

```bash
git add internal/hook/subagent_start.go
git commit -m "fix(hook): consolidate verifier contract load to single ReadFile (ethos-db7)

checkVerifierHash now does one os.ReadFile + DecodeContractStrict per
contract instead of Store.Load + os.ReadFile. Same bytes used for both
hash verification and renderVerifierBlock, eliminating the TOCTOU
window between reads. Inline symlink rejection mirrors mission.
rejectSymlink (unexported, cross-package)."
```

Commit message format: `type(scope): description`

- `fix` = bug fix or hardening (not `feat` — no new functionality)
- `(hook)` = the package that changed
- First line under 72 chars
- Body explains **why**, not **what** (the diff shows what)

## Push and Create PR

```bash
git push -u origin fix/verifier-toctou

gh pr create \
  --title "fix(hook): consolidate verifier contract load (ethos-db7)" \
  --body "## Summary
- Single os.ReadFile + DecodeContractStrict replaces Store.Load + os.ReadFile
- Same bytes for hash verification and isolation block rendering
- Eliminates TOCTOU window between reads

Bead: ethos-db7 | Mission: m-2026-04-10-003"
```

Actual PR: punt-labs/ethos#212

## CI Results

```text
docs           pass     7s
test           pass     1m11s
Cursor Bugbot  skipping 5m23s
```

CI passed in ~1 minute. Bugbot took 5+ minutes (skipped — single Go
file, no security-relevant patterns flagged beyond the review comment).

## Review Comments

**1 finding from Bugbot (medium severity):**

> Duplicated `decodeAndValidate` logic risks future divergence.
> The four-step sequence (DecodeContractStrict -> CurrentRound
> default-fill -> Validate -> MissionID check) manually reimplements
> the unexported `decodeAndValidate` from internal/mission/store.go.

**Resolution**: Same class as the local reviewer's symlink finding —
cross-package duplication of unexported logic. Accepted as a tradeoff.
The comment added in Phase 6 documents the gap. Thread resolved.

## Merge

The historical PR used one review cycle and zero rework pushes. The finding
was an accepted tradeoff, not a code change. Its timing below is historical
evidence, **not** DES-079 publication or squash acceptance.

### DES-079 squash route

The required replacement route publishes complete selected blame records and
an immutable provenance manifest, explicitly carries its reference into the
remote squash message, pins the reviewed head, then verifies the actual squash.
These commands describe the pending contract, not an acceptance run. Local
commit hooks do **not** run on GitHub's squash server. See
[storage and publication contract](../storage-layout.md#publication-squash-and-exports).

After code commits and review fixes, select the real mission IDs and the
reviewed branch base (not an arbitrary recent parent). In checkout C:

```bash
# Pending contract. Substitute the actual PR, base and selected mission IDs.
set -eu
PR=212
BASE=origin/main
ethos mission publish m-2026-04-10-003 --base "$BASE"
# Inspect selected publication plus unrelated staged content before committing.
git diff --cached --stat
git commit -m "docs(provenance): publish selected mission records"
git push
```

Publication is not plain export: it includes all accepted results, decisions,
delegation records, corrections, selected summary rows and applicable ADRs.
Its inventory must be checked at the actual committing-index boundary, including
alternate-index and partial-commit forms. Do not bulk-add S's entire record tree
or force-add trace. Preserve unrelated staged work. Check the latest publication
commit through CI and independent review before continuing; any changed source
blobs require regeneration and renewed verification/review.

Resolve **S** with the established store-root resolver; the body below must be
under S's local zone (or approved development scratch), not in the record tree
or a disposable C-local archive. Do not derive S by assuming cwd is the main
checkout. Once the final publication head is reviewed:

```bash
# S is the resolved canonical store root; this is not a new override value.
: "${S:?set S to the resolved canonical store root}"
BODY="$S/.punt-labs/local/ethos/scratch/squash-body.txt"
mkdir -p "$(dirname "$BODY")"
HEAD_SHA=$(git rev-parse HEAD)
test "$(gh pr view "$PR" --json headRefOid --jq .headRefOid)" = "$HEAD_SHA"
ethos provenance message "$HEAD_SHA" > "$BODY"
gh pr merge "$PR" --squash --match-head-commit "$HEAD_SHA" --body-file "$BODY"

# Wait for confirmed merge if repository policy queues it; do not verify HEAD.
test "$(gh pr view "$PR" --json state --jq .state)" = MERGED
MERGE_SHA=$(gh pr view "$PR" --json mergeCommit --jq .mergeCommit.oid)
test -n "$MERGE_SHA" && test "$MERGE_SHA" != null
git fetch origin "$MERGE_SHA"
ethos provenance verify "$MERGE_SHA"
```

Any failure stops provenance success reporting. Unexpected squash blobs or a
missing/tampered reference are failed verification, not a hidden-warning pass.
Trailers record associations, not authenticated authorship; multi-mission or
multi-delegation ambiguity must stay visible rather than pick the first agent.
Delete the branch only after successful verification; final acceptance must
repeat verification and blame in a fresh isolated-home clone with branch objects
and the originating local archive unavailable. This example has not run that
acceptance or independently rechecked the installed `gh` flags.

## Timing Breakdown

| Step | Duration |
|------|----------|
| Commit + push | 30s |
| PR creation | 15s |
| CI (test + docs) | 1m18s |
| Bugbot | 5m23s |
| Read + resolve comments | 30s |
| Merge | 5s |
| **Total** | **~7m36s** |

The Bugbot wait (5m23s) was 71% of this phase. On a clean PR with no
findings to resolve, this phase would be ~2 minutes (CI only).
