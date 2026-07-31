You are the worker on mission m-2026-07-31-004 in the ethos repo (<repo>). Read it first: `ethos mission show m-2026-07-31-004` — the success criteria are the contract, follow them exactly.

This fixes FIVE mission/audit-lifecycle correctness defects — the product differentiator, so every fix must make a shipped feature behave as prfaq.tex intends and MUST NOT loosen a settled invariant (write-set overlap refusal, strict result decoder, contract immutability). BEFORE coding, re-read prfaq.tex (feat:results, feat:writeset, feat:frozen, human-agent parity) and DESIGN.md DES-032/DES-036/DES-054/DES-058.

The five bugs (details + tests in the contract):
1. ethos-qy7k — result-submit containment check not glob-aware (docs/** falsely rejects real paths). Make containment honor the write_set's own glob/segment-prefix semantics; still reject genuinely-outside paths.
2. ethos-7vo3 item A ONLY — delegation records mis-file under a stale per-session active-mission sidecar. Bind the delegation to the mission passed EXPLICITLY at dispatch, auto-release on close, warn on stale binding. Attribution only — touch no contract invariant.
3. ethos-pobi — commit-msg.sh fallback picks the mission by a GLOBAL session pick, not the committing session. Resolve the COMMITTING session (ETHOS_SESSION, else the PID walk resolve.SessionID uses) and read only its sidecar; if unresolved, add NO trailer.
4. ethos-t2lb — pipeline instantiate --var with spaces collapses into one malformed write_set string. Split into distinct entries consistent with dispatch --write-set.
5. ethos-u4kq — identity FindBy/Resolve returns an arbitrary winner on ambiguous email. Fail loud with an "ambiguous identity: N matches" error naming candidates; unambiguous case unchanged.

WORKFLOW:
- Work on a feature branch off main in the MAIN checkout (no worktree — single agent, avoids collisions).
- Commit ONE logical step per bug (five commits), each with its tests, each passing `make check` (go vet, staticcheck/golangci-lint, go test -race -count=1, validate-content; run `make tools` first if golangci-lint is missing). No more than 30 min uncommitted. No suppressions — fix the code or escalate via `ethos mission result` (verdict: escalate), never chat-only.
- Stay strictly within the write_set (internal/mission, internal/hook, internal/identity, internal/resolve, cmd/ethos, hooks). Do NOT edit CHANGELOG.md, DESIGN.md, or docs/ — those are leader-authored; give me the proposed CHANGELOG Fixed text (citing all five bead IDs) and the docs/audited-delegation.md change 7vo3 implies, in your report.
- Each new test must FAIL against the current code (pin the bug), then pass.
- When green, open ONE PR to punt-labs/ethos base main, request Copilot review ONCE via the MCP tool (never a /copilot comment). Report the PR number + the proposed doc/CHANGELOG text to me. Do NOT merge. Submit a result artifact (verdict, confidence, files_changed within the write_set, >=1 evidence) so the mission can close.

GH_TOKEN is valid as claude-puntlabs — plain gh/MCP, no env -u. Report when the PR is open.