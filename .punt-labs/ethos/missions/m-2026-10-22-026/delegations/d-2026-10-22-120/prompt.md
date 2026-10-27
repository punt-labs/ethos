You are the worker for ethos mission m-2026-10-22-026, round 6 of the DES-078 OpenCode plugin design (bead ethos-5y4r, PR #536). Read the contract with `ethos mission show m-2026-10-22-026`. Its success criteria are authoritative: each one is a leader decision, so apply it as written.

Repository <repo>, branch `design/opencode-plugin-adapter`, already checked out. Write set: `docs/design-opencode-plugin.md` and `DESIGN.md`.

Before you write, verify every cited file:line against source (internal/session/store.go, internal/hook/session_start.go, internal/hook/pretooluse_dispatch.go, internal/hook/commit_trailers.go, internal/mission/active.go, internal/mission/store.go, internal/enable/enable.go and deposit.go, cmd/ethos/main.go, cmd/ethos/iam.go, internal/process/tree.go, plugin/hooks/commit-msg.sh, docs/spec-opencode-integration.tex). Verify the opencode.json `instructions` key against the .tmp/ OpenCode references or context7. If a decision proves impossible as stated, stop and report it; do not pick an alternative.

Git rules:
- Run `make check` before each commit.
- Commit by explicit path only: `git commit -m "<msg>" -- docs/design-opencode-plugin.md DESIGN.md`.
- Never stage, unstage, or restore any other file.
- End each commit message with `Co-Authored-By: Claude Opus 5.5 <[redacted-email]>`.
- Commit on your branch; do not push, do not touch PRs or merge — the leader does that. Do not reply to or resolve threads.

When done, submit the round-1 result through ethos with a per-criterion table, commit SHAs, and `make check` outcomes. Your final report repeats the table and lists any open question.