# Build Plan: Identity Resolution — DES-011

## Goal

Replace the global active identity file with environment-based human
resolution and per-repo agent configuration. Human identity comes from
git/OS, agent identity comes from repo config. The session start hook
binds both at startup with distinct personas.

## Current State

```text
Human resolution:
  ethos whoami → reads ~/.punt-labs/ethos/active → returns stored handle

Agent resolution:
  (none — session start reuses the human persona for both root and primary agent)

Session start hook:
  ACTIVE_PERSONA = ethos whoami --json | extract handle
  root-persona   = $ACTIVE_PERSONA
  primary-persona = $ACTIVE_PERSONA    # same persona for both
```

Problems:

1. `~/.punt-labs/ethos/active` is a manual declaration. It doesn't know
   who is actually at the keyboard.
2. Repos are multi-user. A tracked `active:` field in repo config pins
   the identity to one person — wrong for shared repos.
3. Human and agent get the same persona in the session roster.

## Target State

```text
whoami resolution chain (stops at first match):
  1. iam declaration        → PID-keyed file from ethos iam
  2. git config user.name   → identity where github field matches
  3. git config user.email  → identity where email field matches
  4. $USER                  → identity where handle matches
  5. no match               → no persona (raw $USER as display name)

Agent resolution:
  1. .punt-labs/ethos/config.yaml "agent:" field → identity by handle
  2. not set → no persona (undefined)

Session start hook:
  # human resolves via steps 2-4 (no iam yet), then iam binds it
  HUMAN_PERSONA  = ethos whoami --json | extract handle
  ethos iam $HUMAN_PERSONA       # writes PID-keyed file for human
  AGENT_PERSONA  = ethos resolve-agent
  ethos iam $AGENT_PERSONA       # writes PID-keyed file for agent
  # subsequent whoami calls hit step 1 (iam declaration)

Interactive shell:
  $ ethos iam jfreeman           # writes to current/$$
  $ ethos whoami                 # finds PID file → jfreeman
  # exits shell → PID stale → session purge cleans up
```

### Repo Config Format

```yaml
# .punt-labs/ethos/config.yaml (tracked in git)
agent: claude
```

Only agent-scoped fields. No `active:` field — the human is resolved
from git/OS, not repo config.

### Example Resolution

```text
Machine state:
  git config user.name  = jmf-pobox
  git config user.email = jmf@pobox.com
  $USER                 = jfreeman

Identity store:
  jfreeman.yaml:  handle=jfreeman, github=jmf-pobox, email=jim@punt-labs.com
  claude.yaml:    handle=claude, kind=agent

Resolution:
  Step 1: git user.name "jmf-pobox" → scan identities for github=="jmf-pobox"
          → match: jfreeman.yaml (stop here)

  If step 1 had no match:
  Step 2: git user.email "jmf@pobox.com" → scan for email=="jmf@pobox.com"
          → no match (jfreeman has email jim@punt-labs.com)
  Step 3: $USER "jfreeman" → scan for handle=="jfreeman"
          → match: jfreeman.yaml
```

## Removed Concepts

| Concept | Why removed |
|---------|-------------|
| `~/.punt-labs/ethos/active` file | Human identity comes from git/OS, not a manual pointer |
| `ethos whoami <handle>` (write path) | No "set active" operation needed |
| `active:` in repo config | Repos are multi-user; human identity is per-user |
| `Store.Active()` method | Replaced by `Store.FindBy()` + resolution chain |
| `Store.SetActive()` method | No global active file to write |
| `ErrNoActive` sentinel | No global active concept |
| `resolve.Resolve()` repo-local chain | Replaced by environment-based resolution |

## Implementation Sequence

### Phase 1: Identity Store — FindBy Method

**Files**: `internal/identity/store.go`, `internal/identity/store_test.go`

Add `FindBy(field, value)` to the identity store. Scans all identity
YAML files and returns the first identity where the given field matches
the given value. Uses reference mode (no attribute content resolution)
for the scan.

```go
// FindBy searches for an identity where the named field matches value.
// Supported fields: "handle", "email", "github".
// Returns nil, nil when no identity matches (not an error).
func (s *Store) FindBy(field, value string) (*Identity, error)
```

Implementation: call `List()` (already does a reference-mode scan),
iterate, compare the requested field. Return first match.

**Tests**:

- FindBy "github" matches identity with that github field
- FindBy "email" matches identity with that email field
- FindBy "handle" matches identity with that handle (equivalent to Load)
- FindBy with no match returns nil, nil
- FindBy with unsupported field returns error
- FindBy with empty value returns nil, nil

### Phase 2: Resolve Package — ResolveHuman and ResolveAgent

**Files**: `internal/resolve/resolve.go`, `internal/resolve/resolve_test.go`

Rewrite the resolve package. Four public functions:

```go
// Resolve returns the identity handle for the current caller.
// Resolution chain:
//   1. iam declaration (PID-keyed file, walk process tree)
//   2. git config user.name → github field
//   3. git config user.email → email field
//   4. $USER → handle
// Returns ("", error) when no step matches.
func Resolve(store *identity.Store, sessionStore *session.Store) (string, error)

// ResolveAgent returns the default agent identity handle for the repo.
// Reads .punt-labs/ethos/config.yaml "agent:" field. Returns empty
// string if not configured (not an error).
func ResolveAgent(repoRoot string) string

// FindRepoRoot walks CWD upward for .git. Returns empty string if not
// in a git repo (not an error — agent resolution degrades to undefined).
func FindRepoRoot() string

// GitConfig reads a git config value. Returns empty string if git is
// not installed or the key is not set.
func GitConfig(key string) string
```

**Signature changes from current code:**

- `Resolve` replaces the old `Resolve(repoRoot)`. New signature takes
  the identity store and session store. The session store is needed to
  check for `iam` declarations (step 1).
- `ResolveAgent` returns `string` (not `(string, error)`). Not
  configured is a normal state, not an error.
- `FindRepoRoot` returns `string` (not `(string, error)`). Not in a
  repo is a normal state, not an error.
- Both callers of the old `FindRepoRoot` (`cmd/ethos/main.go` and
  `internal/mcp/tools.go`) must be updated from two-return to
  one-return in this phase to maintain compile safety.

**Step 1 — iam declaration**: `Resolve` walks the process tree upward
(using `internal/process`) and checks for
`~/.punt-labs/ethos/sessions/current/<PID>` at each ancestor. This is
the same mechanism `resolveSessionID` in `internal/mcp/tools.go`
already uses. If found, reads the session roster and returns the
participant's persona for that PID.

**`Resolve` no-match semantics**: Returns an error. The caller
(`runWhoami`, MCP handler) decides how to present it — the CLI prints
an informational message, the MCP handler returns an error result. The
error message includes the sources tried:
`no identity matches git user "jmf-pobox", email "jmf@pobox.com", or OS user "jfreeman"`.

**`Resolve` needs the identity store** to do FindBy lookups (steps
2–4) and the session store to check iam declarations (step 1). It
shells out to `git config` to get user.name and user.email.

**Git config reading**: Use `exec.Command("git", "config", "user.name")`
and `exec.Command("git", "config", "user.email")`. These are fast
(<10ms), available everywhere git is installed, and respect the full git
config cascade (system → global → local → worktree). Git not being
installed is not an error — skip to `$USER` step.

**Repo config loading**: `ResolveAgent` reads
`.punt-labs/ethos/config.yaml` from the given repo root. The
`RepoConfig` struct has only `Agent string` — no `Active` field.

**Tests**:

- Resolve: iam declaration found via PID file → returns that persona
- Resolve: no iam, git user.name matches github field
- Resolve: no iam, git user.name no match, email matches email field
- Resolve: no iam, git no match, $USER matches handle
- Resolve: no match at all returns error with all sources listed
- ResolveAgent: config.yaml with agent field returns handle
- ResolveAgent: no config returns empty string
- ResolveAgent: config without agent field returns empty string
- FindRepoRoot: finds .git in parent
- FindRepoRoot: no .git returns empty string

**Testing git config**: Tests set all three isolation env vars to
prevent leaking real user config:

- `GIT_CONFIG_GLOBAL=<tempfile>` — test-controlled global config
- `GIT_CONFIG_SYSTEM=/dev/null` — suppress system config
- `GIT_CONFIG_NOSYSTEM=1` — belt-and-suspenders for system config

Write a temp `.gitconfig` with test values. Tests that exercise the
"git not available" path can set `PATH` to exclude git.

### Phase 3: CLI — Rewrite whoami, list, create, doctor

**Files**: `cmd/ethos/main.go`, `cmd/ethos/create.go`

This phase removes all calls to `Store.Active()` and
`Store.SetActive()` from the CLI before Phase 5 deletes those methods.
Every caller is listed below.

#### whoami (read-only)

Remove the write path (`ethos whoami <handle>`). `ethos whoami` becomes
read-only — it runs `Resolve` and displays the result.

```text
$ ethos iam jfreeman
$ ethos whoami
Jim Freeman (jfreeman)           # step 1: iam declaration

$ ethos whoami                   # no iam, but git matches
Jim Freeman (jfreeman)           # step 2: git user.name → github

$ ethos whoami --json
{ "name": "Jim Freeman", "handle": "jfreeman", ... }

$ ethos whoami                   # when no step matches
ethos: no identity matches git user "jmf-pobox" or OS user "jfreeman"
```

The same resolution chain runs everywhere — CLI, MCP tool, hook. After
`iam` has been called (by the hook at session start, or by the user in
a shell), step 1 returns immediately. Before `iam`, steps 2–4 provide
automatic resolution.

#### list

`runList` currently calls `s.Active()` to mark one identity with `*`.
Replace this with session-aware marking: load the current session
roster and mark every identity that appears as a participant. Multiple
`*` markers are normal — a session has a human, a primary agent, and
potentially sub-agents, all active simultaneously.

When not in a session (no roster found), no identities are marked.

```text
$ ethos list
* jfreeman         Jim Freeman       # human root
* claude           Claude Agento     # primary agent
  marty-mcfly      Marty McFly
```

#### create

`cmd/ethos/create.go` has two calls to `Store.SetActive()`:

1. `setActiveIfFirst()` — sets the first created identity as active.
2. `createInteractive()` — sets the newly created identity as active.

Remove both. Identity creation no longer implies activation. The
identity becomes reachable via the resolution chain if the user's
git/OS matches its fields.

#### doctor

Replace "Active identity" check with two checks:

```text
$ ethos doctor
  Identity directory       PASS  /Users/jfreeman/.punt-labs/ethos/identities
  Human identity           PASS  Jim Freeman (jfreeman) via git:github
  Default agent            PASS  claude
  Duplicate fields         PASS  no duplicates
```

- **Human identity**: runs `ResolveHuman`, shows which step matched
  ("via git:github", "via git:email", "via os:handle"). FAIL if no
  match.
- **Default agent**: reads repo config. Shows handle or "not configured"
  (PASS either way — not having an agent is valid).
- **Duplicate fields**: scans all identities for duplicate `github` or
  `email` values. WARN if found — duplicates make resolution
  nondeterministic (first match depends on directory order, which
  varies by OS).

**Remove**:

- `checkActiveIdentity()` function
- All `Store.Active()` calls in `main.go` and `create.go`
- All `Store.SetActive()` calls in `create.go`

### Phase 4: Session Start Hook — Separate Human and Agent

**Files**: `hooks/session-start.sh`

Update the hook to resolve human and agent separately. The hook calls
`ethos` CLI commands — single source of truth, not raw YAML parsing.

```bash
# Human resolution (uses new ResolveHuman chain)
HUMAN_PERSONA=""
if command -v ethos >/dev/null 2>&1; then
  HUMAN_INFO=$(ethos whoami 2>>"$ETHOS_LOG" || true)
  HUMAN_PERSONA=$(ethos whoami --json 2>>"$ETHOS_LOG" \
    | grep -o '"handle" *: *"[^"]*"' | head -1 | cut -d'"' -f4 || true)
fi

# Agent resolution (new CLI command)
AGENT_PERSONA=""
if command -v ethos >/dev/null 2>&1; then
  AGENT_PERSONA=$(ethos resolve-agent 2>>"$ETHOS_LOG" || true)
fi

# Session creation — human and agent get distinct personas
ethos session create \
  --session "$SESSION_ID" \
  --root-id "$USER_ID" \
  --root-persona "${HUMAN_PERSONA:-$USER_ID}" \
  --primary-id "$CLAUDE_PID" \
  --primary-persona "$AGENT_PERSONA" \
  ...
```

**New CLI command: `ethos resolve-agent`**. Prints the agent handle
from repo config to stdout (empty if not configured). This is a thin
wrapper around `ResolveAgent(FindRepoRoot())` that the hook calls. No
`--json` needed — it's a single string value.

Note: when `AGENT_PERSONA` is empty (no repo config), the primary
agent has no persona. This is correct — an unnamed agent is more honest
than one mislabeled with the human's persona.

### Phase 5: MCP Tools — Update whoami and list_identities

**Files**: `internal/mcp/tools.go`

#### whoami tool

The MCP `whoami` tool uses the same `Resolve` function as the CLI.
Since the SessionStart hook calls `iam` for the primary agent, step 1
of the resolution chain (PID-keyed file) returns the agent's persona
immediately. No special MCP-specific logic needed.

Remove the `agent` boolean parameter — it's unnecessary. The agent
already *is* whoever `iam` set it to be. The `handle` parameter (write
path) is also removed — `whoami` is read-only.

Update the tool description from "Show or set the active identity" to
"Show the caller's identity."

#### list_identities tool

Currently calls `h.store.Active()` to set `"active": true` on one
entry. Replace with session-aware marking: load the session roster and
mark every identity that appears as a participant's persona. Multiple
entries can be `"active": true`.

When not in a session, no entries are marked active.

#### create_identity tool

Currently calls `h.store.SetActive()` to auto-activate the first
identity. Remove this call. Creation does not imply activation.

### Phase 6: Store Cleanup

**Files**: `internal/identity/store.go`, `internal/identity/store_test.go`

All callers have been updated in Phases 3 and 5. Now delete:

- `Store.Active()` method
- `Store.SetActive()` method
- `ErrNoActive` sentinel error
- Tests for Active/SetActive
- `~/.punt-labs/ethos/active` references in installer (`install.sh`)

This phase is a pure deletion — if it doesn't compile, a caller was
missed (go back to Phase 3 or 5).

### Phase 7: Documentation

**Files**: `CHANGELOG.md`, `README.md`, `CLAUDE.md`, `DESIGN.md`,
`internal/seed/sidecar/README.md`

- **CHANGELOG**: document the breaking change (global active removed,
  `whoami` is read-only, human resolution from git/OS, new
  `resolve-agent` command)
- **README**: rewrite Per-Project Identity section, update storage
  layout (remove "Active identity" row), update Quick Start to remove
  `ethos whoami <handle>` setup step, add `resolve-agent` to command
  list
- **CLAUDE.md**: update storage layout table, update build commands
- **DESIGN.md DES-007**: update text at line 434–436 that still says
  "`whoami` manages the global active identity in the registry." Change
  to reflect that `whoami` runs the human resolution chain (CLI) or
  reads the session roster (MCP).
- **internal/seed/sidecar/README.md**: remove `active` file from the sidecar contract
  layout documentation

## Risks

1. **`git config` not available**: Containers or minimal environments
   may not have git. Mitigation: `$USER` fallback handles this case.
   `ResolveHuman` does not require git — it tries git first, falls back
   to OS.

2. **`git config user.name` is a display name, not a username**: Some
   users set `user.name` to "Jim Freeman" not "jmf-pobox". Step 1
   would fail to match, falling through to email or `$USER`. The chain
   is designed for graceful degradation.

3. **Breaking change: `ethos whoami <handle>` removed**: Any scripts
   calling `ethos whoami <handle>` will break. Mitigation: the only
   known caller is the user interactively. The install flow needs
   updating — instead of "run `ethos whoami jfreeman`", it becomes
   "ensure your git config user.name or `$USER` matches your ethos
   identity's `github` or `handle` field."

4. **Multiple identities match the same field**: Two identities could
   have the same email or github value. `FindBy` returns the first
   match (directory order, which varies by OS). This is a
   misconfiguration. `ethos doctor` warns about duplicate `github` and
   `email` values across identities.

5. **Performance of FindBy**: Linear scan of all identity YAML files.
   With <20 identities and reference mode (no attribute file reads),
   this is <5ms. Not a concern.

6. **`list_identities` MCP tool schema change**: The `active` boolean
   field changes from "one identity is active" to "multiple identities
   can be active (session participants)". Consumers keying on
   `"active": true` expecting exactly one result will see different
   behavior. This is a sidecar contract change — document in CHANGELOG.

7. **`FindBy` scan order is nondeterministic across OSes**: Linux
   `readdir` returns inode order, macOS returns alphabetical. Matters
   only when duplicate field values exist — which `doctor` warns about.
   Tests must not depend on scan order when multiple identities could
   match.

## Dependencies

- None. All changes are within the ethos repo.
- Consumers (Biff, Vox, Beadle) use the sidecar contract and are
  unaffected by how resolution works internally.
- The session-start hook is the integration point — it's part of ethos.
