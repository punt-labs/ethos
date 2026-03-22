# Design Decisions

## DES-001: Sidecar architecture (SETTLED)

**Decision**: Ethos publishes identity state to a known filesystem location.
Other tools (Vox, Beadle, Biff) optionally read it. No import dependency.

**Reasoning**: If ethos were a dependency of vox/biff/beadle, upgrading ethos
would force upgrades of everything downstream. The sidecar pattern makes the
contract a file format — stable, versionable, and tools adopt it at their own
pace.

**Rejected alternatives**:

- Shared library with ethos types imported by consumers — creates tight
  coupling and version lock-step.
- Message bus (NATS) for identity propagation — over-engineered for state
  that changes rarely.

## DES-002: User-global vs repo-local storage (SETTLED)

**Decision**: Identities live in `~/.punt-labs/ethos/identities/`. Repo-local
config (active identity, agent roster) lives in `.punt-labs/ethos/config.yaml`.

**Reasoning**: Your identity doesn't change per repo. But which identities are
active in a given project, and which agents are available, is project-scoped.
This matches Git's own split: `~/.gitconfig` for identity, `.git/config` for
repo overrides.

**Rejected alternatives**:

- Everything in `~/.config/ethos/` — loses per-repo agent roster.
- Everything in `.punt-labs/ethos/` — identity files would need copying
  across repos.

## DES-003: Go over Python (SETTLED)

**Decision**: Implement ethos in Go.

**Reasoning**: Ethos will be queried on session start (via SessionStart hook),
on every Biff `/who`, and by Vox before speaking. Go's ~10ms cold start vs
Python's ~200ms+ is the difference between invisible and noticeable. The module
has a small surface area (YAML I/O, CLI, MCP tools) — Python's ecosystem
advantage doesn't apply.

**Rejected alternatives**:

- Python with lightweight entry point — still 200ms+ even with lazy imports.
  Hooks standard § 12 documents this tax.

## DES-004: Unified identity schema for humans and agents (SETTLED)

**Decision**: Humans and agents use the same YAML schema and creation flow.
The only structural difference is `kind: human` vs `kind: agent`, which
determines whether the identity can be invoked as a subagent.

**Reasoning**: An identity is an identity. It has a name, voice, email, GitHub
handle, writing style, talents, personality. Whether a human or LLM inhabits it
is a property, not a type distinction.

**Rejected alternatives**:

- Separate schemas for human profiles vs agent definitions — duplicates
  fields, creates maintenance burden, and implies a philosophical distinction
  that doesn't exist in practice.

## DES-005: Agent definition as channel binding (SETTLED)

**Decision**: The Claude Code agent `.md` file is treated as a channel binding,
like voice or email. An ethos identity *has* an agent definition the same way
it *has* an email address.

**Reasoning**: The agent `.md` defines *what tools and workflow*. The ethos
identity defines *who*. They are complementary, not competing. Ethos can
generate the `.md` or point to an existing one.

**Rejected alternatives**:

- Ethos identity replaces agent `.md` entirely — loses the system prompt
  and tool restrictions that make Claude Code agents effective.
- Ethos identity and agent `.md` as independent, unlinked artifacts —
  creates drift between "who the agent is" and "what the agent does."

## DES-006: Store type for identity persistence (SETTLED)

**Decision**: Identity CRUD operations live in `internal/identity.Store`,
a struct that takes a root directory path. `cmd/ethos/` has a thin `store()`
helper that creates a `DefaultStore` from `$HOME`. MCP handlers receive the
Store via the `Handler` struct at construction time.

**Reasoning**: The original scaffolding had identity I/O functions as
package-level functions in `cmd/ethos/identity.go` with a parallel `Identity`
struct. This made the persistence untestable (hardcoded `$HOME` paths) and
duplicated the canonical type in `internal/identity/`. The Store pattern
makes all CRUD operations testable with `t.TempDir()` and eliminates the
type duplication. Injecting the Store into MCP handlers avoids `os.Exit`
in server context.

**Rejected alternatives**:

- Package-level functions with `identityDir()` returning `string` — silent
  empty-string failure when `$HOME` is unset, untestable without env mutation.
- Passing root path to every function call — noisy signatures, repeated
  path construction.

## DES-008: Generic extension mechanism (SETTLED)

**Status**: Settled.

### Problem

Consumers of ethos (Beadle, Biff, Vox, Lux, and future tools) need to
store tool-specific attributes on identities. A GPG key ID for email
signing, a preferred TTY name, a voice mood default — these are real
needs, but they do not belong in the ethos identity schema. Ethos is a
low-level identity facility. It must not know about its consumers.

Adding consumer-specific fields (e.g., `gpg_key_id` for Beadle) couples
a low-level facility to a high-level consumer. Every new consumer would
require an ethos schema change, a new release, and coordination across
repos. This does not scale.

### Design

Ethos provides a generic, namespaced key-value extension mechanism at
two scopes:

1. **Persona-level** — durable attributes stored on the filesystem
2. **Session-participant-level** — ephemeral attributes in the session
   roster (DES-007's `ext` map)

Any software can read and write arbitrary key-value pairs scoped to a
namespace (the tool's name) without ethos knowing what the keys mean.

### Persona-Level Extensions

Extensions live in a directory alongside the identity YAML file:

```text
~/.punt-labs/ethos/identities/
  mal.yaml                    # ethos owns — core identity fields
  mal.ext/                    # extension directory
    beadle.yaml                    # beadle owns — beadle-specific attributes
    biff.yaml                      # biff owns — biff-specific attributes
    vox.yaml                       # vox owns — vox-specific attributes
```

Each file is a flat YAML map of key-value pairs owned by the named tool:

```yaml
# mal.ext/beadle.yaml
gpg_key_id: 3AA5C34371567BD2
imap_server: mail.punt-labs.com
trust_default: verify
```

```yaml
# mal.ext/biff.yaml
preferred_tty: tty1
```

**Ownership rules:**

- Ethos creates the `<persona>.ext/` directory when the identity is
  created.
- Each tool manages its own `<namespace>.yaml` file inside the directory.
- Tools may read/write their namespace file directly (sidecar contract)
  or through the ethos CLI/MCP interface.
- Tools must not write to another tool's namespace file.
- Ethos never reads or interprets extension contents except to assemble
  the merged view.

### Merged View

When any consumer asks "who is mal?", ethos returns the complete
picture — core identity fields plus all extensions:

```yaml
name: Mal Reynolds
handle: mal
kind: human
email: mal@serenity.ship
github: mal
voice:
  provider: elevenlabs
  voice_id: abc123
agent: .claude/agents/mal.md
writing_style: |
  Direct. Short sentences. Data over adjectives.
personality: |
  Principal engineer. Formal methods, accountability.
talents:
  - formal-methods
  - product-strategy
ext:
  beadle:
    gpg_key_id: 3AA5C34371567BD2
    imap_server: mail.punt-labs.com
  biff:
    preferred_tty: tty1
  vox:
    default_mood: calm
```

This applies to all read interfaces: `ethos show`, `ethos show --json`,
and the `get_identity` MCP tool. The `ext` map is always present in the
output (empty map `{}` when no extensions exist).

`ethos list` and `ethos list --json` return summary data only (no
extensions). Extensions are returned only when loading a specific
identity.

### Session-Participant-Level Extensions

The session roster (DES-007) already defines an `ext` map per
participant. This serves the same purpose at session scope — Biff
writes `ext.biff.tty: s004`, Vox writes `ext.vox.voice_active: true`.

Session extensions are ephemeral (deleted with the roster on session
end). Persona extensions are durable (persist across sessions).

The two levels are independent. A tool may store durable defaults at the
persona level and ephemeral session state at the participant level.

### CLI Commands

```text
ethos ext get <persona> <namespace> [key]     Read one key or all keys
ethos ext set <persona> <namespace> <key> <value>   Write a key
ethos ext del <persona> <namespace> [key]     Delete one key or entire namespace
ethos ext list <persona>                       List all namespaces
```

### MCP Tools

```text
ext_get      Read extension key(s) for a persona
ext_set      Write extension key for a persona
ext_del      Delete extension key or namespace
ext_list     List namespaces for a persona
```

### Validation Constraints

Ethos enforces structural constraints without interpreting values:

| Field | Rule |
|-------|------|
| Namespace | `^[a-z][a-z0-9-]*$`, max 32 characters |
| Key | `^[a-z][a-z0-9_]*$`, max 64 characters |
| Value | Any valid YAML scalar, max 4096 bytes |
| Keys per namespace | Max 64 |
| Namespaces per persona | Max 32 |

Keys and namespaces are validated on write. Values are stored as-is
(ethos does not parse or interpret them). Reads return raw values.

### Why Files-Per-Namespace

| Alternative | Rejected Because |
|-------------|-----------------|
| Add fields to identity YAML | Couples ethos to consumers; every consumer needs an ethos release |
| Single `ext.yaml` with all namespaces | Tools can corrupt each other's data; merge conflicts on concurrent writes |
| Database (SQLite, etc.) | Violates sidecar contract — tools must be able to read files directly |
| Generic `ext` map inside identity YAML | Ethos would need to parse/merge on every identity write; risk of data loss |

Files-per-namespace means:

- Tools can read their data directly without ethos (sidecar contract)
- No merge conflicts between tools writing concurrently
- Identity YAML stays clean — only ethos-owned fields
- File permissions can differ per namespace if needed
- Adding a new consumer requires zero ethos changes

### Interaction with DES-001 (Sidecar Contract)

The sidecar contract extends to extensions. The file format is the
contract:

- Path: `~/.punt-labs/ethos/identities/<persona>.ext/<namespace>.yaml`
- Format: flat YAML map (string keys, scalar values)
- Any tool can read any namespace file directly without importing ethos

The CLI/MCP interface is a convenience layer for tools that don't want
to manage file I/O or that operate in environments without direct
filesystem access (e.g., MCP-only agents).

---

## DES-007: Session roster — multi-participant identity awareness (SETTLED)

**Status**: Settled. All open questions resolved.

### Problem

Ethos currently tracks one "active" identity. In practice, a session has
multiple participants — a human, a primary agent, and subagents. Any
participant needs to answer:

1. **Who am I?** — my own identity (persona)
2. **Who is everyone else?** — all participants, their personas, and relationships

### Design Principles

**No human/agent distinction in the session model.** The registry has a
`kind` field on each persona (human vs agent), but the session treats all
participants uniformly. The structural difference is captured by the
parent-child tree, not by a role or kind field.

**Initiator/delegate is implicit in the tree.** The root participant (no
parent) is the session initiator. Any participant is an initiator relative
to its children and a delegate relative to its parent. No explicit role
field is needed.

**Extensible participant records.** Other tools (Biff, Vox, Beadle) need
to decorate participants with their own metadata. Each participant has an
`ext` map keyed by tool name. Ethos does not validate or constrain `ext`
contents — the sidecar contract extends to the session file. Values are
`map[string]any`; consumers are responsible for type assertions.

### Data Model

Session roster stored at `~/.punt-labs/ethos/sessions/<session-id>.yaml`:

```yaml
session: ba3bb20f
started: 2026-03-18T14:30:00Z
participants:
  - agent_id: mal          # OS login ($USER)
    persona: mal            # ethos identity lookup key
    parent: ~
    ext:
      biff:
        tty: s001

  - agent_id: "19147"           # topmost claude ancestor PID (process tree walk)
    persona: archie
    parent: mal
    ext:
      biff:
        tty: s004

  - agent_id: a5734dd           # Claude Code subagent ID (from hook input)
    persona: code-reviewer       # auto-matched from agent_type → ethos persona
    parent: "19147"
    ext: {}

  - agent_id: a93c2be
    persona: silent-failure-hunter
    parent: "19147"
    ext: {}

  - agent_id: b8823ff
    persona: ~                   # built-in subagent, no ethos persona
    agent_type: Explore          # raw type preserved for tools that need it
    parent: "19147"
    ext: {}
```

**Fields per participant:**

| Field | Source | Purpose |
|-------|--------|---------|
| `agent_id` | Runtime — OS login, Claude PID, or subagent ID | Unique instance identifier |
| `persona` | Ethos identity registry | Links to full identity profile; `~` when no persona exists |
| `agent_type` | Claude Code hook input | Raw type (e.g., `Explore`, `code-reviewer`); preserved for tools |
| `parent` | Session structure | Who initiated this participant; `~` for root |
| `ext` | Other tools (Biff, Vox, etc.) | Tool-scoped metadata; open `map[string]any` |

**Null persona handling:** When `persona` is `~`, the participant has no
ethos identity. Query tools return the `agent_type` as a display name.
A default persona can be configured in repo config for common agent types
(e.g., map all `code-reviewer` subagents to a shared persona with a
defined writing style and personality).

**Primary agent identification:** The SessionStart hook resolves the
primary agent's `agent_id` by walking the process tree to the topmost
`claude` ancestor PID, using the same `ps -eo pid=,ppid=,comm=` approach
proven in Biff (see `find_session_key()` in biff DES-011/DES-011a). This
PID is stable across all hook invocations in the same session. Falls back
to `$PPID` when no `claude` ancestor is found.

**Tree relationships:**

```text
mal (root — no parent)
  └─ 19147 (archie)
       ├─ a5734dd (code-reviewer)
       ├─ a93c2be (silent-failure-hunter)
       └─ b8823ff (Explore, no persona)
```

Any participant can derive from the tree:

- **Root / session initiator**: walk up to `parent: ~`
- **My initiator**: my `parent`
- **My delegates**: anyone whose `parent` is my `agent_id`
- **Siblings**: same `parent`
- **Full chain of authority**: walk from me to root

### Lifecycle via Hooks

Claude Code provides hook events that map directly to roster operations:

| Hook Event | Roster Operation | Data Available |
|------------|-----------------|----------------|
| `SessionStart` | Create roster, join root + primary agent | `session_id`, `$USER`, Claude PID (tree walk) |
| `SubagentStart` | Join subagent | `session_id`, `agent_id`, `agent_type` |
| `SubagentStop` | Leave subagent | `session_id`, `agent_id` |
| `SessionEnd` | Tear down roster | `session_id` |

**Concurrency:** Multiple SubagentStart hooks may fire in parallel.
Roster writes use `flock(LOCK_EX)` on a lock file
(`sessions/<session-id>.lock`) before read-modify-write. This matches
the `O_EXCL` pattern in `Store.Save` and requires no daemon.

**Cleanup:** The `SessionEnd` hook deletes the roster file. If the
session crashes without `SessionEnd` firing, `ethos session purge`
cleans up stale rosters by checking whether the primary agent's PID
is still alive. The ethos CLI and MCP tools own this — no external
mechanism.

### Commands

| Command | What it does |
|---------|-------------|
| `ethos iam <persona>` | Set my persona for this session (used at session start) |
| `ethos session` | List all participants in the current session |
| `ethos session join` | Add a participant (called by hooks) |
| `ethos session leave` | Remove a participant (called by hooks) |
| `ethos session purge` | Clean up stale session rosters |

`ethos iam <persona>` declares "I am this persona in this session."
`ethos whoami` reads it back via the resolution chain (DES-011). The
caller's `agent_id` is determined automatically (OS login, Claude PID
walk, or subagent ID from context).

### MCP Tools

| Tool | Method | Purpose |
|------|--------|---------|
| `session` | `iam` | Declare persona for current participant |
| `session` | `roster` | Return full participant list with tree |
| `session` | `join` | Register a new participant |
| `session` | `leave` | Deregister a participant |

### Resolved: Persona Default by Agent Type

Each agent type has a default persona — the identity in the registry with
the same name as the `agent_type`. When SubagentStart fires with
`agent_type: code-reviewer`, the hook looks up `persona[agent_type]` in
the registry. If an identity named `code-reviewer` exists, it becomes the
participant's persona. If not, `persona: ~`.

No mapping configuration. The convention is: create an ethos identity with
the same name as the agent type.

```bash
ethos create -f code-reviewer.yaml   # persona for all code-reviewer subagents
ethos create -f explore.yaml         # persona for all Explore subagents
```

A specific subagent instance can override the default via `ethos iam`
to declare a different persona explicitly.

### Resolved: Session ID Propagation to Non-Hook Callers

Hooks receive `session_id` in stdin JSON, but non-hook callers (Biff, Vox)
need the session ID too. The SessionStart hook writes the session ID to a
PID-keyed file:

```text
~/.punt-labs/ethos/sessions/current/<claude-pid>
```

Contents: the session ID (plain text). Any descendant process walks the
process tree to the topmost `claude` ancestor PID, reads that file, and
gets the session ID. This is the same pattern Biff uses for unread count
files (see Biff DES-011). The SessionEnd hook deletes the file.

### Constraints

- Must work without a daemon — ethos is a CLI tool, not a server.
- Must survive subagent spawning — new processes need access.
- Must not require ethos as a dependency — the sidecar contract (DES-001)
  holds. Other tools read known paths.
- Must handle concurrent sessions on the same machine.
- Participant records must be extensible by other tools without ethos
  changes.

---

## DES-009: Hook compatibility with Claude Code (SETTLED)

**Status**: Settled after 5 broken releases (v0.3.0–v0.3.3).

### Problem

Ethos hooks crashed on every session start with `SessionStart:startup hook error`. The error persisted across 5 release cycles, each claiming to fix it.

### Root Causes (3 independent bugs, all required)

**1. `INPUT=$(cat)` blocks indefinitely.**

Claude Code pipes JSON to hook subprocesses via stdin but does not always close the pipe promptly for SessionStart events. Bash `cat` is equivalent to Python's `sys.stdin.read()` — it blocks until EOF. Biff discovered and documented this same bug (see biff DES-025). Every ethos hook used `INPUT=$(cat)` and was vulnerable.

**Fix**: Use `read -r -t 1` with a 1-second timeout for hooks that need stdin data. SessionStart hook doesn't need stdin at all — removed the read entirely.

**2. `"matcher": ""` in hooks.json.**

Every ethos hook entry in hooks.json had `"matcher": ""` (empty string). Every working plugin either omits the matcher key entirely (catch-all) or uses a specific regex pattern. The empty string matcher may be treated differently by Claude Code — either matching nothing or causing a configuration error.

**Fix**: Remove the `"matcher"` key from all non-PostToolUse hooks, matching biff/vox/quarry pattern.

**3. Missing patterns from working plugins.**

Compared to biff (the most mature plugin), ethos hooks were missing:

- Kill switch: `[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0`
- `exit 0` at the end of every hook
- `hookEventName` field in the JSON output
- JSON output via heredoc (not `printf` with manual escaping)
- `PLUGIN_ROOT` derived from `dirname "$0"` (not `CLAUDE_PLUGIN_ROOT` env var)

**Fix**: Rewrote all 5 hooks to match biff's proven patterns exactly.

### Why It Took 5 Releases

1. **Never compared to working code.** The hooks were written from scratch without reading biff's working implementation. Every fix was based on theory (bash version, `set -u`, array syntax) rather than diffing against a known-good reference.

2. **Point fixes without pattern search.** `set -u` was removed from session-start.sh but not the other 4 hooks. `INPUT=$(cat)` was the actual bug but was never identified because no one grepped for `cat)` across all hooks.

3. **No end-to-end testing.** Every fix was verified by running `make check` (Go tests) and piping JSON to the hook manually. Neither reproduces the actual Claude Code execution environment where stdin pipes aren't closed promptly.

4. **No comparison to working plugins.** The user had to ask "did you look at all the other ones which work?" after 5 failed attempts. Reading biff's DESIGN.md (DES-025) would have identified the `stdin.read()` bug immediately — it was documented with root cause analysis, rejected alternatives, and test cases.

### Cross-Project Pattern

Any Claude Code plugin hook that reads stdin with `cat`, `sys.stdin.read()`, or any blocking read is vulnerable. The safe patterns are:

- **Don't read stdin** if you don't need the data (session-start)
- **`read -r -t <seconds>`** in bash for hooks that need stdin data
- **`select` + `os.read(fd, N)`** in Python (see biff DES-025)
- **`INPUT=$(cat)` is safe for PostToolUse** — Claude Code closes the pipe for these events

### Final Hook Patterns

All ethos hooks now follow these rules:

| Pattern | Source | Required |
|---------|--------|----------|
| `[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0` | biff | Yes — emergency kill switch |
| `set -euo pipefail` | biff, vox | Yes |
| `PLUGIN_ROOT="$(cd "$(dirname "$0")/.." && pwd)"` | biff, vox | Yes — not env var |
| `read -r -t 1` instead of `INPUT=$(cat)` | biff DES-025 | Yes for hooks needing stdin |
| No stdin read at all | biff session-start | Yes for SessionStart |
| No `"matcher"` key in hooks.json | biff, vox | Yes for catch-all hooks |
| `exit 0` at end | biff | Yes |
| JSON via heredoc with `hookEventName` | biff | Yes for hooks returning context |

### Rejected Alternatives

- **`set -eo pipefail` (drop `-u`)** — unnecessary. The actual bug was `cat` blocking, not unbound variables. Biff uses `set -euo pipefail` and works fine.
- **Downloading release binary via `go install`** — `go install` doesn't support `-ldflags`, producing `ethos dev`. Fix: download pre-built binary from GitHub releases. This was a separate installer bug discovered during the same cycle.
- **`mktemp` in `/tmp`** — not atomic for `settings.json` updates. Use `mktemp "${SETTINGS}.tmp.XXXXXX"` on the same filesystem.

---

## DES-010: Rich identity attributes — markdown references (SETTLED)

**Status**: Settled. Implemented in PR #47. Build plan at `docs/build-plan.md`.

### Problem

Identity attributes (`writing_style`, `personality`, `talents`) are inline
strings — labels with no actionable content. A consumer reading the identity
gets `"software engineer"` but not what that means: no standards, no
anti-patterns, no tools. There is no reuse — if two identities share a
talent, the description is duplicated or absent.

### Decision

Convert all three attribute fields from inline strings to relative paths
pointing to markdown files. Each attribute type gets its own directory
under the ethos root:

```text
~/.punt-labs/ethos/
  talents/                        # shared talent definitions
  personalities/                  # shared personality definitions
  writing-styles/                 # shared writing style definitions
```

An identity becomes a unique combination of reusable `.md` files plus
core identity fields (name, handle, kind, email, github, voice, agent).

```yaml
writing_style: writing-styles/concise-quantified.md
personality: personalities/principal-engineer.md
talents:
  - talents/executive.md
  - talents/software-engineering.md
```

Paths are relative to the ethos root (`~/.punt-labs/ethos/`). The `agent`
field is the exception — it resolves relative to the repo root, not the
ethos root, because agent `.md` files live in the project.

### Resolution model

`Load()` resolves all markdown references and returns content inline by
default. This is the common case — most callers need the content.

Callers that only need paths (performance optimization for display-only
use cases like biff `/who`) pass `Reference(true)` to skip file reads.
This follows the JSON API `include` convention: full content is the
default, lightweight references are opt-in.

The Identity struct carries both:

- **Path fields** (`WritingStyle`, `Personality`, `Skills`) — always
  populated from YAML, present in both modes
- **Content fields** (`WritingStyleContent`, `PersonalityContent`,
  `SkillsContent`) — populated by default, empty when `reference: true`

`List()` always passes `Reference(true)` — listing all identities should
not read every attribute file. Content resolution is for single-identity
reads.

MCP tools (`get_identity`, `whoami`) return full content by default.
An optional `reference` boolean parameter returns paths only.

### Missing file handling

When `Load()` resolves an attribute and the `.md` file is missing, the
content field is set to an empty string and a warning is added to
`Identity.Warnings []string`. This matches the existing `ListResult.Warnings`
pattern. Consumers can check `Warnings` to detect broken references.
`Save()` validates that all referenced files exist and rejects the save
if any are missing.

### Path containment

Attribute paths must resolve within the ethos root. Containment is
verified by computing absolute, cleaned paths for both the ethos root
and the candidate path, then using `filepath.Rel` to ensure the
resulting relative path does not escape the root (rejects `..`,
`../` prefixes, and absolute results). Naive `strings.HasPrefix` is
not used — it is unsafe (e.g., `/ethos2` matches prefix `/ethos`).
Symlinks are allowed — users may symlink attributes from a dotfiles
repo. The containment check runs on the logical path before following
symlinks.

### Sidecar README deployment

The repo `sidecar/` directory contains README.md files for each
subdirectory of `~/.punt-labs/ethos/`. The installer copies these during
installation so users and consuming tools have documentation of the file
layout and sidecar contract. READMEs are deployed with `cp -n` (no
clobber) to avoid overwriting user modifications.

### Uniform for humans and agents

A human's talent file describes their expertise and standards. An agent's
talent file describes its capabilities and tools. Same format, same
resolution, same reuse model. The `kind` field distinguishes human from
agent — the attribute system does not.

### Rejected alternatives

- **Inline strings with optional file override** — two sources of truth,
  unclear which wins. Clean break is simpler.
- **Load returns paths, caller resolves** — pushes complexity onto every
  consumer. Most callers need content, not paths.
- **Cap resolved content at 64KB** — silent truncation is worse than
  returning the full file. If a file is too large, the author splits it.
- **Frontmatter in `.md` files for metadata** — unnecessary complexity.
  The filename is the identifier, the content is the value. If metadata
  is needed later, frontmatter can be added without breaking existing
  files.

---

## DES-011: Identity resolution — humans from git/OS, agents from repo config (SETTLED)

### Problem

The session start hook assigns personas to the human root and the primary
agent. Both currently get the same persona — the "global active identity"
read from `~/.punt-labs/ethos/active`. This has three problems:

1. **Repos are multi-user.** A tracked `active:` field in repo config
   makes no sense — the repo belongs to the whole team, not one person.
   The human identity must come from an external source specific to the
   current user.

2. **Human and agent get the same persona.** The session roster shows the
   same identity for both root and primary agent. They are different
   participants and should have different identities.

3. **The "global active identity" concept has no clear purpose.** If the
   human is whoever git/OS says they are, and the agent is configured per
   repo, there is nothing left for a global active file to do.

### Design

#### `whoami` resolution chain

`ethos whoami` answers "who am I?" for any caller — human in a shell,
primary agent in a Claude Code session, or sub-agent. Resolution tries
each source in order, stopping at the first match:

| Step | Source | Match field |
|------|--------|-------------|
| 1 | `iam` declaration (PID-keyed file) | Explicit persona set via `ethos iam` |
| 2 | `git config user.name` | Identity `github` field |
| 3 | `git config user.email` | Identity `email` field |
| 4 | `$USER` (OS login) | Identity `handle` field |

Step 1 checks for an explicit `iam` declaration. This uses the same
PID-keyed file mechanism as Claude Code sessions
(`~/.punt-labs/ethos/sessions/current/<PID>`). `ethos whoami` walks the
process tree upward, checking for a `current/<PID>` file at each
ancestor. This works identically for:

- **Interactive shell**: `ethos iam jfreeman` writes to `current/$$`.
  `ethos whoami` in the same shell (or a child process) finds it.
- **Claude Code session**: the SessionStart hook writes to
  `current/<claude-pid>`. MCP tools find it via process tree walk.

The `iam` declaration lives as long as the process it's keyed to. When
the shell exits or Claude Code terminates, the PID becomes stale.
`ethos session purge` cleans up stale PID files by checking whether the
process is alive.

Steps 2–4 are the automatic resolution chain. Each step queries the
identity store for an identity whose field value matches the source. If
no identity matches any step, the caller has no ethos persona — the raw
`$USER` value is used as a display name.

**Rationale for field mapping:**

- `git config user.name` is commonly set to a GitHub username (e.g.,
  `jmf-pobox`), which matches the identity's `github` field.
- `git config user.email` matches the identity's `email` field directly.
- `$USER` is the OS login (e.g., `jfreeman`), which by convention matches
  the identity's `handle`.

These are three different fields on the identity schema, queried against
three different environment sources. No new schema fields are needed.

#### Agent resolution

The primary agent identity is configured per repo in
`.punt-labs/ethos/config.yaml`:

```yaml
agent: claude
```

This file is tracked in git — the whole team shares the same agent
configuration. When `agent:` is unset, the primary agent has no ethos
persona (undefined). It does **not** fall back to the human's persona —
that would conflate two distinct session participants.

#### Session binding

Resolution feeds into the session lifecycle (DES-007):

- **SessionStart hook**: resolves human via steps 2–4, resolves agent
  from repo config, calls `iam` for both. After `iam`, subsequent
  `whoami` calls hit step 1 (PID-keyed file) and return immediately.
- **SubagentStart hook**: resolves sub-agent persona by `agent_type`
  convention (DES-007 § Persona Default by Agent Type), calls `iam`.
- **Interactive shell**: user calls `ethos iam <persona>` explicitly.
  `ethos whoami` returns it for the life of that shell.
- **`ethos whoami`**: read-only query. Runs the resolution chain
  (steps 1–4). No write path.

### Removed concepts

- **`~/.punt-labs/ethos/active` file** — no longer needed. Human identity
  comes from git/OS, not a manually-set pointer.
- **`ethos whoami <handle>` (write path)** — no "set active" operation.
  The human is whoever git/OS says they are.
- **`active:` field in repo config** — repos are multi-user. The repo
  does not configure who the human is.
- **`resolve.Resolve()` repo-local-to-global chain for humans** — dead
  code that was never wired in; replaced by the multi-field lookup.

### Commands affected

| Command | Before | After |
|---------|--------|-------|
| `ethos whoami` | Reads `~/.punt-labs/ethos/active` | Runs human resolution chain (git/OS → identity store) |
| `ethos whoami <handle>` | Sets global active file | Removed |
| `ethos doctor` | Checks global active file | Checks that git/OS resolves to a valid identity |

### Identity store query

The resolution chain requires looking up identities by non-handle fields
(`github`, `email`). The identity store gains a `FindBy(field, value)`
method that scans all identities and returns the first match. This is a
linear scan over YAML files — acceptable given the small number of
identities (typically < 20).

### Rejected alternatives

- **Global active file as the sole source** — ignores multi-user reality.
  A repo checked out by two developers would show the same human persona
  for both.
- **Repo config `active:` field for humans** — same problem. Tracked
  config is shared; human identity is per-user.
- **Match `git config user.name` against identity `handle`** — fails when
  git username differs from ethos handle (e.g., `jmf-pobox` vs
  `jfreeman`). The `github` field is the correct match target.
- **Agent falls back to human persona** — conflates two distinct session
  participants. An unnamed agent is more honest than a mislabeled one.
- **Require git for human resolution** — too rigid. `$USER` fallback
  handles environments without git (containers, CI, non-repo dirs).

---

## DES-012: Namespaced slash commands — no top-level deployment (SETTLED)

### Decision

All ethos slash commands use the plugin namespace (`/ethos:*`). No
commands are deployed to `~/.claude/commands/`. Every MCP tool has a
corresponding slash command.

### Commands

| Command | MCP Tool | Description |
|---------|----------|-------------|
| `/ethos:whoami` | `whoami` | Show the caller's identity |
| `/ethos:list-identities` | `list_identities` | List all identities |
| `/ethos:get-identity` | `get_identity` | Show identity details |
| `/ethos:create-identity` | `create_identity` | Create a new identity |
| `/ethos:talent` | `talent` | Manage talents (method dispatch) |
| `/ethos:personality` | `personality` | Manage personalities (method dispatch) |
| `/ethos:writing-style` | `writing_style` | Manage writing styles (method dispatch) |
| `/ethos:ext` | `ext` | Manage extensions (method dispatch) |
| `/ethos:session` | `session` | Manage session roster (method dispatch) |
| `/ethos:iam` | `session` (method: iam) | Declare persona in current session |

Dev variants use `/ethos-dev:*` automatically via the plugin name in
`plugin.json`.

### Rationale

Top-level commands like `/skill`, `/session`, `/ext` occupy generic
names that will conflict with Claude Code built-ins or other plugins.
Plugin-namespaced commands (`/ethos:skill`) are collision-free and
clearly attributed.

The session-start hook previously copied command `.md` files to
`~/.claude/commands/` for top-level access. This is removed — the
plugin namespace is sufficient.

### Rejected alternatives

- **Top-level deployment** (`/skill`, `/personality`) — generic names
  will conflict. Claude Code or another plugin will claim `/skill`.
- **Prefix without namespace** (`/ethos-skill`) — inconsistent with
  the plugin namespace convention (`plugin:command`).
- **Selective top-level** (only deploy unique names) — still fragile.
  Any new plugin or Claude Code feature could claim the name.

## DES-013: Session-start hook must not write to shared directories (SETTLED)

### Incident — 2026-03-21 (8 hours lost across 2 engineers + 1 agent)

The ethos v0.7.0 session-start hook (`hooks/session-start.sh`) caused a
complete failure of top-level slash command discovery on every machine
where ethos was installed. All `/read`, `/who`, `/vox`, `/write`,
`/find`, `/lux`, and other top-level commands from `~/.claude/commands/`
disappeared. Two machines were affected. A third machine that never had
ethos v0.7.0 was unaffected.

### Root cause

The v0.7.0 session-start hook performed two destructive operations on
shared global state:

**1. Copied command files to `~/.claude/commands/`.**

```bash
for cmd_file in "$PLUGIN_ROOT/commands/"*.md; do
    dest="$COMMANDS_DIR/$name"
    cp "$cmd_file" "$dest"
done
```

This deployed 7 files (`ext.md`, `iam.md`, `personality.md`,
`session.md`, `skill.md`, `whoami.md`, `writing-style.md`) into the
shared `~/.claude/commands/` directory. Each file contained an
`allowed-tools` frontmatter entry referencing `mcp__plugin_ethos_self__*`
MCP tools.

When the ethos MCP server was not running (ethos not installed, or
installed but not active in the current repo), Claude Code's command
parser encountered `allowed-tools` entries referencing non-existent MCP
tools. Instead of skipping those individual files, the parser failed for
the **entire** `~/.claude/commands/` directory — killing discovery of
every top-level command from every plugin (biff, vox, quarry, beadle,
lux, dungeon).

This failure was **silent**. No error message. Commands simply
disappeared from the skill list.

The damage was **persistent**. Uninstalling the ethos plugin removed the
registry entry and cache but left the 7 copied files in
`~/.claude/commands/`. Every subsequent Claude Code session continued to
fail on those orphaned files. The only fix was manually deleting the 7
ethos files from `~/.claude/commands/`.

**2. Mutated `~/.claude/settings.json` via `jq`.**

```bash
jq --arg g "$PROD_GLOB" \
  '.permissions.allow = (.permissions.allow // []) + [$g]' \
  "$SETTINGS" > "$TMPFILE"
mv "$TMPFILE" "$SETTINGS"
```

This re-serialized the entire settings.json through `jq` to add a
single permission entry. The `jq` round-trip is a secondary risk — it
could re-order keys, normalize Unicode, or strip encoding details that
Claude Code's parser depends on. In this incident, `jq` mutation was
confirmed harmless (rewriting settings.json with identical content did
not fix the issue), but it remains a fragile pattern.

### Why diagnosis took 8 hours

1. **Silent failure.** Claude Code gave no error — commands just
   vanished. The failure mode (entire directory disabled) was
   disproportionate to the cause (7 bad files out of 35).

2. **Wrong initial hypothesis.** The first 4 hours were spent comparing
   plugin directory structures, command frontmatter, MCP server
   behavior, and plugin.json format between biff (working) and ethos
   (not working). This was the wrong layer — the ethos agent was trying
   to get `/ethos:whoami` to work when the actual damage was to
   `~/.claude/commands/`.

3. **Red herring: `jq` mutation.** 2 hours were spent investigating
   whether `jq`'s re-serialization of settings.json was the corruption
   vector. It was not.

4. **Persistent damage after uninstall.** Uninstalling the ethos plugin
   did not remove the copied command files, so the failure persisted
   across restarts and reinstalls, making it appear version-independent.

5. **Coincidence masking.** The ethos plugin installation, the
   `~/.claude/commands/` breakage, and the ethos agent's separate
   problem (plugin commands not appearing as namespaced skills) were
   three different issues that occurred simultaneously, creating
   confusion about which symptom belonged to which cause.

### Decision

**Deploy commands from an install script, not a session-start hook.**

Biff, Vox, Lux, and Quarry all deploy top-level slash commands to
`~/.claude/commands/` — and it works. The difference is **when** and
**how**:

| Project | Deploys commands via | Runs when | Guarantees |
|---------|---------------------|-----------|------------|
| Biff | `biff install` | Once, explicitly | Plugin installed first, MCP server registered |
| Vox | `vox install` | Once, explicitly | Plugin installed first, MCP server registered |
| Lux | `lux install` | Once, explicitly | Plugin installed first, MCP server registered |
| Quarry | `quarry install` | Once, explicitly | Plugin installed first, MCP server registered |
| **Ethos** | **session-start hook** | **Every session** | **None** |

The install scripts ensure the plugin and MCP server are registered
before deploying commands. By the time command files land in
`~/.claude/commands/`, the `allowed-tools` entries they reference
(`mcp__plugin_biff_tty__*`, etc.) resolve to real MCP tools.

The ethos session-start hook had no such guarantee. It copied command
files on every session start regardless of whether the ethos MCP server
was active. When the MCP tools didn't resolve, Claude Code's command
parser failed for the entire `~/.claude/commands/` directory.

Specifically:

1. **Command deployment belongs in `ethos install`, not in the
   session-start hook.** Follow the established pattern from biff, vox,
   lux, quarry: install script registers the plugin, deploys commands,
   and sets permissions — once.

2. **Do not mutate `~/.claude/settings.json` from a hook.** Permission
   entries for plugin MCP tools should be set by the install script. A
   hook that re-runs `jq` on settings.json every session is fragile
   and unnecessary.

3. **Session-start hooks may read global state but not write it.** A
   plugin's session-start hook may read `settings.json`, read identity
   files, set environment variables, and emit `hookSpecificOutput`. It
   must not create, modify, or delete files outside `$PLUGIN_ROOT` or
   the plugin's own data directory (`~/.punt-labs/ethos/`).

### Fix applied

1. Removed the 7 ethos command files from `~/.claude/commands/` on
   affected machines.
2. Removed the `jq` settings mutation from the session-start hook
   (refactor/namespaced-commands branch).
3. Removed the command-copy logic from the session-start hook
   (refactor/namespaced-commands branch, DES-012).
4. Ethos must implement `ethos install` following the biff/vox/lux/quarry
   pattern before deploying top-level commands again.

### Rules for session-start hooks

| Allowed | Forbidden |
|---------|-----------|
| Read `~/.claude/settings.json` | Write `~/.claude/settings.json` |
| Read `~/.claude/commands/*.md` | Create/delete files in `~/.claude/commands/` |
| Write to `~/.punt-labs/ethos/` | Write to `~/.claude/` (any path) |
| Emit `hookSpecificOutput` JSON | Run `jq` on shared config files |
| Set environment variables | Modify `installed_plugins.json` |
| Call `ethos` CLI subcommands | Call `claude plugin install/uninstall` |

---

## DES-014: Rename `skill` → `talent` system-wide (SETTLED)

### Problem

`skill` is a reserved name in Claude Code. When a plugin command file
is named `skill.md`, Claude Code's command parser fails for the
**entire** `commands/` directory — silently breaking all plugin commands
from that plugin. This was discovered during DES-012 when
`/ethos:skill` poisoned the autocomplete for all 10 ethos commands.

### Decision

Rename `skill` to `talent` everywhere:

- MCP tool: `skill` → `talent`
- CLI subcommand: `ethos skill` → `ethos talent`
- Command file: `skill.md` → `talent.md`
- Identity YAML field: `talents:` → `talents:`
- Identity struct fields: `Skills` → `Talents`, `SkillContents` → `TalentContents`
- Attribute Kind: `attribute.Skills` → `attribute.Talents`
- Storage directory: `~/.punt-labs/ethos/skills/` → `~/.punt-labs/ethos/talents/`
- Sidecar: `sidecar/skills/` → `sidecar/talents/`

### Breaking change

The identity YAML schema changes from `talents:` to `talents:`.
Existing identity files must be updated manually. No external users
exist — this is acceptable.

### Rejected alternatives

- **Keep `skill` internally, only rename the command file** — creates a
  confusing split where the command is `/ethos:talent` but the MCP tool
  is `skill`, the CLI is `ethos skill`, and the storage is `skills/`.
- **Use `skills` (plural) for the command file** — might avoid the
  reserved name conflict, but doesn't address the root issue. Claude
  Code could reserve `skills` next.
- **Use a different word only for the command file** — same split
  problem as option 1.

## DES-015: Plugin development via cache symlink (PROPOSED)

### Problem

Claude Code plugins are loaded from a versioned cache directory at
`~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/`. During
development, the cached snapshot is stale — changes to commands, hooks,
skills, and agents in the working tree are not reflected until the
plugin is re-published and re-fetched.

The binary (Go/Python) can be rebuilt and installed independently
(`make install`), but plugin prompt files (`.md` commands, hook shell
scripts, skill definitions) are only read from the cache. This creates
a two-speed problem: MCP tool changes take effect after `make install`
and restart, but prompt changes require manually copying files into
the cache or re-publishing.

### Decision

Add `make dev` and `make undev` targets to the Makefile:

- `make dev` — builds and installs the binary, then replaces the
  plugin cache version directory with a symlink to the working tree.
  The original cache is preserved as `<version>.bak`.
- `make undev` — removes the symlink and restores the original cache
  from backup.

```bash
# Enter dev mode: binary installed, plugin cache → working tree
make dev

# Exit dev mode: restore original cache
make undev
```

This makes all prompt files (commands, hooks, skills, agents) live-
editable during development. Combined with `make install` for binary
changes, the full development loop is:

1. Edit Go code → `make dev` (rebuilds binary + ensures symlink)
2. Edit prompt files → restart Claude (no build needed, symlink is live)
3. Edit MCP tools → `make dev` + restart Claude

### Scope

This pattern applies to any Claude Code plugin that has a compiled
binary alongside prompt files. It is not ethos-specific — biff, vox,
quarry, lux, and z-spec all have the same two-speed problem.

### Version resolution

The symlink uses the latest version directory found in the cache
(`ls -1 | sort -V | tail -1`). This matches the version Claude Code
resolved from the marketplace registry. No synthetic "dev" version is
used — Claude Code would not look for a version the registry doesn't
advertise.

### Rejected alternatives

- **`--plugin-dir .`** — ephemeral (one session), must be passed every
  time. `make dev` persists until `make undev`.
- **Version bump to `n+1`** — Claude Code resolves versions from the
  marketplace registry. A version the registry doesn't know about would
  not be loaded.
- **Copy files into cache on every build** — fragile, easy to forget,
  and creates drift between source and cache. Symlink is atomic.
- **Use `dev` as the version string** — same problem as `n+1`: Claude
  Code won't look for it.
