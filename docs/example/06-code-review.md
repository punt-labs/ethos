# Phase 6: Code Review

Local code review before pushing to a PR. This phase took **1 minute
25 seconds** — the reviewer found 1 actionable finding.

## Local Code Reviewer

The `feature-dev:code-reviewer` agent reviewed `git diff HEAD`:

```text
Agent(
  subagent_type: "feature-dev:code-reviewer",
  prompt: "Review the diff on fix/verifier-toctou. This is ethos-db7 —
    consolidating two file reads into one to eliminate a TOCTOU in
    checkVerifierHash. Run git diff HEAD. Focus on: TOCTOU elimination,
    symlink ordering, DecodeContractStrict behavior, error handling."
)
```

### Actual Review Output (summarized)

```text
## Critical Issues
None.

## Important Issues

### 1. Inline Lstat duplicates mission.rejectSymlink (Confidence: 85)

checkVerifierHash re-implements symlink rejection inline instead of
calling mission.rejectSymlink. The inline version has the same
Lstat-before-Read TOCTOU gap. The duplication means improvements to
rejectSymlink won't propagate here.

Fix: Export mission.RejectSymlink and call it, or add a comment
citing the gap.

## Edge Cases Confirmed Correct

- TOCTOU elimination: Yes. One ReadFile, same bytes for decode and RawYAML.
- rejectSymlink ordering: Yes. Lstat before ReadFile.
- KnownFields: Yes. DecodeContractStrict matches Store.Load.
- Error handling on ErrNotExist: Consistent with Store.Load.
- Legacy missions: Correctly skip hash recompute.
```

### Resolution

The finding is valid — the duplication is a maintenance hazard. But
`rejectSymlink` is unexported in the `mission` package and
`checkVerifierHash` is in the `hook` package. Exporting it is out of
scope for this bead.

**Fix applied**: Added a comment on the inline Lstat block citing the
gap and referencing ethos-jjm:

```go
// Inline symlink rejection — mirrors mission.rejectSymlink
// (unexported, different package). Same Lstat-before-Read
// TOCTOU gap as rejectSymlink; see ethos-jjm for context.
```

Second reviewer pass: clean.

## Review Layer Summary

| Layer | Finding | Action |
|-------|---------|--------|
| Local code-reviewer | Duplicated symlink logic | Comment added |
| Copilot | (after PR push) None | — |
| Bugbot | Duplicated decodeAndValidate logic | Resolved as accepted tradeoff |

Each layer caught what the previous missed. The local reviewer found
the symlink duplication. Bugbot found the decode duplication — same
class of issue, different function. Neither was a bug; both were
architectural observations about cross-package code sharing.
