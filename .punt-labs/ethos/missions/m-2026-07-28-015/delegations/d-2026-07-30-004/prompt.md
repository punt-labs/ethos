Hunt for silent failures and inadequate error handling in the diff of branch `fix/ethos-friction-defects` against `main` in the ethos repo at /Users/jfreeman/Coding/punt-labs/ethos. Get the diff with: `git -C /Users/jfreeman/Coding/punt-labs/ethos diff main origin/fix/ethos-friction-defects`.

Pay special attention to:
1. internal/mission/reflection.go — the new back-fill of a blank `mission` field from the containing mission ID on read. Does any error path swallow a genuinely invalid mission (non-empty mismatch) instead of returning an error? Is a decode/validation error ever silently defaulted?
2. cmd/ethos/mission.go — `ClearActiveMission` call on close: is its error checked, or silently ignored? If clearing fails, does the user get told?
3. hooks/commit-msg.sh — do the trimmed sidecar reads or the newline-IFS find loop swallow failures (e.g. missing file, empty result) in a way that silently skips trailer logic without signal?

Report concrete silent-failure findings with file:line. Empty list if clean.