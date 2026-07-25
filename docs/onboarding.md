# Design: `ethos setup` and the Foundation Bundle

## Problem

The install-to-working-team path requires 12 steps across 4
tools (shell, editor, ethos CLI, Claude Code). A new user must:

1. Run the installer
2. Run `ethos identity create` (human)
3. Answer 8 prompts (name, handle, kind, email, github, agent, personality, writing style, talents)
4. Run `ethos identity create` again (agent)
5. Answer 8 more prompts
6. `mkdir -p .punt-labs`
7. Hand-write `.punt-labs/ethos.yaml` with `agent: <handle>`
8. Decide whether to activate a team
9. Discover that `gstack` exists and what it assumes
10. `ethos team activate gstack`
11. Restart Claude Code
12. Verify the agent loaded its identity

Steps 3-9 require knowledge the user does not yet have. The
personality picker lists slugs with no preview. The gstack bundle
assumes a startup philosophy most users do not share. Most users
abandon after step 3 and use Claude Code anonymous.

The installer cannot run the wizard because piped scripts have no
TTY.

## Chosen Approach

One new command: `ethos setup`. One new embedded bundle: foundation.

`ethos setup` asks 5 questions, creates 2 identities (human +
agent), writes the repo config, activates a bundle, and generates
agent files. The new Quick Start becomes:

```bash
curl -fsSL https://punt-labs.com/ethos/install.sh | sh
ethos setup
```

## `ethos setup` Command Specification

### Help Text

```text
Set up ethos identities and team for the current repo

Usage:
  ethos setup [flags]

Flags:
      --bundle <name>   Team bundle to activate (default "foundation")
      --solo            Identity only, no team bundle
  -f, --file <path>     Create identities from a YAML file (non-interactive)
      --json            JSON output
  -h, --help            Help for setup

Examples:
  ethos setup                           # interactive wizard
  ethos setup --solo                    # identity only, no team
  ethos setup --bundle gstack           # use gstack instead of foundation
  ethos setup --file config.yaml        # non-interactive, from file
```

### Interactive Flow

The wizard runs when stdin is a TTY and `--file` is not set.

#### Prompt 1: Name

```text
Your name: Priya Chandran
```

No default. Required. Validation: non-empty string.

#### Prompt 2: Handle

```text
Handle [priya-chandran]:
```

Default: slugified name (lowercase, spaces to hyphens, non-alnum
stripped). Validation: matches `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
(same as `identity.validHandle` — no trailing hyphens, single-char
handles allowed).

#### Prompt 3: Email

```text
Email [priya@example.com]:
```

Default: `git config user.email`. The created identity carries this
email so `ethos whoami` resolves the caller by git email (resolution
chain step 3) and `ethos session start` can seat the roster root on a
fresh machine. Required: if the prompt is left blank and
`git config user.email` is unset, setup fails with a remedy and writes
nothing — an identity with no resolvable email is never persisted. The
same rule applies to `--file`: an absent `email` key defaults from git,
and an unresolvable email hard-fails.

#### Prompt 4: GitHub handle

```text
GitHub handle (optional):
```

No default. Optional: left empty when not supplied. Persisted on the
human identity when given (the `github` key in `--file`).

#### Prompt 5: Working style

```text
Working style:
  1. concise-quantified  -- terse, data-driven, no filler
  2. narrative            -- structured prose, context-heavy
  3. conversational       -- casual, direct
  (enter to skip)
Choice:
```

The slugs above are illustrative. The actual menu lists writing
styles available in the global store (seeded by `ethos seed`). Accepts a number, a slug, or empty to skip. If
skipped, human identity gets no writing style; agent identity gets
`concise-quantified` (the default for agents).

No personality prompt. The human gets `principal-engineer` (the
standard seeded personality). The agent gets the same. Users who
want something different use `ethos identity update` after setup.

No talents prompt. The human gets no talents. The agent gets
`engineering`. Users add domain talents later.

### What Gets Created

Given name="Priya Chandran", handle="priya-chandran",
style="concise-quantified", bundle="foundation":

#### Human identity

Created at `~/.punt-labs/ethos/identities/priya-chandran.yaml`:

```yaml
name: Priya Chandran
handle: priya-chandran
kind: human
email: priya@example.com   # defaulted from git config user.email
writing_style: concise-quantified
personality: principal-engineer
```

#### Agent identity

Created at `~/.punt-labs/ethos/identities/claude.yaml`:

```yaml
name: Claude
handle: claude
kind: agent
writing_style: concise-quantified
personality: principal-engineer
talents:
  - engineering
```

The agent handle is always `claude`. The agent name is always
`Claude`.

#### Repo config

Created at `.punt-labs/ethos.yaml`:

```yaml
agent: claude
team: foundation
active_bundle: foundation
```

Created only if running inside a git repository. If the file
exists, missing keys are added; existing keys are not overwritten.

#### Bundle activation

Sets `active_bundle: foundation` in the repo config (same as
`ethos team activate foundation`).

#### Leadership binding

Setup binds the human handle to the `ceo` seat and `claude` to the
`coo` seat, written as a repo-local team (the repo layer wins over
the bundle's placeholder `foundation-ceo`/`foundation-coo` seats).
Every specialist reports to the COO; the COO reports to the CEO
(DES-064). Leadership validation runs before any write — an
incomplete ceo/coo pair, or a human handle colliding with the
reserved `coo` seat (`handle: claude`), fails clean with nothing
persisted. A human-driven `ethos team activate` applies the same
rebind; an agent-driven activate skips it with a warning (the agent
is the COO, not the CEO) and leaves the human's setup leadership
intact.

#### Agent files

Generated at `.claude/agents/*.md` by calling
`GenerateAgentFiles` after bundle activation. Creates one `.md`
file per agent identity in the active team (excluding the main
agent `claude`). Each file contains the agent's persona,
responsibilities, tool restrictions, and anti-responsibilities
derived from the team graph.

### File Summary

| File | Scope | Created by setup | Overwritten if exists |
|------|-------|------------------|-----------------------|
| `~/.punt-labs/ethos/identities/<handle>.yaml` | global | yes | no |
| `~/.punt-labs/ethos/identities/claude.yaml` | global | yes | no |
| `.punt-labs/ethos.yaml` | repo | yes (merge) | no (merge) |
| `.punt-labs/ethos/teams/<bundle>.yaml` | repo | yes (leadership rebind) | yes (rebind) |
| `.claude/agents/*.md` | repo | yes | yes (idempotent) |

### TTY Detection

When stdin is not a TTY (piped install, CI, scripts):

- Without `--file`: print `ethos: setup requires a terminal (use
  --file for non-interactive mode)` to stderr, exit 2.
- With `--file`: run non-interactive mode (see below).

### Non-Interactive Mode (`--file`)

Accepts a YAML file with the same fields as the interactive prompts:

```yaml
name: Priya Chandran
handle: priya-chandran
writing_style: concise-quantified
bundle: foundation
```

`bundle: ""` or omitted means foundation. `solo: true` means no
bundle. All three prompts have equivalents. The file does not
support fields outside these -- it is not an identity file, it is
a setup config.

### `--solo` Mode

Skips bundle activation and agent file generation. Creates the
human and agent identities and writes the repo config with `agent:
claude` but no `team` or `active_bundle` keys. The agent gets
identity and personality but no team delegation structure.

### `--bundle <name>` Flag

Overrides the default bundle. The named bundle must be discoverable
(global or repo-local). If not found, exit 1 with:

```text
ethos: setup: bundle "foo" not found; available bundles:
  - foundation (embedded)
  - gstack (embedded)
```

### `--json` Output

Emits a JSON object summarizing what was created:

```json
{
  "human_identity": "priya-chandran",
  "agent_identity": "claude",
  "repo_config": ".punt-labs/ethos.yaml",
  "bundle": "foundation",
  "agent_files": [
    ".claude/agents/foundation-architect.md",
    ".claude/agents/foundation-implementer.md",
    ".claude/agents/foundation-reviewer.md",
    ".claude/agents/foundation-security.md"
  ],
  "skipped": []
}
```

The `skipped` array lists items that already existed and were not
overwritten (e.g. `"human_identity"` if `priya-chandran.yaml`
already exists).

### Idempotency

Each step checks before writing:

- **Identity exists**: skip, add to `skipped` list. Do not
  overwrite. Print `skipped: identity "priya-chandran" already
  exists` to stderr.
- **Repo config exists**: merge. Read existing YAML, add missing
  keys only. Never remove or overwrite existing keys.
- **Bundle already active**: skip activation. Print `skipped:
  bundle "foundation" already active`.
- **Agent files**: always regenerated (idempotent by content
  comparison in `GenerateAgentFiles`).

Running `ethos setup` twice produces the same end state as running
it once.

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Setup completed (some steps may have been skipped) |
| 1 | Error: identity validation failed, bundle not found, config write failed |
| 2 | Usage error: no TTY without `--file`, invalid flag combination |

### Error Messages

```text
ethos: setup: name is required
ethos: setup: handle "BAD" must be lowercase alphanumeric with hyphens
ethos: setup: bundle "foo" not found; available bundles:
  - foundation (embedded)
  - gstack (embedded)
ethos: setup: not in a git repository (identity created, skipping repo config)
ethos: setup requires a terminal (use --file for non-interactive mode)
ethos: setup: writing repo config: permission denied
```

Errors go to stderr. Partial progress is reported: if identity
creation succeeds but bundle activation fails, the identity is
kept and the error says what failed.

### Not in a Git Repository

When run outside a git repository:

- Human and agent identities are still created (they are global).
- Repo config, bundle activation, and agent file generation are
  skipped.
- Print `ethos: setup: not in a git repository (identities
  created, skipping repo config and team)` to stderr.
- Exit 0 (partial setup is still useful).

## Foundation Bundle Specification

### Bundle Manifest

`bundle.yaml`:

```yaml
name: foundation
version: 1
description: "General-purpose CEO/COO starter team for any codebase"
ethos_min_version: "3.7.0"
```

### Directory Structure

```text
bundles/foundation/
  bundle.yaml
  identities/
    foundation-ceo.yaml
    foundation-coo.yaml
    foundation-architect.yaml
    foundation-implementer.yaml
    foundation-reviewer.yaml
    foundation-security.yaml
  personalities/
    foundation-architect.md
    foundation-implementer.md
    foundation-reviewer.md
    foundation-security.md
  writing-styles/
    foundation-clear.md
    foundation-reviewer.md
  roles/
    ceo.yaml
    coo.yaml
    architect.yaml
    implementer.yaml
    reviewer.yaml
    security-reviewer.yaml
  talents/
    code-review.md
    engineering.md
    security.md
  teams/
    foundation.yaml
```

No `pipelines/` directory. Foundation uses the global seed
pipelines (`standard`, `quick`, `product`) which are already
deployed by `ethos seed`. This is a structural difference from
gstack, which ships its own pipeline variants.

### Identities

The bundle ships placeholder leadership seats (`foundation-ceo`,
`foundation-coo`) so the team validates and resolves standalone.
`ethos setup` and a human-driven `ethos team activate` rebind them
to the real human (CEO) and `claude` (COO) — see DES-064.

`foundation-ceo.yaml`:

```yaml
name: Owner
handle: foundation-ceo
kind: human
```

`foundation-coo.yaml`:

```yaml
name: Operations Lead
handle: foundation-coo
kind: agent
personality: foundation-architect
writing_style: foundation-clear
talents:
  - engineering
```

`foundation-architect.yaml`:

```yaml
name: Architect
handle: foundation-architect
kind: agent
personality: foundation-architect
writing_style: foundation-clear
talents:
  - engineering
```

`foundation-implementer.yaml`:

```yaml
name: Implementer
handle: foundation-implementer
kind: agent
personality: foundation-implementer
writing_style: foundation-clear
talents:
  - engineering
```

`foundation-reviewer.yaml`:

```yaml
name: Reviewer
handle: foundation-reviewer
kind: agent
personality: foundation-reviewer
writing_style: foundation-reviewer
talents:
  - engineering
  - code-review
```

`foundation-security.yaml`:

```yaml
name: Security Reviewer
handle: foundation-security
kind: agent
personality: foundation-security
writing_style: foundation-reviewer
talents:
  - engineering
  - security
```

Naming convention: `foundation-<role>`. Matches gstack's pattern
(`gstack-<role>`). Single-word agent names (Architect, Implementer)
-- no themed names. Foundation is utilitarian.

### Roles

Six roles: two leadership roles plus four specialists, reusing the
same names as gstack where applicable. Leadership roles carry no
`output_format` — they direct and delegate, they do not emit
FINDINGS (DES-064).

`ceo.yaml`:

```yaml
name: ceo
responsibilities:
  - set direction and own outcomes
  - approve major decisions and resolve escalations
tools:
  - Read
  - Grep
  - Glob
```

`coo.yaml`:

```yaml
name: coo
responsibilities:
  - run execution and delegate to specialists
  - review specialist output and report to the ceo
tools:
  - Read
  - Grep
  - Glob
  - Bash
```

`architect.yaml`:

```yaml
name: architect
responsibilities:
  - design review and tradeoff evaluation
  - architecture decisions and documentation
  - cross-component coordination
permissions:
  - edit-code
tools:
  - Read
  - Write
  - Edit
  - Bash
  - Grep
  - Glob
```

`implementer.yaml`:

```yaml
name: implementer
responsibilities:
  - feature implementation with tests
  - make check passes before reporting done
  - atomic commits with clear messages
permissions:
  - edit-code
tools:
  - Read
  - Write
  - Edit
  - Bash
  - Grep
  - Glob
```

`reviewer.yaml`:

```yaml
name: reviewer
responsibilities:
  - code review with severity-rated findings
  - correctness, performance, and maintainability assessment
  - file:line references for every finding
permissions:
  - read-code
tools:
  - Read
  - Grep
  - Glob
```

`security-reviewer.yaml`:

```yaml
name: security-reviewer
responsibilities:
  - security audit against OWASP Top 10
  - secrets and credential scanning
  - dependency supply chain review
permissions:
  - read-code
tools:
  - Read
  - Grep
  - Glob
  - Bash
```

Reviewer and security-reviewer have no Write/Edit tools. They
report findings; they do not fix code. Security-reviewer gets Bash
for running scanning tools.

### Team Definition

`foundation.yaml`:

```yaml
name: foundation
members:
  - identity: foundation-ceo
    role: ceo
  - identity: foundation-coo
    role: coo
  - identity: foundation-architect
    role: architect
  - identity: foundation-implementer
    role: implementer
  - identity: foundation-reviewer
    role: reviewer
  - identity: foundation-security
    role: security-reviewer
collaborations:
  - from: coo
    to: ceo
    type: reports_to
  - from: architect
    to: coo
    type: reports_to
  - from: implementer
    to: coo
    type: reports_to
  - from: reviewer
    to: coo
    type: reports_to
  - from: security-reviewer
    to: coo
    type: reports_to
```

The COO is the hub. The CEO (human owner) is the apex; the COO
(`claude`) runs execution, and architect, implementer, reviewer, and
security all report to the COO (DES-064). No product lead, no QA
engineer. Six seats, five edges.

### Personalities

Each personality is a short markdown file. Foundation personalities
are functional, not themed. Example for `foundation-implementer.md`:

```markdown
# Implementer

Writes production code from design documents and specs.

## Core Principles

- Tests first when feasible. Make check must pass.
- One logical change per commit.
- Error handling at every call site.
- No abstractions until the second use case demands one.
```

Foundation personalities are 10-20 lines each. They define behavior
without philosophy. Contrast with gstack, which embeds "Boil the
Lake" and "Search Before Building" as first principles.

### Writing Styles

Two writing styles:

`foundation-clear.md`: Direct, minimal, data-over-adjectives.
Used by architect and implementer.

`foundation-reviewer.md`: Finding-oriented -- severity, file:line,
one sentence per finding. Used by reviewer and security-reviewer.

### Talents

Three talent files: `engineering.md`, `code-review.md`, and
`security.md`. These reference the corresponding global talents
from seed but can be overridden per-bundle.

### Pipeline Templates

Foundation does not ship its own pipelines. The global seed
deploys `standard`, `quick`, and `product` to
`~/.punt-labs/ethos/pipelines/`. These are language-agnostic and
work with any team structure.

Users invoke them the same way:

```bash
ethos mission pipeline instantiate standard \
  --var feature=auth --var target=internal/auth/
```

The pipeline's `archetype` fields map to roles via the archetype
system; no pipeline-level team coupling.

## Install Script Changes

### No Automatic `ethos setup`

The installer must not run `ethos setup` automatically. Reasons:

1. Piped installs (`curl | sh`) have no TTY. The wizard would fail.
2. The user has not chosen a repo yet. Setup writes repo config.
3. The installer already runs `ethos seed` and `ethos doctor`.
   Adding a fourth interactive step to a piped script is hostile.

### What Changes

The final message in `install.sh` changes from:

```text
Run "ethos create" to create your first identity.
```

to:

```text
Run "ethos setup" in your project directory to get started.
```

That is the only installer change. `ethos seed` still deploys
starter content (including the new foundation bundle). `ethos
doctor` still validates the installation.

## Rejected Alternatives

### Auto-create identities during install

The installer runs as `curl | sh`. No TTY is guaranteed. Even when
a TTY exists, adding interactive prompts to a piped install script
breaks the user's mental model -- they expect the pipe to finish,
not to ask questions. Identity creation belongs in a separate
command that the user runs intentionally.

### Use gstack as the default bundle

Gstack embeds a specific philosophy (Boil the Lake, Search Before
Building, User Sovereignty) and a 6-agent team with product lead
and QA engineer. A solo developer working on a Django app does not
need a product lead. Shipping gstack as default tells most users
"this product is not for you" within the first minute.

### Config file generator instead of a wizard

A command that emits a YAML template for the user to edit moves the
problem rather than solving it. The user still has to understand
every field, make choices about personalities and writing styles
they have never seen, and get the syntax right. The wizard makes
the choices for them with sane defaults and lets them override
later.

### Merge setup into install

Running setup inside the installer couples two concerns: system
installation (binary, plugin, seed content) and project
configuration (identities, repo config, team). The installer is
run once per machine. Setup is run once per project. They have
different scopes and different lifecycles.

### Prompt for personality and talents during setup

The existing `identity create` asks 8 prompts. Users do not have
opinions about personalities and talents during first use. The
wizard asks 5 questions, applies sane defaults, and gets out of
the way. Users who care use `identity update` after they understand
the system.

## Migration Plan

### Existing users with identities but no bundle

Running `ethos setup` detects existing identities, skips identity
creation, and offers bundle activation if no bundle is active. The
user gets a team without losing their customized identities.

### Existing users with the legacy submodule

`ethos setup` does not touch the legacy `.punt-labs/ethos/`
submodule. If it detects a legacy submodule, it prints:

```text
ethos: setup: legacy submodule detected at .punt-labs/ethos/
Run "ethos team migrate" to convert to the bundles layout.
```

Migration remains a separate command (`ethos team migrate`) because
it modifies git history (deinit + rm + submodule add). Mixing git
submodule operations into a setup wizard is a surprise the user did
not sign up for.

### Existing users with gstack active

`ethos setup` sees an active bundle and skips bundle activation.
It creates missing identities (if any) and fills in missing config
keys. It does not switch the bundle from gstack to foundation.

### Backwards compatibility

`ethos setup` composes with existing commands. It calls the same
internal functions as `identity create`, `team activate`, and
`GenerateAgentFiles`. No existing command behavior changes. Users
who prefer the manual path can still use individual commands.

The `identity create` command remains available (the top-level
`ethos create` alias is marked `Hidden: true`; `identity create`
is the visible subcommand). Setup does not deprecate it -- it is
the power-user path for creating additional identities beyond the
initial pair.

## Implementation Notes

These are constraints for the implementer, not design decisions.

- `ethos setup` is a new file `cmd/ethos/setup.go`. It registers
  under the `admin` group alongside `seed` and `doctor`.
- The foundation bundle lives at
  `internal/seed/sidecar/bundles/foundation/` with the same
  structure as `internal/seed/sidecar/bundles/gstack/`.
- `ethos seed` deploys the foundation bundle to
  `~/.punt-labs/ethos/bundles/foundation/` alongside gstack.
- The `--file` flag reuses the same validation as interactive mode.
  The YAML schema is a setup config, not an identity file.
- The wizard writes prompts to stderr (same pattern as `create.go`).
  Answers read from stdin. Output (created file paths, JSON) goes
  to stdout.
