# Build Plan: Repo-Scoped Identity Configuration

> **HISTORICAL — SHIPPED**: This build plan is preserved for reference.
> It describes pre-Phase-1 design work for `ethos-uld` (repo-scoped
> identity configuration). That feature shipped and is documented in
> [`architecture.tex`](architecture.tex) under Identity Resolution
> and in the ADR archive at [`../DESIGN.md`](../DESIGN.md).
>
> **Current build planning lives in
> [`ETHOS-ROADMAP.md`](ETHOS-ROADMAP.md)**. Do not use this file as a
> source of truth for current priorities.

---

Bead: ethos-uld. Status: design reviewed by architect, revised.

## Problem

All identity data (identities, talents, personalities, writing styles) lives
in `~/.punt-labs/ethos/` — a user-global, untracked location. This means:

1. **Team identity is not shared.** The claude agent identity, its personality,
   writing style, and talents are defined on one machine and invisible to other
   team members or CI.
2. **Identity definitions are not version-controlled.** Changes to personality
   or talent descriptions are untracked and unreviewable.
3. **Reinstalling ethos loses all identity config.** The install script creates
   empty directories but no definitions.
4. **Different repos can't have different teams.** The ethos team is different
   from the biff team, but both read from the same global directory.

## Design

### Core insight: voice is an extension, not a core field

The `voice` field in identity YAML is a tool-scoped binding — identical in
nature to `ext/beadle` (GPG keys) or `ext/biff` (TTY sessions). Moving
voice to `ext/vox` eliminates the PII overlay mechanism entirely:

- Identity YAML becomes pure team knowledge — every field is safe to commit
- Extensions are always user-global — they contain credentials and
  machine-specific state
- No field splitting on Save, no overlay merge on Load
- LayeredStore is simpler: load from repo or global, no per-field merging

### Layered resolution: repo-local → user-global

| Layer | Location | Git-tracked | Contains |
|-------|----------|-------------|----------|
| Repo-local | `.punt-labs/ethos/` | Yes | Identities, talents, personalities, writing styles, config |
| User-global | `~/.punt-labs/ethos/` | No | Extensions (PII/credentials), sessions, fallback identities |

### Resolution rules

**Identity Load (by handle):**

1. Look in `.punt-labs/ethos/identities/<handle>.yaml` (repo-local)
2. If not found, look in `~/.punt-labs/ethos/identities/<handle>.yaml` (user-global)
3. Extensions always loaded from user-global regardless of identity layer

No overlay. No field merging. The identity YAML is atomic — whichever
layer has it owns it entirely.

**Attribute Load (talent, personality, writing style):**

1. Look in `.punt-labs/ethos/<kind>/<slug>.md` (repo-local)
2. If not found, look in `~/.punt-labs/ethos/<kind>/<slug>.md` (user-global)

**Identity List:**

1. List from repo-local
2. List from user-global
3. Deduplicate by handle (repo-local wins)

**Identity Save/Update:**

- Default write target: repo-local (if available)
- `--global` flag writes to user-global (for personal-only identities)
- Extension writes (`ext set`) always go to user-global
- No field splitting — the entire identity goes to one layer

**Identity Update:**

- Determine which layer owns the identity (`Exists` check on repo first)
- Lock and write within the owning layer

### What's in repo-local (all safe to commit)

| Field | Reason |
|-------|--------|
| `name`, `handle`, `kind` | Team identity |
| `email` | Used by resolve chain across machines |
| `github` | Public, used for attribution |
| `personality`, `writing_style`, `talents` | Team behavior definitions |
| `agent` | Claude Code agent binding |

### What's always user-global (never in repo)

| Data | Reason |
|------|--------|
| `ext/*` | Tool-scoped credentials (GPG keys, voice provider, TTY sessions) |
| `sessions/` | Ephemeral session state |

### Voice migration: core field → extension

The `voice` field moves from identity YAML to `ext/vox`:

**Before:**

```yaml
# ~/.punt-labs/ethos/identities/claude.yaml
name: Claude Agento
voice:
  provider: elevenlabs
  voice_id: helmut
```

**After:**

```yaml
# .punt-labs/ethos/identities/claude.yaml (repo-local, git-tracked)
name: Claude Agento
# no voice field

# ~/.punt-labs/ethos/identities/claude.ext/vox.yaml (user-global)
provider: elevenlabs
voice_id: helmut
```

Consumers (Vox) read voice config from `ext/vox` instead of the core
identity fields. The MCP `identity` tool's `whoami`/`get` responses
include ext data, so Vox gets the voice binding through the same channel.

### Storage layout after migration

```text
# Repo-local (git-tracked)
.punt-labs/ethos/
  .gitignore                           # excludes identities/*.ext/
  config.yaml                          # existing: agent
  identities/
    claude.yaml                        # all fields except voice (no PII)
    jfreeman.yaml                      # same
  talents/
    engineering.md
    management.md
    product-development.md
    operations.md
    product-management.md
    go-engineering.md
  personalities/
    friendly-direct.md
    principal-engineer.md
  writing-styles/
    direct-with-quips.md
    concise-quantified.md

# User-global (untracked)
~/.punt-labs/ethos/
  identities/claude.ext/
    beadle.yaml                        # GPG keys
    vox.yaml                           # provider, voice_id
  identities/jfreeman.ext/
    beadle.yaml
    vox.yaml
  sessions/                            # unchanged
  active                               # unchanged
```

## Changes

### Step 0: Move voice to ext/vox

**Files**: `internal/identity/identity.go`, `internal/identity/store.go`,
`internal/mcp/tools.go`, `cmd/ethos/main.go`

Remove the `Voice` field from the `Identity` struct. Add a migration path:
on Load, if `voice` field exists in YAML, auto-migrate to `ext/vox` and
strip from identity. Voice configuration is now stored exclusively under
`ext/vox` and must be managed by clients or follow-on tooling rather than
as core identity fields or MCP `identity create` arguments.

**Breaking change**: `voice` field removed from identity YAML schema.
Consumers must read from `ext/vox`. Only consumer is Vox — update its
sidecar reader.

**Verification**: `ethos show claude` still shows voice data (from ext).
`ethos whoami` includes ext in response.

### Step 1: Factor out `FindRepoEthosRoot()`

**Files**: `internal/resolve/resolve.go`

Extract repo ethos root discovery from `ResolveAgent` into a reusable
function. Returns `<repo-root>/.punt-labs/ethos` if the directory exists,
empty string otherwise.

**Verification**: Existing tests pass. `ResolveAgent` refactored to use
the new helper.

### Step 2: Extract `IdentityStore` interface

**Files**: `internal/identity/interface.go` (new)

Define `type IdentityStore interface` covering: `Load`, `Save`, `List`,
`FindBy`, `Exists`, `Update`, `Root`, `ExtGet`, `ExtSet`, `ExtDel`,
`ExtList`, `IdentitiesDir`, `Path`.

Verify `*Store` implements it (compile check). Change:

- `resolve.Resolve` parameter: `*Store` → `IdentityStore`
- `mcp.Handler.store` field: `*Store` → `IdentityStore`
- `mcp.NewHandler` parameter: `*Store` → `IdentityStore`

Pure refactor — behavior unchanged.

**Verification**: `make check` passes. All callers compile.

### Step 3: Implement `LayeredStore`

**Files**: `internal/identity/layered.go`, `internal/identity/layered_test.go`

```go
type LayeredStore struct {
    repo   *Store  // .punt-labs/ethos/ (may be nil)
    global *Store  // ~/.punt-labs/ethos/
}
```

Implements `IdentityStore`. No overlay, no field merging:

- **Load**: try repo first, fall back to global. Resolve attributes from
  both layers (repo first, global fallback). Extensions always from global.
- **Save**: write to repo if available, global otherwise. No field splitting.
- **List**: merge repo + global, deduplicate by handle (repo wins)
- **FindBy**: search repo first, then global
- **Update**: `owningStore(handle)` determines which layer to lock/write
- **ValidateRefs**: check both repo and global attribute stores
- **Ext operations**: always delegate to global store

**Verification**: Table-driven tests with both layers populated, repo-only,
global-only scenarios. No PII overlay tests needed — eliminated by design.

### Step 4: Implement layered attribute resolution

**Files**: `internal/identity/layered.go` (extend Load)

When `LayeredStore.Load` resolves attributes and a file is not found in
the repo attribute store, retry against global attribute stores. Collect
warnings only when neither layer has the file.

**Verification**: Test that a talent in global resolves when identity is
in repo-local.

### Step 5: Wire `LayeredStore` into CLI and MCP

**Files**: `cmd/ethos/identity.go`, `cmd/ethos/serve.go`,
`cmd/ethos/attribute.go`

Replace `identity.DefaultStore()` with:

```go
func layeredStore() identity.IdentityStore {
    repoRoot := resolve.FindRepoEthosRoot()
    global := identity.DefaultGlobalStore()
    if repoRoot == "" {
        return global
    }
    return identity.NewLayeredStore(repoRoot, global)
}
```

For attribute stores, expose `RepoRoot()` and `GlobalRoot()` on
`LayeredStore` or accept pre-built attribute stores in `NewHandler`.

**Verification**: `ethos whoami` resolves from repo-local identity.
`ethos talent show` resolves from repo-local talent.

### Step 6: Update Save/Create for repo-local default

**Files**: `internal/identity/layered.go`, CLI create command

- Default write target: repo-local (if available)
- `--global` flag for user-global writes
- Extension writes always go to global
- No field splitting — voice is already an extension

**Verification**: `ethos create` writes to `.punt-labs/ethos/identities/`.

### Step 7: Migrate ethos repo identities

1. Create `.punt-labs/ethos/.gitignore` with `identities/*.ext/`
2. Copy identity YAMLs (without voice field) to `.punt-labs/ethos/identities/`
3. Copy all talent/personality/writing-style `.md` files to repo-local
4. Migrate voice fields to `ext/vox` in user-global (if not done in Step 0)
5. Verify `ethos whoami` and `ethos show claude` resolve correctly

### Step 8: Update documentation

**Files**: CLAUDE.md, README.md, AGENTS.md, DESIGN.md

- Add DES-018: Repo-scoped identity configuration
- Add DES-019: Voice as extension (voice field removed from schema)
- Update storage layout tables everywhere
- Update sidecar README
- Notify Vox of sidecar contract change

## Dependency Order

```text
Step 0 → Step 1 → Step 2 → Step 3 → Step 4 → Step 5 → Step 6 → Step 7 → Step 8
```

Step 0 (voice migration) can be shipped independently as a patch release
before the layered store work begins. This lets Vox adapt early.

## Architect Review Summary

Original review raised 7 concerns. Voice-as-extension eliminates 3 of them:

| # | Original Concern | Resolution |
|---|-----------------|------------|
| 1 | `LayeredStore` needs Go interface (blocking) | Step 2: extract `IdentityStore` interface |
| 2 | `ValidateRefs` checks wrong root (blocking) | Step 3: check both repo + global attribute stores |
| 3 | `email` misclassified as PII | Fixed: email is repo-local |
| 4 | `resolveAttributes` single-root | Step 4: retry failed attrs against global |
| 5 | `mcp.NewHandler` hardcodes `Root()` | Step 5: interface + layered construction |
| 6 | `Update` target ambiguity | Step 3: `owningStore(handle)` selects correct layer |
| 7 | `.gitignore` for `*.ext/` not enforced | Step 7: mandatory in migration |
| — | PII overlay mechanism complexity | **Eliminated**: voice is now ext, no overlay needed |
| — | Save field-splitting logic | **Eliminated**: no PII fields in identity YAML |
| — | Load merge logic | **Eliminated**: identity is atomic per layer |

## Commit Strategy

One commit per step. Step 0 can be a separate PR (schema change).
Steps 1-8 in a single PR.

## Risks

1. **Backward compatible.** Global fallback means existing installs work.
   Voice auto-migration on Load handles the schema transition gracefully.

2. **Breaking change for Vox.** Vox reads the `voice` field from identity
   YAML via the sidecar. After Step 0, it must read from `ext/vox`.
   Coordinate with Vox before shipping Step 0.

3. **No PII leakage possible.** Identity YAML has no PII fields after
   voice removal. Extensions are always user-global and `.gitignore`
   excludes `*.ext/` directories. Double protection.

4. **Sidecar contract.** Additive — repo-local is a new read path.
   User-global continues to work unchanged for tools not yet aware.
