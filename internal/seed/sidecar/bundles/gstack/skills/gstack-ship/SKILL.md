# GStack Ship

Ship-readiness checklist: merge the base branch, run tests, verify the
plan was actually implemented, and land cleanly. Distilled from
`garrytan/gstack`'s `ship` skill (origin: garrytan/gstack,
SPDX-License-Identifier: MIT).

## Before merging code, in order

1. **Distribution pipeline check.** If the diff introduces a new
   standalone artifact (CLI binary, library, tool), confirm a release
   workflow exists to build and publish it. Code without a distribution
   path is code nobody can use — flag it explicitly if deferred, never
   let it drop silently.
2. **Merge the base branch before running tests**, so tests run against
   the merged state, not a stale branch tip. Auto-resolve simple
   conflicts (version files, changelog ordering); stop and show anything
   ambiguous.
3. **Run the test suite** and fix failures before proceeding — a ship
   step never rides on a red suite.
4. **Audit test coverage of the diff** — new code paths need new tests,
   not just "existing tests still pass."
5. **Audit plan completion** — if a design doc or plan exists, confirm
   every planned piece actually landed, and call out any scope drift
   (things dropped, changed, or added) explicitly rather than silently.
6. **Pre-landing review** — a last read of the diff as a whole, not
   file-by-file, catching integration issues invisible at the file
   level.
7. **Version bump** — classify the change (patch/minor/major) by scale,
   not vibes: a big diff with new capability is not a patch just
   because no test failed.
8. **Commit in bisectable chunks** — one logical change per commit;
   split a rename from a rewrite from new tests.
9. **Verification gate** — before pushing, re-run checks against the
   final merged state, not an earlier one.

## Judgment calls, not just checkboxes

- Bisect commits: a reviewer (human or bot) should be able to revert one
  concern without reverting all of them.
- Never fold unrelated changes into an existing changelog entry that
  already shipped — each shippable unit gets its own entry.
- A shortcut that saves human-hours but only saves minutes with AI
  assistance should usually be replaced with the complete version before
  shipping, not shipped as a known gap.

## Output

Report status plainly: what shipped, what was verified, and anything
still open. Do not report "done" over an unverified claim — verify with
evidence (test run, diff read, checklist item) before checking it off.
