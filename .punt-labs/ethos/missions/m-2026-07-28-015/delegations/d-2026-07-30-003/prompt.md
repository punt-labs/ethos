Review the diff of branch `fix/ethos-friction-defects` against `main` in the ethos repo at /Users/jfreeman/Coding/punt-labs/ethos. Get the diff with: `git -C /Users/jfreeman/Coding/punt-labs/ethos diff main origin/fix/ethos-friction-defects`.

Context: this branch fixes four mission/hook friction defects plus three follow-up review findings. Focus your review on these specific concerns:
1. internal/mission/reflection.go + store.go — a legacy reflections.yaml (no `mission:` field) must be back-filled from the containing mission ID on READ, but an explicit non-empty MISMATCHED `mission:` must still be rejected, and the WRITE/submission path must still require a valid mission field. Confirm no path silently accepts a bad mission value.
2. cmd/ethos/mission.go + internal/mission/active.go — `runMissionClose` must clear the active-mission sidecar on the terminal close transition so post-close missionless commits get no `Mission:` trailer. Check fail/other terminal transitions too.
3. hooks/commit-msg.sh — session-dir discovery must not word-split on spaces in $HOME; sidecar reads must be trimmed. Confirm shellcheck-clean logic.
4. internal/mission/inputs.go — the inputs.bead deprecation warning must fire only on genuine user submission of a legacy field, not on normal dispatch.

Report only high-confidence issues that truly matter (correctness, silent failures, invariant violations). Skip style nits. Return a concise list; empty if clean.