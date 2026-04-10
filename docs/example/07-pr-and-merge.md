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

```bash
# Resolve the Bugbot thread
gh api graphql -f query='mutation {
  resolveReviewThread(input: {threadId: "PRRT_kwDORp6BQc56KHpF"}) {
    thread { isResolved }
  }
}'

# Merge
gh pr merge 212 --squash --delete-branch
```

**1 review cycle.** Zero rework pushes. The finding was an accepted
tradeoff, not a code change.

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
