Continue ethos mission m-2026-07-25-023 (the prior worker hit a usage limit mid-task; now lifted). Working dir <repo>. Run `ethos mission show m-2026-07-25-023` then `ethos mission claim m-2026-07-25-023`.

STATE: you are on branch fix/fresh-install-identity-ux. The prior worker already made UNCOMMITTED edits to internal/doctor/doctor.go and internal/doctor/doctor_test.go (the CheckHumanIdentity change: fresh install with ZERO identities → WARN + 'run ethos setup', real mismatch → still FAIL). Review those edits, keep/finish them, then complete the rest.

The write-set for m-023 is: internal/doctor, install.sh, cmd/ethos/handlers_test.go.

REMAINING WORK:
1. Finish/verify internal/doctor: fresh (zero identities) → WARN pointing to `ethos setup` (NOT a red FAIL, NOT circular 'fix then run doctor'); identities-exist-but-none-match → still loud FAIL; doctor's overall exit code still non-zero for genuine problems.
2. install.sh (around :316 verify, :359-360): on a fresh install where the only issue is no-identity-yet, do NOT print 'installed but doctor found issues / Fix the issues above, then run ethos doctor' — route to `ethos setup` instead. Genuine failures still surface loudly.
3. cmd/ethos/handlers_test.go: TestRunDoctor_Failure and TestRunDoctor_FailureJSON build an empty store and assert FAIL — empty is now WARN, so update them to assert FAIL via a NON-matching identity (real misconfig). Fixtures only, no production edits in cmd/ethos.
4. make check green; staticcheck + go vet clean; no suppressions.
5. DOGFOOD: the container harness at .tmp/clean-machine (Dockerfile + guide.sh) currently curls the RELEASED v4.5.0 install.sh. Edit .tmp/clean-machine to COPY the LOCAL (repo) install.sh into the image and run THAT (so your changed install.sh is exercised), then: `docker build -t ethos-clean-cli .tmp/clean-machine && docker run --rm ethos-clean-cli`. Confirm a fresh --no-plugin install as the unprivileged tester user completes with a `run ethos setup` nudge and NO red identity FAIL / NO 'doctor found issues' alarm. Paste the container output. (.tmp is scratch — edits there won't be committed.)

Commit incrementally on fix/fresh-install-identity-ux, one logical step per commit, each passing make check. Do NOT edit DESIGN.md/CHANGELOG (COO). Reply "written — <sha>" + the container dogfood output + one line on the doctor state model (e.g. added WARN).