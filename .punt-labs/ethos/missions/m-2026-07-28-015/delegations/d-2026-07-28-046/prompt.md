Review the diff of branch fix/redaction-committed-pii vs main in /Users/jfreeman/Coding/punt-labs/ethos (PR #411, beads ethos-ersr + ethos-n4np + ethos-ggtu). Get it: `git diff main..origin/fix/redaction-committed-pii`. It closes a committed-PII leak: sensitive content reached git-tracked shared history because the write-path redaction was incomplete. Read docs/audited-delegation.md (§Path redaction) + DESIGN.md DES-054/DES-058 for the design intent.

Three fixes:
1. Delegation records (internal/mission): prompt.md/record.yaml were written with absolute $HOME/<repoRoot> paths. Now a shared mission.PathRedactor runs INSIDE WriteDelegationSkeleton (and CloseDelegation's reason) before write; internal/hook's redactAbsolutePaths delegates to it (one implementation, two callers, since hook can't be imported by mission).
2. Audit content (internal/hook/audit_content.go): a keep-list policy — send_email tool_input keeps only `subject`, everything else -> [redacted]; matched on the bare tool name (mcp__ns__ prefix stripped); prompt-bearing fields get an email-address sweep. Both passes run BEFORE tool_input_hash is computed.
3. Fail-closed: if $HOME can't be resolved, the audit path drops the payload (keeps only timestamp/tool/linkage) and the delegation writer refuses outright rather than writing raw.

Focus on correctness:
- Does the PathRedactor actually catch every path form (both $HOME and <repoRoot>, and does it handle the case where HOME is a prefix of repoRoot or vice versa)? Any delegation write path that still bypasses it?
- keep-list: is it truly allow-list (unknown/new fields default to redacted, not kept)? Does the bare-tool-name match correctly strip mcp__ns__ so every namespace's send_email is covered? Could a prompt-bearing field with an email slip the sweep?
- ordering: is redaction provably BEFORE the hash for BOTH the audit-content policy AND the path redaction — never a hash over the raw form?
- Does the "byte-identical for non-sensitive tools" claim hold (a Read/Edit/Bash line with nothing to redact is unchanged, no spurious redacted:true marker)?
Report only high-confidence issues with file:line. If clean, say so plainly.