You are the pinned evaluator on ethos mission m-2026-07-22-002 (implement archetype). Working directory: <repo>, branch feat/enable-disable, tip ad09c85. You evaluated the design (m-2026-07-22-001, docs/enable-disable.md at cf75786 — read your own eval at .tmp/missions/results/m-2026-07-22-001-eval-rop.md); now evaluate the implementation against it.

Scope: 7 commits, main...feat/enable-disable minus the three docs(design) commits — internal/claudemd (7486148), internal/githook (3a861f0), hooks marker gate (1523c87), internal/enable (78b0025), cmd+doctor (ef57ea9), install.sh+docs (ad09c85). Worker's result YAML: .tmp/missions/results/m-2026-07-22-002.yaml.

Verify EMPIRICALLY, not by reading alone — build .tmp/ethos-eval from this branch and run it:
1. §2.4 writer conformance: run the claudemd tests; then hand-probe one case the tables might miss — a CRLF host whose existing import line was hand-edited with trailing spaces, and a host whose final line is inside an unterminated fenced block.
2. Chain/Unchain parity: run the githook tests; verify Unchain(Chain(x)) == x byte-for-byte on a real foreign hook; verify the resolver agrees with doctor on a real worktree AND a core.hooksPath repo (make throwaway repos).
3. The marker gate: with marker absent, a chained hook must do NO ethos work and must preserve a failing host's exit status (make the host fail; assert commit blocked). With marker present, seal fires.
4. Enable/disable round-trip + the four operator rulings that reached code: strand-on-disable (unsealed lines survive in the local zone and seal after re-enable + commit), worktree refuse+--force naming siblings, gitlink error naming the remedy, setup hint on missing config.
5. Zone safety: enable on a repo with populated identities/teams/sessions leaves them byte-identical; a manifest collision with a config path errors and deposits nothing.
6. Doctor four states: PASS not-enabled, PASS enabled-active, FAIL enabled-missing, WARN gated-but-unenabled; WARN must not fail the doctor run overall.
7. install.sh: shell chaining functions gone; work-tree run delegates to `ethos enable`; shellcheck still green; the deleted-lines diff contains no behavior that failed to reappear in Go (walk the deletion against the githook port).
8. The worker's flagged deviation (hooks/embed.go instead of embedding from internal/githook): sound or a layering problem?
9. make check with -race from a clean checkout of the branch.

Also read the worker's contamination note (its test once ran enable against THIS repo; it reverted CLAUDE.md and moved artifacts to .tmp/contamination/) — verify the revert left no residue (git diff main...HEAD -- CLAUDE.md must be empty; no .punt-labs/ethos/enabled or .vendored-manifest in the tree) and the fixed fixture actually prevents recurrence (find the test, check it uses a temp dir OUTSIDE the repo).

Write to .tmp/missions/results/m-2026-07-22-002-eval-rop.md: numbered findings (REQ/REC/NIT) with file:line and repro, verdict APPROVE or REVISE. Reply "written — <verdict>". Your final text is data for the leader.