# Audit: hardcoded team/identity strings in internal/hook

Triggered by github issue #457 (bead `ethos-5r7v`). Read-only audit —
no source files changed.

**Total findings: 3.** All three fit under `ethos-5r7v`'s umbrella;
no finding warrants a separate bead. One item of surprise scope beyond
issue #457 is noted at the bottom — it is a different shape of thing
and is not filed as a finding.

## Scope

Read in full: `internal/hook/generate_agents.go`, `persona.go`,
`subagent_start.go`, `session_start.go`, `format_output.go`,
`pre_compact.go` (reads `persona.go`'s builders and has its own team-
section wrapper), `agent_installer.go`, and every corresponding
`_test.go`. Grepped the whole package for `Claude Agento`, `COO`,
`VP Engineering`, `You are`, `You report`, and `Punt Labs` to catch
anything the file list missed.

## Findings

### 1. `generate_agents.go:524` — the reported bug

```go
fmt.Fprintf(&b, "You report to Claude Agento (COO/VP Engineering).\n")
```

Unconditional literal, written into every generated
`.claude/agents/<handle>.md` regardless of the team's actual
`reports_to` graph. A team with a different apex, or an agent that
reports to someone other than `claude`, gets told the wrong boss.

**Symmetric fix using existing pattern at `deriveAntiResponsibilities`
(generate_agents.go:307).** That function already walks
`t.Collaborations` filtering `c.Type == "reports_to"` for edges
*into* a role (to build "What You Don't Do"). The fix walks the same
collaboration list filtering on `c.From == m.Role` — the role's
*outgoing* `reports_to` edge(s) — resolves the target role's occupant
identity, and renders `"You report to %s (%s)."` from that identity's
`Name`/`Handle`, the same way `resolveParentLine`
(subagent_start.go:864) already does for the SubagentStart persona
block. Multiple targets join with the existing `joinWithOxford`
helper (generate_agents.go:352), already used one section down for
anti-responsibilities. Zero outgoing edges (a top-of-hierarchy role)
drops the line entirely rather than emitting a false claim.

Confirmed live: `.claude/agents/bwk.md:45` in this repo carries the
exact bad line today, written by this code path.

### 2. `generate_agents_test.go:253` — test pins the bug as expected

```go
assert.Contains(t, content, "You report to Claude Agento (COO/VP Engineering).")
```

Inside `TestGenerateAgentFiles` (case "basic generation"). This
assertion is why `make check` stays green while the bug ships — the
test locks in the wrong value instead of deriving it from the
fixture's own team data.

**Symmetric fix:** once finding 1 is fixed, this assertion must
compute the expected line from the same test-fixture team the file
already builds a few lines below for the anti-responsibilities cases
(`TestGenerateAgentFiles_AntiResponsibilities`, generate_agents_test.go:863+),
not hand-pin a new literal string.

### 3. `generate_agents_test.go:849` — same pin, second call site

```go
"You report to Claude Agento (COO/VP Engineering).\n\n"+toolScopeNote+"\n## Core Principles\n",
```

Inside `TestGenerateAgentFiles_ToolScopeNote`, which asserts the
tool-scope note's exact position in the rendered file by concatenating
it with the same hardcoded reporting line. Same defect shape as
finding 2 — the position assertion is legitimate, but it is glued to
a literal that must go away with finding 1's fix.

**Symmetric fix:** same as finding 2 — build the expected prefix from
resolved fixture data, keep the position assertion (`toolScopeNote`
directly after the reporting line, directly before the personality
body) intact.

## What did NOT get flagged, and why

- `persona.go` (`BuildPersonaBlock`, `BuildTeamContext`,
  `writeMemberBlock`) — every `Name`, `Handle`, `Role`,
  `Responsibilities`, and `Collaborations` line is read from the
  loaded `identity.Identity` / `role.Role` / `team.Team` structs. No
  literal identity or role name anywhere in the package.
- `subagent_start.go:864` (`resolveParentLine`) — this is the second
  correct pattern in the package: it resolves the parent from the
  session roster and loads that handle's identity before rendering
  `"You report to %s (%s)."`. It is the template finding 1's fix
  should follow for the *rendering* half (the walk itself follows
  `deriveAntiResponsibilities`).
- `subagent_start.go:319` (`renderVerifierBlock`) — `"You are the
  frozen verifier %q for mission %s."` — both `%q`/`%s` values come
  from the mission contract (`m.Evaluator.Handle`, `m.MissionID`), not
  literals.
- `format_output.go` — pure JSON-to-text formatting. Table headers
  (`HANDLE`, `STATUS`, etc.) and section labels are structural markup,
  explicitly out of scope per the contract.
- `pre_compact.go` — routes everything through `BuildPersonaBlock` /
  `BuildTeamSection`; no literals of its own.
- `agent_installer.go` — copies files by handle, does not render
  identity content.
- Every other test file's use of `"Claude Agento"` (`persona_test.go`,
  `session_start_test.go`, `subagent_start_test.go`,
  `pre_compact_test.go`, `session_start_repo_only_test.go`,
  `generate_agents_test.go:98`) — these are fixture **input** data
  (`identity.Name` on a constructed test `Identity`) that the real
  formatter then echoes back and the test asserts. That is a
  legitimate test asserting correct behavior, not a pinned-wrong-value
  bug: the fixture author chose that name on purpose and the code
  under test is doing exactly what it should with it.

## Surprise scope beyond issue #457 (not filed as a finding)

`cmd/ethos/setup.go` and `cmd/ethos/team_bundle.go` hardcode the
handle `"claude"` as the reserved COO seat during the setup wizard's
CEO/COO leadership assignment (`setup.go:148,349-354,458-467,510-531`;
`team_bundle.go:282-291`). This is out of `internal/hook`'s call
graph — the CLI's `setup` command does not call into any of the
audited files — so it is outside this audit's write-set and scope.
It is also a different shape of thing than findings 1-3: it is the
seed bundle's documented, intentional default org shape (see this
repo's `CLAUDE.md`, "Claude Agento" section) enforced by explicit
validation, not an unconditional literal silently overriding real
team data on every render. Noted for the operator in case a separate
look is wanted; not proposed as part of `ethos-5r7v`.
