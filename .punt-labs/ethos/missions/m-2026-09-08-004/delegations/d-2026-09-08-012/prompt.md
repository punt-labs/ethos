Review ONLY the delta `git diff f9bbf00..HEAD` on branch `fix/spawn-binding` in <repo>/.claude/worktrees/session-pid-identity, plus the current state of DES-076 in DESIGN.md. Ignore `.punt-labs/ethos/{sessions,missions}/**` JSONL.

**Scope discipline.** The rest of the branch has had two full review rounds; those findings are fixed. These 12 commits are the fixes for the latest round and have had zero review. Do not re-review earlier commits except as context.

You previously found that DES-076's residual-risk section was incomplete — five unnamed residuals — and that finding drove much of this delta. Your job now is to check whether the ADR is finally honest, and whether the new claims hold.

Specific claims to verify:

1. **`052d18b` claims an unresolvable pending entry no longer blocks: it is skipped but never cleared, so a newer valid entry behind it is reachable, and a lone unresolvable entry falls through rather than denying.** Verify all three parts against the code. Then enumerate: is "skip but don't clear" actually complete? What happens when *every* entry is unresolvable? When an entry oscillates between resolvable and not (a branch switching back and forth)? Does the skipped entry ever get cleaned up, or does it accumulate?

2. **`6c601df` claims putting the sidecar clear in `deleteFiles` "closes it for every deletion path at once."** Verify `Delete`, `Purge`, and `PurgeTombstoned` all genuinely funnel through it, and that no other deletion or expiry path bypasses it. The prior version of this claim — that session end cleared "the same scope `ethos mission release` clears" — was false, so check this one by construction rather than by comment.

3. **`af9b941`'s drift guard** claims to enforce exactly three `DelegationVerdictAborted` write sites. A guard that cannot fail is worse than none. Verify it detects a fourth site, and identify what forms of addition it would MISS (indirect call, alias, constructed string, a wrapper function).

4. **The five residuals you previously identified as unnamed** — legacy dispatch-origin pair, pending dispatch surviving abnormal session death, unenforced aborted-writer count, mtime tie ordering, hand-edited `verdict: aborted`. Confirm each is now either fixed or honestly named in the ADR. Flag any that are claimed fixed but are not.

5. **The residual-risk section as it now stands.** Are there residuals introduced BY this delta that it does not name? The three-way classification and the shared-primitive sidecar clear are both new mechanisms with new failure modes.

6. **Test quality on the new tests**, particularly `TestDispatchAgent_ActiveMissionSidecarLegacyDispatchOriginNotCaptured` and the claimed "true end-to-end" depth-refusal test. You have now caught the same defective shape three times — a test that guards its fixture rather than its stated property, checking only fields that survive the transition it claims to detect. Check for it again.

Report each claim CONFIRMED or NOT CONFIRMED with file:line evidence, and name the condition under which any conditionally-true claim fails.