# Build Plan: Rich Identity Attributes (ethos-ncw)

> **HISTORICAL — SHIPPED**: This build plan is preserved for reference.
> It describes pre-Phase-1 design work for `ethos-ncw` (rich identity
> attributes — talents, personalities, writing styles as markdown files
> referenced by slug). That feature shipped in Phase 1 and is documented
> in the current architecture at [`architecture.tex`](architecture.tex)
> and the ADR archive in [`../DESIGN.md`](../DESIGN.md).
>
> **Current build planning lives in
> [`ETHOS-ROADMAP.md`](ETHOS-ROADMAP.md)**. Do not use this file as a
> source of truth for current priorities.

---

## Goal

Convert identity attributes from inline strings to markdown file references.
An identity becomes a unique combination of reusable `.md` files plus core
identity fields. No backward compatibility needed — no external users.

## Current State

```yaml
# ~/.punt-labs/ethos/identities/jfreeman.yaml
name: Jim Freeman
handle: jfreeman
kind: human
email: jim@punt-labs.com
github: jmf-pobox
voice:
  provider: elevenlabs
  voice_id: charlie
writing_style: "Concise, precise, quantified."
personality: "Focused and goal oriented"
skills:
  - "executive"
  - "software engineer"
  - "product manager"
```

Three fields are inline strings. They carry no actionable information — a
consumer reading the identity gets labels, not definitions. The same skill
label can mean different things to different tools. There is no reuse — if
two identities share a skill, the description is duplicated (or absent).

## Target State

```yaml
# ~/.punt-labs/ethos/identities/jfreeman.yaml
name: Jim Freeman
handle: jfreeman
kind: human
email: jim@punt-labs.com
github: jmf-pobox
voice:
  provider: elevenlabs
  voice_id: charlie
agent: .claude/agents/jfreeman.md
writing_style: writing-styles/concise-quantified.md
personality: personalities/principal-engineer.md
skills:
  - skills/executive.md
  - skills/software-engineering.md
  - skills/product-management.md
```

### Filesystem Layout

```text
~/.punt-labs/ethos/
  active                          # plain text: active handle
  identities/
    jfreeman.yaml                 # core identity
    jfreeman.ext/                 # tool extensions (DES-008)
    claude.yaml
    claude.ext/
  skills/                         # shared skill definitions
    executive.md
    software-engineering.md
    product-management.md
    formal-methods.md
    delegation.md
  personalities/                  # shared personality definitions
    principal-engineer.md
    friendly-mentor.md
  writing-styles/                 # shared writing style definitions
    concise-quantified.md
    academic-formal.md
    direct-with-quips.md
```

### Path Resolution

Paths in identity YAML are **relative to the ethos root** (`~/.punt-labs/ethos/`).
This keeps YAML portable across machines. The Store resolves them to absolute
paths on read.

```go
// writing_style: writing-styles/concise-quantified.md
// resolves to: ~/.punt-labs/ethos/writing-styles/concise-quantified.md
```

### Attribute File Format

Each `.md` file is a self-contained definition. No required frontmatter.
Plain markdown. The entire file content is the attribute value.

```markdown
<!-- ~/.punt-labs/ethos/skills/software-engineering.md -->

# Software Engineering

Deep expertise in systems design, Go, Python, and shell scripting.
Emphasis on correctness over speed. Prefers formal methods where
applicable. Reviews code for security, correctness, and simplicity
in that order.

## Standards

- Test-driven development when feasible
- Table-driven tests with testify
- Errors are values, not strings
- No panics in library code

## Anti-patterns

- Over-engineering: adding abstractions before the second use case
- Premature optimization without profiling data
- Mocking internals instead of testing behavior
```

```markdown
<!-- ~/.punt-labs/ethos/personalities/principal-engineer.md -->

# Principal Engineer

Direct, accountable, evidence-driven. Leads with facts and data, not
opinions. Says "I don't know" when uncertain. Calibrates confidence
to evidence. Reduces tech debt with every change regardless of who
created it.

## Decision-making

- Root causes are provable — present facts, data, and tests
- Replace adjectives with data: "much faster" → "3x faster"
- Every statement must pass the "so what" test

## Communication

- Short sentences, under 30 words
- No performative validation or sycophancy
- Direct corrections without harshness
```

### Reuse Model

Two identities can reference the same file:

```yaml
# claude.yaml
personality: personalities/principal-engineer.md
skills:
  - skills/software-engineering.md
  - skills/delegation.md

# code-reviewer.yaml
personality: personalities/principal-engineer.md
skills:
  - skills/software-engineering.md
  - skills/formal-methods.md
```

Both share `principal-engineer.md` and `software-engineering.md`. The
code-reviewer adds formal methods; claude adds delegation. The personality
is defined once and shared.

## Implementation Sequence

### Phase 1: Schema (ethos-ncw.4)

**Goal**: Identity struct accepts paths, Store resolves content by default.

**Resolution model**: `Load()` resolves all markdown references and returns
content inline by default. Callers that only need paths (performance
optimization) pass `reference: true` to get paths without file reads. This
follows the JSON API `include` convention — full content is the default,
lightweight references are opt-in.

1. Update `Identity` struct in `internal/identity/identity.go`:
   - `WritingStyle string` — no type change, but semantics change from
     inline text to relative path in YAML
   - `Personality string` — same
   - `Skills []string` — same (paths instead of labels)
   - Add resolved content fields for JSON/display:

   ```go
   // Raw paths from YAML (always populated)
   WritingStyle string   `yaml:"writing_style,omitempty" json:"writing_style,omitempty"`
   Personality  string   `yaml:"personality,omitempty" json:"personality,omitempty"`
   Skills       []string `yaml:"skills,omitempty" json:"skills,omitempty"`

   // Resolved markdown content (populated by default, empty when reference=true)
   WritingStyleContent string            `yaml:"-" json:"writing_style_content,omitempty"`
   PersonalityContent  string            `yaml:"-" json:"personality_content,omitempty"`
   SkillsContent       map[string]string `yaml:"-" json:"skills_content,omitempty"`
   ```

2. Resolution in Store:

   ```go
   // Load reads an identity and resolves all attribute references.
   // Pass reference=true to skip resolution (paths only).
   func (s *Store) Load(handle string, opts ...LoadOption) (*Identity, error)

   type LoadOption func(*loadConfig)
   func Reference(v bool) LoadOption  // skip content resolution
   ```

3. Update `Validate()`:
   - `writing_style`: must end in `.md` and file must exist
   - `personality`: must end in `.md` and file must exist
   - `skills`: each entry must end in `.md` and file must exist
   - Validation is on `Save`, not `Load` (existing files with missing
     attributes should warn, not fail)

4. Path containment:
   - Compute absolute, cleaned paths for both the ethos root and the
     candidate path, then use `filepath.Rel` to get the relative path
   - Reject if the result is `".."`, starts with `".."+separator`, or
     is absolute — these indicate escape from the root
   - Do NOT use `strings.HasPrefix` for containment — it is unsafe
     (e.g., `/ethos2` matches prefix `/ethos`)
   - Symlinks are allowed (dotfiles repos); containment checks the logical
     path before following symlinks
   - Extract a small `isContained(root, path) (bool, error)` helper

5. Update MCP tools:
   - `get_identity` / `whoami`: return full content by default. Add
     optional `reference` boolean param — when true, response includes
     paths only (no content fields).
   - `create_identity`: validate that referenced paths exist.

6. Update CLI:
   - `ethos show <handle>`: display resolved content (print the markdown)
   - `ethos show <handle> --json`: full response with paths and content
   - `ethos show <handle> --reference`: paths only, no content
   - `ethos list`: unchanged (summary only, no resolution)

7. Tests:
   - Create `.md` files in `t.TempDir()` and reference them from identity YAML
   - Validate `Load()` resolves content by default
   - Validate `Load(handle, Reference(true))` returns paths only
   - Validate `List()` uses `Reference(true)` and does not read attribute files
   - Validate error for missing files on Save
   - Validate Load succeeds when attribute files are missing — content field
     empty, warning added to `Identity.Warnings []string`

### Phase 2: Directories (ethos-ncw.1, .2, .3 — parallel)

**Goal**: Create directory structure and ensure `ethos create` sets it up.

1. `ethos create` creates `skills/`, `personalities/`, `writing-styles/`
   under the ethos root if they don't exist.

2. `ethos doctor` checks for the directories and reports status.

3. The `install.sh` creates the directories alongside `identities/`.

These three beads are independent and can be done in parallel or as a
single commit since the work is just `mkdir -p` in a few places.

### Phase 3: Migration (ethos-ncw.5)

**Goal**: Convert jfreeman and claude to the new format.

1. Create the `.md` files:

   **jfreeman**:
   - `skills/executive.md`
   - `skills/software-engineering.md`
   - `skills/product-management.md`
   - `personalities/principal-engineer.md`
   - `writing-styles/concise-quantified.md`

   **claude**:
   - `skills/management.md`
   - `skills/delegation.md`
   - `skills/product-development.md`
   - `skills/engineering.md`
   - `personalities/friendly-direct.md`
   - `writing-styles/direct-with-quips.md`

2. Update both identity YAML files to reference paths.

3. This runs on the user's machine via a migration command or manually.
   Since there are only 2 identities and no external users, manual
   is fine.

## What Changes for Consumers

### Beadle (in progress — beadle-3um)

Beadle reads identity YAML via the sidecar contract. Currently it reads
`email` and `name`. It does not read `skills`, `personality`, or
`writing_style`. No impact on beadle's current integration.

If beadle later wants to know the active identity's personality (e.g.,
for email tone), it reads the `personality` path from the YAML, then
reads the `.md` file. Two file reads, both sub-ms.

### Biff

Biff reads identity for `/who` and `/finger` display. Currently shows
handle and name. Skills appear as labels in `/finger`. After migration,
biff would either:

- Show the skill filenames (without `.md`) as labels — zero work
- Resolve the `.md` files and extract the `# Title` line — minor work

### Vox

Reads `voice` binding only. No impact.

## Risks

1. **File not found at runtime**: An identity references `skills/foo.md`
   but the file was deleted. Mitigation: `Load()` sets content to empty
   string and adds a warning to `Identity.Warnings []string` (same
   pattern as `ListResult.Warnings`). `Save()` rejects the write if any
   referenced file is missing.

2. **Path traversal**: `writing_style: ../../../etc/passwd`. Mitigation:
   `filepath.Rel` after cleaning both paths — reject if relative result
   starts with `..`. Explicitly rejected in DES-010: `strings.HasPrefix`
   is unsafe (`/ethos2` matches `/ethos`). Symlinks are allowed.

3. **Large files**: A `.md` file could be arbitrarily large. No cap.
   Explicitly rejected in DES-010. If a file is too large, the author
   splits it. Truncating silently is worse than returning full content.

## Dependencies

- None. This is entirely within the ethos repo.
- Beadle integration (beadle-3um) reads identity YAML directly — the
  schema change is additive from beadle's perspective (new fields are
  strings either way).

## Beads

| Bead | Title | Dependency |
|------|-------|------------|
| ethos-ncw | Epic: Rich identity attributes | — |
| ethos-ncw.1 | Skills directory | — |
| ethos-ncw.2 | Personalities directory | — |
| ethos-ncw.3 | Writing styles directory | — |
| ethos-ncw.4 | Schema and store changes | — |
| ethos-ncw.5 | Migrate jfreeman + claude | ethos-ncw.1, .2, .3, .4 |
| ethos-ncw.6 | Installer deploys sidecar READMEs | — |
