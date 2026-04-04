# Documentation

Technical writing for software projects. Documentation exists to reduce
the time between "I need to know X" and "now I know X." Every document
has a specific audience and a specific job. If you cannot name both,
do not write the document.

## Documentation Types

### README

The entry point for every repository. A README answers three questions
in order: what is this, why should I care, how do I use it.

Structure:

1. **Title** -- project name, one line.
2. **Description** -- one paragraph. What the project does, who it is for.
3. **Quick start** -- fewest steps to a working result. Copy-paste ready.
4. **Features** -- bullet list of capabilities. No marketing.
5. **Commands / API** -- reference for everything the user can do.
6. **Setup / Installation** -- prerequisites, install steps, verification.
7. **Development** -- how to build, test, lint, contribute.
8. **License** -- SPDX identifier or full text.

A README is not a tutorial. It is a reference card. Keep it scannable.
If a section exceeds 40 lines, extract it into a separate document and
link to it.

### API Documentation

Every public interface gets documented: endpoints, functions, CLI
commands, MCP tools. The standard for each entry:

- **Signature or endpoint** -- exact syntax.
- **Parameters** -- name, type, required/optional, default, constraints.
- **Return value** -- type, structure, possible values.
- **Errors** -- every error condition, its code or message, and what
  the caller should do about it.
- **Example** -- one minimal, working example per entry.

```text
## CreateUser

POST /api/v1/users

| Parameter | Type   | Required | Description          |
|-----------|--------|----------|----------------------|
| name      | string | yes      | Display name         |
| email     | string | yes      | Must be unique       |
| role      | string | no       | Default: "member"    |

### Success Response

201 Created

{"id": "u_abc123", "name": "Mal", "email": "mal@serenity.ship"}

### Errors

| Status | Code            | When                    |
|--------|-----------------|-------------------------|
| 400    | invalid_email   | Email fails validation  |
| 409    | email_exists    | Email already in use    |
| 422    | missing_field   | Required field omitted  |
```

Document error responses with the same rigor as success responses.
Callers spend more time handling errors than happy paths.

### Tutorials

Step-by-step guides for specific tasks. A tutorial differs from a README:
the README says what exists, the tutorial walks through doing something
specific with it.

Structure:

1. **Goal** -- one sentence. "By the end, you will have X."
2. **Prerequisites** -- exact versions, installed tools, assumed knowledge.
3. **Steps** -- numbered, each with one action and one observable result.
4. **Verification** -- how to confirm the tutorial worked.
5. **Next steps** -- links to related tutorials or reference docs.

Every code block in a tutorial must be copy-pasteable and produce the
output shown. Test tutorials by running them from scratch on a clean
environment. Stale tutorials are worse than no tutorials because they
teach wrong things confidently.

### Architecture Decision Records

ADRs capture the why behind design choices. Code shows what was built.
Commit messages show when. ADRs show why this approach and not another.

Structure:

```markdown
## ADR-NNN: Title (STATUS)

**Status**: PROPOSED | SETTLED | DEPRECATED | SUPERSEDED by ADR-MMM

### Problem

What situation requires a decision? One paragraph.

### Decision

What was decided? Be specific enough that someone could implement
the decision from this paragraph alone.

### Reasoning

Why this approach? What constraints drove the choice? What tradeoffs
were accepted?

### Rejected Alternatives

For each alternative:
- What it was
- Why it was rejected (specific, not "didn't feel right")

### Consequences

What becomes easier? What becomes harder? What new constraints exist?
```

Status transitions:

- PROPOSED -- under discussion, not yet implemented.
- SETTLED -- implemented and in production.
- DEPRECATED -- no longer applies but kept for history.
- SUPERSEDED -- replaced by a newer ADR (link to it).

Never delete an ADR. Supersede or deprecate it. The history of rejected
approaches prevents revisiting dead ends.

### Changelogs

Changelogs are for users. They answer: "What changed since I last
updated?" Follow the Keep a Changelog format.

```markdown
## [Unreleased]

### Added
- New `ethos resolve-agent` command for repo-scoped agent lookup

### Changed
- `ethos whoami` now includes extension data in output

### Fixed
- Session purge no longer removes active sessions

## [2.6.1] - 2026-03-28

### Fixed
- Extension session_context injection in SubagentStart hook
```

Rules:

- Group by: Added, Changed, Deprecated, Removed, Fixed, Security.
- Write entries from the user's perspective, not the developer's.
  "Fixed crash when config file is missing" not "Added nil check in
  loadConfig."
- Every entry is one line. If it needs more, the entry is too broad --
  split it.
- Link version headers to diff comparisons on the hosting platform.
- The `[Unreleased]` section always exists at the top. It accumulates
  entries until the next release tags them with a version number.
- Semantic versioning: MAJOR for breaking changes, MINOR for new
  features, PATCH for bug fixes.

## Code Comments

Comments explain why, not what. The code already says what it does.
A comment that restates the code is noise that drifts out of sync.

### When to Comment

- **Non-obvious constraints**: "Must run before X because Y depends on
  the state set here."
- **Business rules**: "Timeout is 30s because the upstream SLA is 25s
  plus 5s buffer."
- **Workarounds**: "Using X instead of Y because of bug Z (link to issue)."
- **Performance choices**: "Pre-allocated because profiling showed this
  path allocates 10k times per request."
- **Legal or compliance**: "GDPR requires deletion within 30 days."

### When Not to Comment

- Restating the code: `// increment counter` above `counter++`.
- Explaining basic language features.
- TODO comments without an owner or issue link. Unlinked TODOs are
  graffiti -- they never get done.
- Commented-out code. Delete it. Version control remembers.

### Package and Function Comments

Go: package comment on the package clause, function comment on every
exported function. Start with the function name.

```go
// ResolveIdentity walks the resolution chain (repo-local, global)
// and returns the first matching identity for the given handle.
// Returns ErrNotFound if no identity matches.
func ResolveIdentity(handle string) (*Identity, error) {
```

Python: module docstring, class docstring, public method docstring.
Use imperative mood for the first line.

```python
def resolve_identity(handle: str) -> Identity:
    """Resolve an identity by handle from repo-local then global scope.

    Args:
        handle: The identity handle to resolve.

    Returns:
        The resolved Identity.

    Raises:
        NotFoundError: If no identity matches the handle.
    """
```

## Writing Principles

### Describe, Do Not Sell

Documentation describes what exists. It does not persuade, market, or
promote. "Ethos resolves identities from repo-local and global scopes"
-- not "Ethos provides a powerful identity resolution system."

### Facts, Not Opinions

"The CLI responds in under 50ms" -- not "The CLI is fast."
"Supports Go 1.26 and later" -- not "Works with modern Go versions."

### Data Over Adjectives

Replace every adjective with a number or delete it.

| Do not write               | Write instead                        |
|----------------------------|--------------------------------------|
| "very fast"                | "responds in 12ms at p99"            |
| "highly scalable"          | "tested to 10k concurrent sessions"  |
| "lightweight"              | "4MB binary, zero runtime deps"      |
| "easy to use"              | show the 3-line quick start          |
| "robust error handling"    | "returns typed errors for all 7 failure modes" |

### Imperative Mood

Write instructions as commands. "Run `make check`" -- not "You should
run `make check`" or "The user can run `make check`."

### Active Voice

"The resolver checks repo-local first" -- not "The repo-local scope
is checked first by the resolver."

### Second Person for Procedures

"You" is fine in tutorials and setup guides. Avoid it in reference
documentation.

## Banned Patterns

These patterns signal documentation that is padding rather than
informing. Remove them on sight.

- **Marketing language**: "powerful", "innovative", "world-class",
  "enterprise-grade", "best-in-class", "seamless", "blazing fast."
- **Weasel words**: "generally", "typically", "in most cases",
  "should work", "may vary."
- **Hollow adjectives**: "very", "really", "quite", "extremely",
  "highly", "incredibly."
- **Hedge stacking**: "It might possibly be the case that perhaps..."
  Pick one qualifier or remove them all.
- **Filler transitions**: "It is worth noting that", "It is important
  to understand that", "As previously mentioned." Delete the phrase;
  the sentence that follows is the content.
- **Passive evasion**: "Mistakes were made", "It was decided that."
  Name the actor.
- **Undated references**: "recently", "soon", "in a future release."
  Use dates or version numbers.

## Diagrams

### When to Use

- System boundaries and data flow between components.
- Sequence of interactions between services.
- State machines with more than 3 states.
- Deployment topology (what runs where).

### When Not to Use

- Anything a 5-line bullet list explains as clearly.
- Internal function call chains (the code is the diagram).
- Diagrams that require a paragraph of explanation to interpret.

### C4 Model

Use C4 levels when documenting architecture:

1. **Context** -- system and its external actors. What interacts with it.
2. **Container** -- deployable units (binaries, services, databases).
3. **Component** -- major internal modules within a container.
4. **Code** -- rarely needed. Use only for complex algorithms or
   state machines.

Most documentation needs levels 1 and 2. Level 3 is for onboarding.
Level 4 is almost never worth maintaining.

### Sequence Diagrams

Use for interactions that involve timing, ordering, or multiple actors.

```text
User -> CLI: ethos whoami
CLI -> Resolver: ResolveIdentity(handle)
Resolver -> RepoStore: Load(handle)
RepoStore --> Resolver: not found
Resolver -> GlobalStore: Load(handle)
GlobalStore --> Resolver: Identity
Resolver --> CLI: Identity
CLI --> User: formatted output
```

### Keep Them Current

A stale diagram is actively harmful -- it teaches the wrong mental
model. Every diagram must have an owner (file or person) responsible
for updating it when the system changes.

Rule: if you change the system, check its diagrams. If you cannot
keep a diagram current, delete it. An absent diagram prompts
investigation. A wrong diagram prevents it.

## Living Documentation

### Docs That Rot

Documentation that is not verified on every change will drift from
reality. After enough drift, developers stop trusting the docs and
stop reading them. At that point the docs are negative value -- they
cost time to maintain and provide no benefit.

### Verification Strategies

- **README code blocks**: extract and run them in CI. If the example
  does not compile or produce the documented output, the build fails.
- **API docs**: generate from source (OpenAPI, godoc, pydoc). Do not
  maintain handwritten API docs that duplicate source annotations.
- **Tutorials**: run them as integration tests. A tutorial is a script
  with prose between the steps.
- **Architecture diagrams**: review in every PR that changes the
  components depicted.

### Update on Every Change

When you change behavior, update the docs in the same commit. Not in
a follow-up PR. Not in a separate ticket. In the same commit. If the
docs and the code are in different commits, they will diverge the
moment someone cherry-picks or reverts.

## Cross-Referencing

### Single Source of Truth

Every fact lives in one place. Other documents link to it. When the
fact changes, one update propagates everywhere.

Bad: copy the installation steps into the README, the tutorial, and
the contributing guide. When a step changes, two of the three copies
go stale.

Good: installation steps live in `INSTALL.md`. The README links to it.
The tutorial links to it. The contributing guide links to it.

### Link Format

Use relative links within a repository. They survive forks and mirrors.

```markdown
See [installation](./INSTALL.md) for setup instructions.
See [ADR-003](./DESIGN.md#adr-003-session-storage) for the design rationale.
```

Absolute URLs only for external resources.

### Orphan Detection

Documentation without incoming links is invisible. Periodically check
that every document is reachable from the README or a table of contents.
Unreachable docs get stale because nobody knows they exist.

## Anti-Patterns

- Writing documentation after the project ships. Write it as you build.
- Treating docs as a separate workstream with separate deadlines.
  Docs are part of the definition of done for every change.
- Documentation-by-screenshot. Screenshots cannot be searched, diffed,
  or kept current. Use text with occasional screenshots for visual
  elements only.
- Giant monolith documents. If a file exceeds 500 lines, it covers
  too many topics. Split by audience or by task.
- Documentation that describes the ideal system instead of the actual
  system. Write what exists today. Use ADRs to capture what you intend
  to change and why.
