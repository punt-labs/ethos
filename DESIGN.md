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
handle, writing style, skills, personality. Whether a human or LLM inhabits it
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

## DES-007: Session roster — multi-participant identity awareness (OPEN)

**Status**: Open. The registry layer (DES-001 through DES-006) is settled.
This decision concerns the session layer that sits on top of it.

**Problem**: Ethos currently tracks one "active" identity. In practice, a
session has multiple participants — a human, a primary agent, and potentially
many subagents. Any participant needs to be able to answer two questions:

1. **Who am I?** — my own identity
2. **Who is everyone else?** — the full roster of participants in this session

A subagent three levels deep needs to know the human's name (for a Biff
greeting), its parent agent's identity (for attribution), and its siblings
(for coordination). The human needs to know which agents are active. The
current single-slot `active` file cannot express this.

**What exists today**: The identity registry (`~/.punt-labs/ethos/identities/`)
stores all known identities. The `active` file names one. The resolution
chain (repo-local → global) resolves one handle. This is the foundation —
you need to know who exists before you can track who is present.

**What is missing**: A session-scoped roster that tracks all current
participants, their roles (human, agent), their relationships (who spawned
whom), and provides a query interface so any participant can discover the
others.

**Open questions**:

- **Propagation**: How does session state reach subagents? Environment
  variables survive process boundaries. Files are durable but require
  coordination. Claude Code's agent lifecycle and hook model constrain
  what is possible.
- **Lifecycle**: Who adds participants to the roster? The session-start
  hook can register the primary agent. But subagents are spawned
  dynamically — does the parent register them, or do they self-register?
- **Scope**: Is the roster per-terminal-session, per-repo, or per-machine?
  Multiple concurrent sessions (e.g., two worktrees) may have different
  rosters.
- **Query interface**: `ethos whoami` returns "me." What returns "everyone"?
  A new command (`ethos session`)? A flag (`ethos whoami --session`)?
  An MCP tool (`session_roster`)?
- **Ephemeral vs durable**: The registry is durable (YAML files). The
  session roster is ephemeral (lives only while participants are active).
  Where does ephemeral state live? A file that gets cleaned up? A socket?

**Constraints**:

- Must work without a daemon — ethos is a CLI tool, not a server.
- Must survive subagent spawning — new processes need access.
- Must not require ethos as a dependency — the sidecar contract (DES-001)
  must hold. Other tools read known paths, not import ethos.
- Must handle concurrent sessions on the same machine.

**Not yet decided**: Architecture, storage mechanism, API surface. This
ADR will be updated as the design matures.
