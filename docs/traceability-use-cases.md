# Traceability Use Cases

How the artifacts described in
[traceability-data-assets.md](traceability-data-assets.md) support
real-world forensic, compliance, and operational scenarios.

Each use case names the question an operator needs to answer, the
artifacts that answer it, the query path (what you'd actually run),
and any gaps where the current system falls short.

---

## 1. Why does this code look the way it does?

**Scenario:** A new engineer reads a function and wants to understand
the design intent — not just what it does but why this approach was
chosen over alternatives.

**Artifacts used:**
- Git blame → commit SHA → git trailer (`Mission:`, `Delegation:`)
- Delegation prompt (`prompt.md`) — the instructions that drove the implementation
- Mission contract (`contract.yaml`) — the success criteria the code had to satisfy
- Mission context field — the leader's reasoning, often quoting operator directives
- Quarry transcript — the conversation where the approach was discussed

**Query path:**
```bash
git blame -L 21,21 internal/hook/generate_agents.go
# → commit 2b851cc, trailer Delegation: d-2026-05-25-011

cat .punt-labs/ethos/missions/m-2026-05-25-002/delegations/d-2026-05-25-011/prompt.md
# → "Fix the hardcoded Go file-extension patterns..."

cat .punt-labs/ethos/missions/m-2026-05-25-002/contract.yaml
# → context: "Bug report from CEO: in a Python project..."

quarry find "why detect project type at generation time"
# → conversation section where the leader discussed the approach
```

Or visually: `ethos ui` → Browse → click the file → click the
agent link on the line → see prompt + audit trail.

**Gaps:** Commits predating the trailer fix require the bead-ID
fallback (parses commit subject). The model's internal
chain-of-thought is not captured — only the prompt, the
conversation transcript, and the resulting actions.

---

## 2. How did a bug get into production?

**Scenario:** A production incident is traced to a specific function.
The team needs to know: who changed it, when, under what contract,
what testing was done, and whether the change was reviewed.

**Artifacts used:**
- Git blame → commit → trailer → mission + delegation
- Delegation audit trail — every tool call the agent made, including test runs
- Mission results (`results.yaml`) — the worker's structured verdict + evidence checks
- Mission event log (`log.jsonl`) — whether a review round occurred
- Contract evaluator field — who was supposed to review, and their frozen hash

**Query path:**
```bash
# 1. Find the commit that introduced the bug
git blame -L 42,42 internal/mission/store.go
# → commit abc1234, trailer Mission: m-2026-05-22-031

# 2. Did the worker run tests?
ethos audit show --delegation d-2026-05-22-031 --format text | grep "go test"
# → shows every test invocation with timestamps

# 3. Did the tests pass?
cat .punt-labs/ethos/missions/m-2026-05-22-031/results.yaml
# → verdict: pass, evidence: [{name: "go test", status: pass}]

# 4. Was there a review round?
cat .punt-labs/ethos/missions/m-2026-05-22-031/log.jsonl
# → create, result, close (no "reflect" or "round_advanced" = no review round)

# 5. Who was the evaluator?
grep evaluator .punt-labs/ethos/missions/m-2026-05-22-031/contract.yaml
# → evaluator: {handle: djb, hash: 19095b...}
```

**What this tells the investigator:** The agent ran tests and they
passed, but there was no review round (the mission closed after
round 1 with no reflection). The evaluator was frozen at creation
but may not have actually reviewed the output. The audit trail
shows exactly which files the agent read before making the change —
did it read the file where the bug's root cause lived, or did it
miss it?

**Gaps:** The audit log captures tool inputs but not tool outputs.
You can see the agent ran `go test` but not the test output. If the
test passed but was inadequate (didn't cover the bug scenario), the
audit trail shows the test was run but not what it tested. The
conversation transcript (quarry) may contain the test output if it
was printed during the session.

---

## 3. Did the agent stay within its authorized scope?

**Scenario:** A compliance officer wants to verify that an agent
only modified files it was authorized to touch and didn't access
sensitive areas outside its contract.

**Artifacts used:**
- Mission contract `write_set` — the authorized file paths
- Delegation audit trail — every Read, Edit, Write, Bash call
- Audit entry `tool_input.file_path` — the actual paths touched

**Query path:**
```bash
# 1. What was authorized?
grep write_set .punt-labs/ethos/missions/m-2026-05-25-002/contract.yaml
# → write_set: [internal/hook/generate_agents.go, internal/hook/generate_agents_test.go]

# 2. What was actually touched?
ethos audit show --delegation d-2026-05-25-011 --format json \
  | jq -r '.tool_input.file_path // empty' \
  | sort -u
# → list of every file path the agent interacted with

# 3. Did any Write/Edit fall outside the write_set?
ethos audit show --delegation d-2026-05-25-011 --format json \
  | jq -r 'select(.tool == "Edit" or .tool == "Write") | .tool_input.file_path' \
  | sort -u
# → compare against write_set
```

**What this tells the compliance officer:** The audit trail is
complete — every file the agent read or wrote is logged with
timestamps. The PreToolUse hook enforces the write_set at runtime
(blocks unauthorized writes), so a write outside the contract would
appear as a block decision, not a successful write. The audit log
also shows Bash commands, which could be used to write files
indirectly — a thorough audit should scan those for redirect
operators (`>`, `>>`, `tee`).

**Gaps:** Bash commands can modify files without going through the
Write/Edit tool path. The PreToolUse hook blocks unauthorized
Write/Edit calls but does not police Bash file writes. A
compliance check against Bash commands in the audit trail is
possible (the full command string is logged) but requires parsing
shell syntax.

---

## 4. Is the team following required processes?

**Scenario:** An engineering manager wants to verify that delegated
work goes through the mission system, has success criteria, uses a
frozen evaluator, and doesn't skip review rounds.

**Artifacts used:**
- Missions index (`missions.jsonl`) — all closed missions with metadata
- Mission contracts — success criteria, evaluator, budget
- Mission event logs — whether reflection/advancement events occurred
- Audit log — presence of Tier A (ungoverned) vs Tier B (governed) dispatches

**Query path:**
```bash
# 1. How many missions used the full review cycle?
ethos find missions --format json \
  | jq -r 'select(.rounds_used > 1) | .id' \
  | wc -l
# → count of missions with multi-round reviews

# 2. How many Agent dispatches were Tier A (ungoverned)?
grep '"tool":"Agent"' .punt-labs/ethos/sessions/*/audit.jsonl \
  | grep -vc '"contract_id"'
# → count of Agent calls without a governing contract

# 3. Do all missions have success criteria?
ethos find missions --format json \
  | jq -r 'select(.success_criteria == null or (.success_criteria | length) == 0) | .id'
# → missions without criteria (should be empty)

# 4. Were evaluators actually different from workers?
ethos find missions --format json \
  | jq -r 'select(.worker == .evaluator) | .id'
# → missions where worker == evaluator (violation)
```

**What this tells the manager:** The mission system enforces
structural constraints at creation time (evaluator != worker,
success criteria required, budget set). But it does not enforce
that a review ROUND actually happens — a leader can close a
mission after round 1 without a reflection event. The event log
reveals whether the review cycle was followed or skipped.

**Gaps:** The system records whether a reflection was submitted
but not whether the evaluator actually reviewed the output. A
reflection with `converging: true, recommendation: continue` could
be rubber-stamped. Detecting rubber-stamp reviews would require
analyzing the reflection content or the evaluator's session
transcript.

---

## 5. What did a specific agent do this week?

**Scenario:** A team lead wants to see all work performed by `bwk`
across all missions in the past week — what was delegated, what was
delivered, how many tool calls, which files were touched.

**Artifacts used:**
- Missions index — filter by worker + date range
- Delegation records — agent_type, verdict, timestamps
- Audit log — tool call counts, file paths

**Query path:**
```bash
# 1. Which missions had bwk as worker this week?
ethos find missions --worker bwk --since 2026-05-19 --format table

# 2. For each mission, what delegation?
for mid in $(ethos find missions --worker bwk --since 2026-05-19 --format json | jq -r '.id'); do
  echo "=== $mid ==="
  ls .punt-labs/ethos/missions/$mid/delegations/ 2>/dev/null
done

# 3. Total tool calls across all delegations
for mid in $(ethos find missions --worker bwk --since 2026-05-19 --format json | jq -r '.id'); do
  for did in $(ls .punt-labs/ethos/missions/$mid/delegations/ 2>/dev/null); do
    ethos audit show --delegation $did --format json | wc -l
  done
done | paste -sd+ - | bc
# → total tool calls

# 4. Which files did bwk touch?
grep '"agent_type":"bwk"' .punt-labs/ethos/sessions/*/audit.jsonl \
  | jq -r 'select(.tool == "Edit" or .tool == "Write") | .tool_input.file_path // empty' \
  | sort -u
```

Or visually: `ethos ui` → Dashboard → filter by worker (future
feature) → click each mission → see delegations + audit trails.

**Gaps:** `ethos find missions` currently filters by worker but
doesn't aggregate across missions. A dedicated `ethos find agent
bwk --since 2026-05-19` command would streamline this. The UI
dashboard doesn't have a per-agent view yet.

---

## 6. Verifying compliance tooling was used

**Scenario:** A security team requires that every code change goes
through `staticcheck`, `go vet`, and `make check` before commit. An
auditor needs to verify that an agent actually ran these tools during
a specific mission.

**Artifacts used:**
- Delegation audit trail — Bash commands with full command strings
- Tool input preview — searchable for tool names

**Query path:**
```bash
# 1. Did the agent run staticcheck?
ethos audit show --delegation d-2026-05-25-011 --format json \
  | jq -r 'select(.tool == "Bash") | .tool_input.command' \
  | grep -c staticcheck
# → count of staticcheck invocations

# 2. Did the agent run go vet?
ethos audit show --delegation d-2026-05-25-011 --format json \
  | jq -r 'select(.tool == "Bash") | .tool_input.command' \
  | grep -c "go vet"

# 3. Did the agent run make check (the full gate)?
ethos audit show --delegation d-2026-05-25-011 --format json \
  | jq -r 'select(.tool == "Bash") | .tool_input.command' \
  | grep -c "make check"

# 4. Did the agent run tests?
ethos audit show --delegation d-2026-05-25-011 --format json \
  | jq -r 'select(.tool == "Bash") | .tool_input.command' \
  | grep -c "go test"
```

**What this tells the auditor:** The audit trail contains every
Bash command the agent ran, with full command strings. An auditor
can verify not just that `make check` was invoked, but that it was
invoked AFTER the last Edit (not before the changes were made). The
timestamps on audit entries make the ordering provable.

**Gaps:** The audit log captures the command string but not the
exit code or output. You can see the agent ran `make check` but
not whether it passed. The result artifact's evidence checks
(`[{name: "make-check", status: pass}]`) provide the worker's
CLAIM that it passed, but that's self-reported, not
independently verified. A future enhancement could capture tool
exit codes in the audit entry.

---

## 7. Reconstructing a delegation chain (nested spawns)

**Scenario:** A leader dispatched an agent who itself spawned a
sub-agent (e.g., a worker dispatching an evaluator). The team
needs to trace the full delegation tree.

**Artifacts used:**
- Delegation records — `parent_delegation` field links child to parent
- Audit log — the Agent tool call from the parent shows the child dispatch
- Session ID — shared across the chain

**Query path:**
```bash
# 1. Find the root delegation
cat .punt-labs/ethos/missions/m-XXX/delegations/d-001/record.yaml
# → parent_delegation: "" (root)

# 2. Find child delegations
for did in $(ls .punt-labs/ethos/missions/m-XXX/delegations/); do
  parent=$(grep parent_delegation .punt-labs/ethos/missions/m-XXX/delegations/$did/record.yaml | awk '{print $2}')
  echo "$did parent=$parent"
done

# 3. Trace the Agent dispatch in the parent's audit trail
ethos audit show --delegation d-001 --format json \
  | jq -r 'select(.tool == "Agent") | "\(.ts) → spawned \(.tool_input.subagent_type // "unknown")"'
```

**Gaps:** The `parent_delegation` field on the child record is
populated only when `PARENT_DELEGATION_ID` env is set in the
spawning context. For Tier B dispatches via the sidecar mechanism,
the parent delegation is the sidecar's value — which is the MOST
RECENT dispatch, not necessarily the logical parent in a concurrent
scenario. True delegation trees (leader → worker → sub-worker)
require sequential dispatches to maintain accurate parent links.

---

## 8. Auditing PII exposure in agent activity

**Scenario:** A privacy officer needs to verify that agent activity
logs don't contain personally identifiable information (usernames,
home directory paths, machine-specific layout).

**Artifacts used:**
- Session audit log — every tool input with path redaction applied
- Redaction policy (documented in `audit_entry.go`) — `$HOME` → `~`, `repoRoot` → `<repo>`

**Query path:**
```bash
# 1. Check for any remaining absolute paths (post-redaction)
grep -c '/Users/' .punt-labs/ethos/sessions/*/audit.jsonl
# → should be 0 for entries written after the redaction fix

# 2. Check for home directory leaks
grep -c '/home/' .punt-labs/ethos/sessions/*/audit.jsonl

# 3. Verify redaction is working
tail -1 .punt-labs/ethos/sessions/*/audit.jsonl \
  | jq '.tool_input'
# → paths should show ~/... or <repo>/...
```

**What this tells the privacy officer:** Post-redaction entries
contain `~` and `<repo>` tokens instead of absolute paths. The
`tool_input_hash` is computed over the redacted form, so the
hash is machine-independent and doesn't encode PII.

**Gaps:** Entries written before the redaction fix (pre-v3.13.0)
contain raw absolute paths including the username. These are in
git history if committed. The redaction applies to `tool_input`
and `tool_input_preview` but not to the conversation transcript
(quarry captures), which may contain absolute paths in code
output or error messages.

---

## 9. Understanding the cost of a feature

**Scenario:** A product manager wants to know how much agent effort
went into a feature — how many missions, how many rounds, how many
tool calls, and how long it took from first dispatch to final close.

**Artifacts used:**
- Missions index — filter by ticket/bead ID
- Mission contracts — created_at, closed_at, rounds budgeted/used
- Audit log — tool call counts per delegation

**Query path:**
```bash
# 1. All missions for a feature (by bead ID)
ethos find missions --format json \
  | jq -r 'select(.ticket == "ethos-exok") | "\(.id) \(.status) rounds=\(.rounds_used)/\(.rounds_budgeted) \(.created_at) → \(.closed_at)"'

# 2. Total tool calls for each mission's delegations
for mid in $(ethos find missions --format json | jq -r 'select(.ticket == "ethos-exok") | .id'); do
  for did in $(ls .punt-labs/ethos/missions/$mid/delegations/ 2>/dev/null); do
    count=$(ethos audit show --delegation $did --format json 2>/dev/null | wc -l | tr -d ' ')
    echo "$mid/$did: $count calls"
  done
done

# 3. Wall-clock duration
ethos find missions --format json \
  | jq -r 'select(.ticket == "ethos-exok") | "start=\(.created_at) end=\(.closed_at)"'
```

**Gaps:** Tool call count is a rough proxy for effort — a 100-call
mission that ran `make check` ten times is different from a
100-call mission that made 80 targeted edits. Token usage (the
actual cost) is not captured in the audit log. Adding a
`tokens_used` field to the result artifact would close this gap.

---

## 10. Proving a negative — this file was NOT touched

**Scenario:** After an incident, the team needs to prove that a
specific sensitive file was NOT read or modified by any agent during
a time window.

**Artifacts used:**
- Session audit log — complete record of all tool calls with file paths

**Query path:**
```bash
# 1. Was secrets.yaml ever accessed?
grep 'secrets.yaml' .punt-labs/ethos/sessions/*/audit.jsonl
# → empty = never touched

# 2. More thorough: any tool call referencing the sensitive path
grep -r 'credentials\|\.env\|secrets' .punt-labs/ethos/sessions/*/audit.jsonl \
  | jq -r '"\(.ts) \(.tool) \(.agent_type // "leader")"' 2>/dev/null

# 3. Verify no Bash command could have accessed it indirectly
grep '"tool":"Bash"' .punt-labs/ethos/sessions/*/audit.jsonl \
  | jq -r '.tool_input.command' \
  | grep -i 'secret\|credential\|\.env'
```

**What this tells the investigator:** The audit log captures every
Read, Edit, Write, and Bash command with full inputs. If a file
path does not appear in any audit entry, no agent accessed it
through the standard tool surface. Bash commands are the escape
hatch — an agent could `cat secrets.yaml` via Bash without a
Read tool call, but the Bash command string is still logged.

**Gaps:** If the agent accessed a file by including its path in a
Bash command that was redacted (e.g., `$HOME/secrets.yaml` becomes
`~/secrets.yaml`), the search needs to account for the redacted
form. Also, if PostToolUse didn't fire (hook misconfiguration, or
a tool call that bypassed hooks), the access would be unlogged.

---

## Summary Matrix

| Use Case | Key Artifacts | Fully Supported? |
|----------|--------------|-----------------|
| Why does this code look this way? | blame → trailer → contract → prompt → quarry | Yes (with trailer fallback for old commits) |
| How did a bug get in? | blame → audit trail → results → event log | Mostly — tool OUTPUT not captured |
| Did the agent stay in scope? | contract write_set vs audit file paths | Yes for Read/Edit/Write; Bash is a gap |
| Are processes being followed? | missions.jsonl → event logs → contracts | Yes structurally; rubber-stamp detection is manual |
| What did an agent do this week? | missions by worker + audit aggregation | Yes but needs a dedicated query command |
| Was compliance tooling used? | audit trail Bash commands | Commands logged; exit codes not captured |
| Nested delegation chain? | parent_delegation field + audit Agent calls | Yes for sequential; concurrent dispatches are ambiguous |
| PII in agent logs? | audit entries post-redaction | Post-fix: yes. Pre-fix: historical leak in git |
| Feature cost? | missions by ticket + audit call counts | Tool calls logged; token cost not captured |
| Prove file was NOT touched? | audit log negative search | Yes — Bash commands included in search surface |
