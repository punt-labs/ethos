Read your mission contract: `ethos mission show m-2026-08-14-001` in <repo>. You are on branch `work/ethos-livi-audit`.

Also read `bd show ethos-livi` for the operator's revised bead direction after tty17's finding (vendored copies are NOT authoritative today — ethos reads global; vendored trim doesn't reduce context; vendored set not proven complete). The audit must scope the resolution flip properly before design starts.

The 6 audit questions in the mission are the deliverable structure — answer each with file:line citations. Read `docs/design-issue-457-reports-to.md` from earlier this month for the shape of a good ethos design doc if you need a template, and use `internal/hook/generate_agents.go`'s `deriveAntiResponsibilities`/`deriveReportsToTargets` pattern as an example of how existing code already walks layered stores.

Read-only. Commit `docs/audit-ethos-livi-repo-primary-resolution.md`. Include the small `.claude/.settings.json.punt-import.lock` residual as a note but don't fix it. Report at the top: (a) does a repo-primary resolution ADR exist? (b) rough hours-of-work estimate; (c) any blocking prerequisites the operator needs to rule on before design.