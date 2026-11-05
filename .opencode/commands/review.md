---
description: Run this repo's local Qodo and three-perspective review without publishing
agent: build
model: bifrost/openai/gpt-6-astra#high
---

# Local review

Review $ARGUMENTS (default full intended diff). Load ethos-qodo-review to probe
the runtime and route to the installed skill or authorized 1.6.0 compatibility
path; never claim a blocked 2.x skill passed. Dispatch code-reviewer,
silent-failure-hunter and invariant-completeness-reviewer into separate fresh
contexts with exact task ID, round, scope and evidence. Correlate exact returned
child IDs and completed results; resolve findings, don't suppress them. No push,
PR or release is implied. Preserve the repo's existing PR review policy.
