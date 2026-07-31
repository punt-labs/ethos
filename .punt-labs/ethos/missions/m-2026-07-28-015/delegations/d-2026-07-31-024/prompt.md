Hunt for silent failures and inadequate error handling in the diff of branch fix/mission-lifecycle-cluster vs main in the ethos repo at <repo>. Diff: `git -C <repo> diff main...fix/mission-lifecycle-cluster`.

Five fixes; focus on the error/fallback paths specifically:
1. hooks/commit-msg.sh (pobi): the fallback now resolves the committing session (ETHOS_SESSION / PID walk). Does any resolution failure silently drop the trailer WITHOUT signal, or worse, fall back to a wrong session? Commit 249d0c0 claims to "surface a failed trailer lookup instead of swallowing it" — verify it actually does, and didn't introduce a new swallow elsewhere.
2. internal/identity + internal/resolve (u4kq): the ambiguous-email path now returns an error. Is every caller of FindBy/Resolve updated to handle the new error, or does one ignore it and proceed with a nil/zero identity (a new silent-wrong path)? Does the error path ever get swallowed and defaulted?
3. internal/mission conflict.go (qy7k): the glob-aware containment — does a match error (bad glob pattern) get treated as "contained" (accept) rather than surfaced? An error reading as accept would silently admit an out-of-write-set path.
4. internal/mission (7vo3): the delegation-binding change — does the stale-binding warning actually emit, or is it a no-op? Does any read error in resolving the dispatch mission get swallowed?

Report concrete silent-failure findings with file:line. Empty list if clean.