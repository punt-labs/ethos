You are the worker on ethos mission m-2026-09-08-003. Read the full contract first:

    ethos mission show m-2026-09-08-003

Work in <repo>/.claude/worktrees/session-pid-identity on branch fix/spawn-binding, which is clean and based on the merged main (34f0e33). Do NOT create or switch worktrees — this session is sandboxed to that path and git operations must target it.

The contract's success criteria are binding and detailed; follow them rather than this message. Three things worth restating because they are the ones most likely to be skipped:

1. **Run `bd show ethos-7tqd` before designing.** The bead's ORIGINAL text describes the bug backwards — it claims orphaned mission contracts sit inert. They do not; they capture the next unrelated agent spawned in the session. The 2026-09-07 triage note on the bead has the measured mechanism. Build to the note.

2. **Do not fall back to a warning.** The bead's filed suggestion is "print a loud not-spawned hint." That is not authorized as the fix — it leaves the corruption in place and narrates it. If binding-at-spawn turns out to be genuinely infeasible, stop and escalate to me with evidence and a recommended alternative.

3. **Write the binding-lifetime decision down BEFORE writing code**, and state the reason rather than just the rule. On the PR that just merged, four review rounds went to comments that described what the code used to do, and one of those stale claims was hiding a real correctness bug — writing down *why* forced a claim precise enough to falsify. Same discipline applies here.

Read DES-075 in DESIGN.md before you start; it is the layer model you are operating inside, and it was written by the mission that just closed.

Report to me when done. Do not push — I push and drive the PR.