# Fixing the hardcoded "You report to Claude Agento" line

**Status**: Design. GitHub issue #457, bead `ethos-5r7v`. Mission
`m-2026-08-13-002`. Design only — no implementation in this document or
this mission.

## Problem

`buildAgentFile` in `internal/hook/generate_agents.go:524` writes a
literal into every generated `.claude/agents/<handle>.md`:

```go
fmt.Fprintf(&b, "You report to Claude Agento (COO/VP Engineering).\n")
```

Every agent on every team is told it reports to Claude Agento, regardless
of the team's actual `reports_to` graph. Two tests pin the literal as
expected output (`generate_agents_test.go:253`, `:849`), so `make check`
stays green while the bug ships. Full detail: `docs/audit-hardcoded-team-strings.md`.

## Correction to the mission contract's description of the reference pattern

The mission contract describes the fix as walking `c.From == m.Role`
**instead of** `c.To == m.Role`, as if `deriveAntiResponsibilities`
filters on `c.To`. It doesn't. Read directly:

```go
// generate_agents.go:307
func deriveAntiResponsibilities(roleName string, collabs []team.Collaboration, roles *role.LayeredStore) []antiResponsibility {
    var out []antiResponsibility
    for _, c := range collabs {
        if c.From != roleName {   // <-- filters on c.From, not c.To
            continue
        }
```

`deriveAntiResponsibilities(roleName, ...)` already walks `roleName`'s
**outgoing** `reports_to` edges (`c.From == roleName`), takes each
edge's target role (`c.To`), and pulls that target role's
`Responsibilities` — because "the role you report to" is exactly what
defines your anti-responsibilities. That is the identical walk the new
reporting line needs; the only thing that changes is what gets resolved
from `c.To` (an occupying **identity**, not a role's
**Responsibilities** list).

So the new helper does not walk collaborations any differently than
`deriveAntiResponsibilities` — it reuses the exact same filter
(`c.From == roleName && c.Type == "reports_to"`) and only replaces the
resolution step. This is stated explicitly because the contract's
phrasing, if followed literally, would produce a helper that walks
`c.To == roleName` — the wrong direction, and not what any sibling
pattern in this file does.

## Recommendation

### Production: one helper, one render function, one call site

**`deriveReportsToTargets`** — new function, same file, placed next to
`deriveAntiResponsibilities` (they are literally the same walk with a
different resolution step):

```go
// reportsToTarget is one resolved occupant of a role that roleName's
// outgoing reports_to edge points to.
type reportsToTarget struct {
    Name   string
    Handle string
}

// deriveReportsToTargets walks the team's collaboration edges starting
// from roleName and returns, in walk order, the occupying identity of
// each outgoing reports_to edge's target role. Same walk as
// deriveAntiResponsibilities (c.From == roleName, c.Type ==
// "reports_to", same warn-and-skip on any other edge type) — only the
// resolution step differs: this resolves the target role's occupying
// IDENTITY through members, not the target role's Responsibilities
// through roles.
//
// A target role with no occupying member — prevented on every Load by
// team.ValidateStructural, but not provable from this function's own
// inputs — or an occupant whose identity fails to load, is warned to
// stderr and skipped, mirroring deriveAntiResponsibilities's
// target.Load handling at :330. The caller must not assert a
// reporting line it cannot back up. Returns nil if roleName has no
// outgoing reports_to edges or every target fails to resolve.
func deriveReportsToTargets(roleName string, members []team.Member, collabs []team.Collaboration, identities identity.IdentityStore) []reportsToTarget {
    var out []reportsToTarget
    for _, c := range collabs {
        if c.From != roleName {
            continue
        }
        if c.Type != "reports_to" {
            fmt.Fprintf(os.Stderr,
                "ethos: generate-agents: reports-to: unsupported edge from %q to %q with type %q (expected \"reports_to\") — skipping\n",
                c.From, c.To, c.Type)
            continue
        }
        var handle string
        for _, m := range members {
            if m.Role == c.To {
                handle = m.Identity
                break
            }
        }
        if handle == "" {
            fmt.Fprintf(os.Stderr,
                "ethos: generate-agents: reports-to: target role %q has no occupying team member — skipping\n", c.To)
            continue
        }
        occ, err := identities.Load(handle, identity.Reference(true))
        if err != nil {
            fmt.Fprintf(os.Stderr,
                "ethos: generate-agents: reports-to: target role %q: loading identity %q: %v\n", c.To, handle, err)
            continue
        }
        out = append(out, reportsToTarget{Name: occ.Name, Handle: occ.Handle})
    }
    return out
}
```

`identity.Reference(true)` matches `resolveParentLine`
(`subagent_start.go:859`) and `isAgentKind` (`generate_agents.go:253`):
this call needs only `Name`/`Handle`, and reference mode answers even
when the full attribute load (personality, writing style, ext) would
fail — a reports-to line should not depend on the target's personality
content resolving.

**`renderReportsToLine`** — small standalone function, mirroring
`resolveParentLine`'s own shape (a dedicated function that returns a
string, not logic inlined into its caller):

```go
// renderReportsToLine renders the resolved reports-to targets as one
// sentence, in the same shape resolveParentLine (subagent_start.go:864)
// uses for a single target: "You report to Name (handle)." Multiple
// targets join with joinWithOxford. Zero targets returns "" — the
// caller omits the line rather than assert a claim it cannot back up
// (audit finding 1).
func renderReportsToLine(targets []reportsToTarget) string {
    if len(targets) == 0 {
        return ""
    }
    names := make([]string, len(targets))
    for i, t := range targets {
        names[i] = fmt.Sprintf("%s (%s)", t.Name, t.Handle)
    }
    return fmt.Sprintf("You report to %s.", joinWithOxford(names))
}
```

**Call site**, `GenerateAgentFilesTo` (`generate_agents.go:158`, next to
the existing `deriveAntiResponsibilities` call):

```go
antiResps := deriveAntiResponsibilities(m.Role, t.Collaborations, roles)
reportsTo := deriveReportsToTargets(m.Role, t.Members, t.Collaborations, identities)
filePatterns := projectFilePatterns(destRoot)
content := buildAgentFile(id, r, antiResps, reportsTo, filePatterns)
```

`t` (the loaded `*team.Team`) and `identities` are already in scope at
this call site — no new parameters flow into `GenerateAgentFilesTo`
itself.

**`buildAgentFile`** (`generate_agents.go:406`) gains a parameter and
loses the hardcoded line:

```go
func buildAgentFile(id *identity.Identity, r *role.Role, antiResps []antiResponsibility, reportsTo []reportsToTarget, filePatterns string) string {
    ...
    fmt.Fprintf(&b, "\nYou are %s (%s), %s.\n", id.Name, id.Handle, firstLine)
    if line := renderReportsToLine(reportsTo); line != "" {
        b.WriteString(line)
        b.WriteString("\n")
    }

    // Tool scope. ...
    b.WriteString("\n")
    b.WriteString(toolScopeNote)
```

Byte-shape requirement when `reportsTo` is empty: the opening line is
followed directly by the single blank line that already precedes
`toolScopeNote`, exactly as when a reports-to line is present, minus
the line itself. No double blank line, no placeholder. This must hold:

```text
You are Brian K (bwk), Go specialist sub-agent.
<blank>
Only the tools listed in the `tools:` field above are available to you.
...
```

### Edge case (a): zero outgoing `reports_to` edges

**Recommendation: drop the line entirely.**

Sibling justification: `deriveAntiResponsibilities` already answers
this exact question for the same input shape (zero outgoing edges from
`roleName`) — it returns `nil`, and `buildAgentFile` (`:566`) skips the
entire `## What You Don't Do` section, no placeholder, no marker. Test
`TestGenerateAgentFiles_AntiResponsibilities/no_reports_to_edges`
(`generate_agents_test.go:926-935`) pins this: `NotContains(t, content,
"## What You Don't Do")`. The reports-to line is the same data source
(the same `roleName`'s outgoing edges), so it takes the same treatment.

Rejected: emit a distinguishing sentence ("You are at the top of the
reporting hierarchy."). Rejected because zero outgoing edges is not
evidence of being "at the top" — it is evidence that this role has no
`reports_to` edge in `t.Collaborations`, which could equally mean the
team's collaboration graph was never populated for this role. Inventing
a sentence that asserts hierarchy position from an absence is exactly
the "false claim from data that doesn't support it" failure the audit's
finding 1 describes, just relocated from a hardcoded string to a
hardcoded inference. Dropping the line asserts nothing beyond what the
data supports.

Rejected: leave an explicit marker (e.g. an HTML comment or a
"(no reports_to configured)" note). Rejected because no sibling section
in this file does this — `## What You Don't Do`, `## Safety
Constraints`, and `## Output Format` all omit silently when their
source data is empty (`generate_agents.go:566`, `:585`, `:614`). A
marker here would be the only such annotation in the file and needs its
own justification this design has no basis for.

### Edge case (b): multiple outgoing edges

**Recommendation: `joinWithOxford` over `"Name (handle)"` strings, one
per resolved target, in walk order.**

`"You report to Claude Agento (claude) and Jane Doe (jane)."` for two;
Oxford comma for three or more, via the existing
`joinWithOxford(generate_agents.go:352)` — no new join logic.

This differs in one respect from the anti-responsibilities preamble
(`"You report to %s. These are not yours:\n\n", joinWithOxford(targets)`,
`generate_agents.go:576`), which joins **role names** ("coo",
"architect"). The atomic unit there is a role, because the section
buckets bullets by role. Here the atomic unit is a person: the
reports-to line names who you report to, not which role. Joining
`"Name (handle)"` tokens is the direct analogue of what
`resolveParentLine` renders for one target, extended to N.

Not addressed by the three enumerated edge cases, but worth stating: if
two different target roles resolve to the *same* identity (an identity
occupying two roles, both of which `roleName` reports to), the list is
not deduplicated — the same `"Name (handle)"` token appears twice, and
`joinWithOxford` renders `"...Claude Agento (claude) and Claude Agento
(claude)."` No sibling pattern in this file dedups its join inputs
(`deriveAntiResponsibilities`'s `targets` slice can't repeat, because it
tracks first-seen **role**, and two roles are always textually
distinct even when co-occupied). Recommend leaving this undeduplicated
for the first cut: it requires the team author to have configured two
distinct `reports_to` edges pointing at roles occupied by the same
person, which is a real but unusual shape, and silently collapsing two
distinct edges into one rendered name hides a structural fact about the
team graph that a human reviewing the rendered file might want to see.
If it proves visually wrong in practice, add a de-dup-by-handle pass
before `joinWithOxford` in a follow-up — a one-line, low-risk change
that doesn't need to gate this fix.

### Edge case (c): target role has no occupying member, or that member's identity fails to load

**Recommendation: warn to stderr, skip that edge, keep going.** One
behavior for both sub-cases (no occupant found in `members`; occupant
found but `identities.Load` errors) — both drop the edge from the
returned slice without touching `expected`/`generated`/`failedMembers`
accounting in `GenerateAgentFilesTo`.

Directly mirrors `deriveAntiResponsibilities`'s handling of
`roles.Load(c.To)` failing (`generate_agents.go:327-331`): warn with
the target name and the underlying error, `continue`, let the rest of
the walk complete. The agent file still generates — with a shorter (or
absent) reports-to line, not a failed run. This is a fault in the
resolved *content* of one edge, not a fault in the ability to produce
the file at all; `GenerateAgentFilesTo`'s `failedMembers` tracking is
reserved for members whose *own* file failed to write after passing
every preflight check (`generate_agents.go:88-96`), which this isn't.

Two sub-cases collapse to one behavior, but are structurally different
in reachability:

- **No occupying member for the target role.** `team.ValidateStructural`
  (`internal/team/team.go:94-98`, enforced on every `Store.Load` since
  it's called at `store.go:118`) requires every collaboration's `From`
  and `To` to name a role filled by some member, before the `Team`
  value the generator loop ever sees. This branch is defensive, not
  reachable through the normal `teams.Load` path — same status as
  `deriveAntiResponsibilities`'s own `c.Type != "reports_to"` branch,
  which its own comment (`generate_agents.go:301-306`) documents as
  handling a case that's valid-but-deferred rather than structurally
  impossible. I keep the branch anyway: `members` and `collabs` are
  plain slices passed by value, not re-validated at this call, and a
  defensive check costs one loop and a stderr line.
- **Occupant found, but `identities.Load` fails.** Reachable in
  production the same way `roles.Load(c.To)` is reachable in
  `deriveAntiResponsibilities`: `ValidateStructural` guarantees the
  role *name* is referenced by a member, not that the referenced
  identity's YAML file is present, well-formed, or has resolvable
  content. A team member whose identity file was deleted, or a
  DES-057-style incomplete repo-set (missing ext), reaches this branch
  in real repos.

## Test rewrites

### `generate_agents_test.go:253` (`TestGenerateAgentFiles`, case `"basic generation"`)

The default `setupTestRepo` fixture (`generate_agents_test.go:70-162`)
defines **no `collaborations`** on the `engineering` team. Under the
fix, `bwk` (role `go-specialist`) has zero outgoing `reports_to` edges
in this fixture — edge case (a) — so the correct derived expectation is
**absence**, not a re-pinned present line:

```go
// generate_agents_test.go:253, replace:
assert.Contains(t, content, "You report to Claude Agento (COO/VP Engineering).")
// with:
assert.NotContains(t, content, "You report to Claude Agento (claude).")
```

I chose the specific string (`"You report to Claude Agento (claude)."`)
over a bare `"You report to"` substring check for one reason: the
anti-responsibilities preamble (`generate_agents.go:576`) also emits a
sentence starting `"You report to %s. These are not yours:"`. In this
particular fixture that section is also absent (same zero-collaboration
input), so a bare substring check happens to pass either way — but
pinning the exact rendered sentence documents *which* invariant is
under test and survives a future fixture edit that adds an unrelated
collaboration to this shared setup without adding one for
`go-specialist`.

I am not adding a `collaborations` edge to this test case's own `setup`
to exercise a populated reports-to line here, because `"basic
generation"`'s `check` func already asserts
(`generate_agents_test.go:260`):

```go
assert.Contains(t, content, "- adherence to punt-kit/standards/go.md\n\nTalents: engineering\n")
```

— i.e., the last Responsibilities bullet is immediately followed by
`Talents:`, which is only true when `## What You Don't Do` is also
absent. Adding a `go-specialist -> coo (reports_to)` edge here would
populate *both* sections at once (the default `coo` role already carries
`responsibilities: ["execution quality"]`, `generate_agents_test.go:147`),
breaking that existing, in-scope-of-nothing-I-was-asked-to-touch
assertion. Leaving `"basic generation"` on the vanilla zero-edge fixture
keeps every existing assertion in that test case true and gives this
case a second, independent job: proving the zero-target drop behavior
holds on the plainest fixture in the file, not just in a dedicated edge
case test.

### `generate_agents_test.go:849` (`TestGenerateAgentFiles_ToolScopeNote`)

This test's own position assertion needs a populated reports-to line —
the contract's requirement to preserve "toolScopeNote sits directly
after the reporting line" only means something when a reporting line
exists. Unlike `"basic generation"`, this test asserts nothing about
Responsibilities/Talents adjacency, so adding a collaboration here has
no collateral effect. Add a `setup`-equivalent fixture write before the
`GenerateAgentFiles` call, using the same edge shape as
`TestGenerateAgentFiles_AntiResponsibilities`'s `"single reports_to,
non-empty target"` case (`generate_agents_test.go:876-897`) for fixture
consistency across the file — same `go-specialist -> coo` edge, same
`claude`-occupies-`coo` mapping already true in the base fixture:

```go
func TestGenerateAgentFiles_ToolScopeNote(t *testing.T) {
    root, ids, teams, roles := setupTestRepo(t)

    ethosDir := filepath.Join(root, ".punt-labs", "ethos")
    writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
        "name":         "engineering",
        "repositories": []string{"punt-labs/ethos"},
        "members": []map[string]string{
            {"identity": "claude", "role": "coo"},
            {"identity": "bwk", "role": "go-specialist"},
            {"identity": "test-human", "role": "ceo"},
        },
        "collaborations": []map[string]string{
            {"from": "go-specialist", "to": "coo", "type": "reports_to"},
        },
    })
    // Rebuild stores after the fixture rewrite (existing pattern,
    // generate_agents_test.go:816-823).
    ids = identity.NewLayeredStore(identity.NewStore(ethosDir), identity.NewStore(ethosDir))
    teams = team.NewLayeredStore(ethosDir, ethosDir)
    roles = role.NewLayeredStore(ethosDir, ethosDir)

    require.NoError(t, GenerateAgentFiles(root, ids, teams, roles))

    data, err := os.ReadFile(filepath.Join(root, ".claude", "agents", "bwk.md"))
    require.NoError(t, err)
    content := string(data)

    assert.Contains(t, content, toolScopeNote, "generated agent must carry the tool-scope note")

    assert.Contains(t, content,
        "You report to Claude Agento (claude).\n\n"+toolScopeNote+"\n## Core Principles\n",
        "note must sit between the opening lines and the personality body")

    frontmatterEnd := strings.Index(content, "\n---\n\n")
    noteIdx := strings.Index(content, toolScopeNote)
    require.True(t, frontmatterEnd >= 0 && noteIdx >= 0,
        "frontmatter terminator and note must both be present")
    assert.Less(t, frontmatterEnd, noteIdx, "note must appear after the frontmatter")
}
```

`"You report to Claude Agento (claude)."` here is fixture-echoed output
(the test wrote the `go-specialist -> coo` edge and `claude`'s
`Name`/`Handle` explicitly), not a re-pinned bug value — the same
distinction the audit's "what did NOT get flagged" section already
draws for every other hardcoded `"Claude Agento"` fixture string in this
package (`docs/audit-hardcoded-team-strings.md:109-117`).

### New: `TestGenerateAgentFiles_ReportsTo`

A dedicated table-driven suite, structured identically to
`TestGenerateAgentFiles_AntiResponsibilities` (`generate_agents_test.go:863-1158`):
same `setup`/`assert` closure shape, same per-case fixture rewrite +
store-rebuild pattern, same reliance on `bwk.md` as the file under test.
Self-contained on purpose — a reader auditing reports-to behavior
should not have to cross-reference `"basic generation"` or
`ToolScopeNote` to find the zero-edge case, even though those two also
happen to exercise it. `TestGenerateAgentFiles_AntiResponsibilities`
sets this precedent itself: its own `"no reports_to edges"` case
(`:926-935`) is explicitly redundant with every other subtest's
incidental zero-collaboration default, and the comment there says so.

Three cases:

**1. `"zero targets — line omitted"`.** No `setup` (default
`setupTestRepo` fixture already has no `collaborations`, same reasoning
as `AntiResponsibilities`'s zero-edge case). Assert both the absence and
the exact byte shape at the boundary — this is the case that pins "no
double blank line, no placeholder" from the production-code section
above:

```go
assert.NotContains(t, content, "You report to Claude Agento (claude).")
assert.Contains(t, content,
    "You are Brian K (bwk), Go specialist sub-agent.\n\n"+toolScopeNote,
    "opening line must go straight to the blank-line+tool-scope-note block when no reports-to line is emitted")
```

**2. `"multiple targets — joined with Oxford join"`.** Fixture: add an
`architect` role (`responsibilities: ["system design reviews"]` — content
doesn't matter here, only its occupant's identity does), a second agent
identity `arc` (`kind: agent`, `name: "Ada Architect"`, minimal
personality/writing-style so the *rest* of `bwk`'s file still generates
— `arc` itself doesn't need to generate, only occupy the role), and two
outgoing edges from `go-specialist`:

```go
setup: func(t *testing.T, root string) {
    ethosDir := filepath.Join(root, ".punt-labs", "ethos")
    writeYAML(t, filepath.Join(ethosDir, "roles", "architect.yaml"), map[string]interface{}{
        "name":             "architect",
        "responsibilities": []string{"system design reviews"},
    })
    writeYAML(t, filepath.Join(ethosDir, "identities", "arc.yaml"), map[string]interface{}{
        "name": "Ada Architect", "handle": "arc", "kind": "agent",
    })
    writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
        "name":         "engineering",
        "repositories": []string{"punt-labs/ethos"},
        "members": []map[string]string{
            {"identity": "claude", "role": "coo"},
            {"identity": "bwk", "role": "go-specialist"},
            {"identity": "arc", "role": "architect"},
        },
        "collaborations": []map[string]string{
            {"from": "go-specialist", "to": "coo", "type": "reports_to"},
            {"from": "go-specialist", "to": "architect", "type": "reports_to"},
        },
    })
},
assert: func(t *testing.T, content string) {
    assert.Contains(t, content, "You report to Claude Agento (claude) and Ada Architect (arc).\n")
},
```

`arc`'s identity only needs `Reference`-loadable fields
(`Name`/`Handle`/`Kind`) — `deriveReportsToTargets` calls
`identities.Load(handle, identity.Reference(true))`, which does not
require `arc` to have a personality or writing style. This keeps the
fixture minimal; `arc.md` is never asserted on and never needs to exist.

**3. `"missing identity — warned and dropped, other target survives"`.**
Same "ghost" technique `TestDeriveAntiResponsibilities_MissingTarget`
(`generate_agents_test.go:1160-1231`) already uses to defeat
`ValidateStructural` while still producing a real downstream load
failure — but applied to the *identity*, not the role. A member
`ghost-agent` fills a real role (so `ValidateStructural` is satisfied),
but no `identities/ghost-agent.yaml` exists on disk, so
`identities.Load("ghost-agent", ...)` fails downstream inside
`deriveReportsToTargets`:

```go
setup: func(t *testing.T, root string) {
    ethosDir := filepath.Join(root, ".punt-labs", "ethos")
    writeYAML(t, filepath.Join(ethosDir, "roles", "advisor.yaml"), map[string]interface{}{
        "name":             "advisor",
        "responsibilities": []string{"informal guidance"},
    })
    writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
        "name":         "engineering",
        "repositories": []string{"punt-labs/ethos"},
        "members": []map[string]string{
            {"identity": "claude", "role": "coo"},
            {"identity": "bwk", "role": "go-specialist"},
            // ghost-agent fills "advisor" so ValidateStructural passes.
            // identities/ghost-agent.yaml is deliberately never
            // written, so identities.Load("ghost-agent") still fails
            // downstream — the invariant under test.
            {"identity": "ghost-agent", "role": "advisor"},
        },
        "collaborations": []map[string]string{
            {"from": "go-specialist", "to": "coo", "type": "reports_to"},
            {"from": "go-specialist", "to": "advisor", "type": "reports_to"},
        },
    })
},
assert: func(t *testing.T, content string, stderr string) {
    assert.Contains(t, stderr, "reports-to")
    assert.Contains(t, stderr, "ghost-agent")
    assert.Contains(t, content, "You report to Claude Agento (claude).\n")
    assert.NotContains(t, content, "ghost-agent")
},
```

This case needs `captureStderr` (`generate_agents_test.go:41-65`) around
the `GenerateAgentFiles` call, same as
`TestDeriveAntiResponsibilities_MissingTarget` — the table's `assert`
closure signature for this suite takes `(t, content, stderr)`, one
parameter wider than `TestGenerateAgentFiles_AntiResponsibilities`'s
`(t, content)`, since two of the three cases don't need stderr and the
third does.

One incidental note for the implementer: the main
`GenerateAgentFilesTo` loop (`generate_agents.go:98-140`) also iterates
`ghost-agent` as a team member in its own right and will independently
fail to load its identity, emitting its own unrelated
`"ethos: generate-agents: skipping \"ghost-agent\""` warning to the same
captured stderr. That's expected noise, not a second bug — the
assertions above use `Contains`, not exact-match, and don't collide with
it. `ghost-agent`'s own load failure is a generic `os.ErrNotExist`-class
error, not `repomiss.ErrIncompleteRepoSet`, so it does not increment
`expected`/`failedMembers` (`generate_agents.go:120-121`) and does not
change `GenerateAgentFilesTo`'s return value — confirmed by reading the
`errors.As` guard at that line.

## Migration / rollout

**No manual migration step. Verified from code, not assumed.**

`GenerateAgentFilesTo` writes `.claude/agents/<handle>.md` only when the
freshly computed content differs from what's already on disk
(`generate_agents.go:164-168`):

```go
existing, readErr := os.ReadFile(destPath)
if readErr == nil && string(existing) == content {
    generated++
    continue
}
```

`GenerateAgentFilesTo` is called from the `SessionStart` hook
(`internal/hook/session_start.go:113`) on every session start. Every
existing `.claude/agents/<handle>.md` in every repo currently carries
the literal `"You report to Claude Agento (COO/VP Engineering).\n"` line
(confirmed live in this repo's own `.claude/agents/bwk.md:45` per the
audit). Once this fix ships, the next `SessionStart` in any repo
recomputes `content` without that literal — content differs from disk —
and the file is rewritten automatically, through the same path that
already handles every other content change to these generated files
(role edits, personality edits, `output_format` additions). No script,
no `ethos migrate` step, no repo-by-repo action.

The rollout does depend on running an ethos binary built from this fix.
Repos pinned to an older release keep generating the old literal until
they upgrade — ordinary version-skew behavior, not specific to this
change.

## Explicitly out of scope

Per the mission contract's non-goals: `cmd/ethos/setup.go` and
`cmd/ethos/team_bundle.go`'s hardcoded `"claude"` reserved-COO-seat
handle (audit doc, "Surprise scope" section,
`docs/audit-hardcoded-team-strings.md:119-132`) is untouched by this
design. It is a different call graph (`cmd/ethos/setup`, not
`internal/hook`), a different shape of thing (documented intentional
seed-bundle default, enforced by explicit validation, not an
unconditional literal silently overriding real team data on every
render), and is not part of `ethos-5r7v`.
