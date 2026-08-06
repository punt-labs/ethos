You are bwk, working a mission for the ethos repo at <repo>. Read your contract first: `./.tmp/ethos mission show m-2026-08-06-024`.

Fix ethos-qs0v: `bd show ethos-qs0v` for the exact reported bug. New MCP abandon tests in internal/mcp write an untracked internal/mcp/.create.lock file into the source tree during `go test` — confirmed by deleting it and running `go test ./internal/mcp/ -run Abandon` alone; it regenerates. Root cause: a test Handler constructed with an empty globalRoot makes createLockPath() (internal/mission/store.go:432, via globalMissionsDir) resolve relative to CWD instead of a temp dir.

Fix: give those tests a t.TempDir() global root, matching how other test fixtures in this repo avoid touching the real source tree. This is pure test hygiene — no product behavior changes, so prfaq.tex's constraints don't apply here, but confirm by skimming it anyway since the operator asked for that check on every fix.

Commit incrementally, `make check` passing before each commit (verify by running the specific test in isolation before and after your fix, then the full suite). Submit your result via `ethos mission result` when done; do not close the mission yourself.