# Ethos-Biff Integration Design

Biff is a team communication tool. Ethos is an identity service. Today
biff resolves identity from `gh api user` and team membership from a
static `.biff` TOML file. This design replaces both with ethos, giving
biff richer identity (display name, role, talents) and dynamic team
resolution (membership from the collaboration graph, not a hardcoded
list).

Ethos is enrichment, not dependency. When ethos is absent, biff falls
back to its current behavior. No ethos code changes are required for
layers 1-3.

## Decision: CLI, Not Filesystem

Biff calls `ethos whoami --json`, not reads YAML files directly.

**Rationale.** Ethos resolves identity through a chain (repo-local,
global, OS fallback) with attribute content expansion. Reading the YAML
file gets the raw slugs; calling the CLI gets the resolved result. The
CLI also handles version evolution -- if the schema changes, the JSON
output contract is maintained while the file format may shift.

**Trade-off.** CLI adds ~20ms subprocess overhead at startup. This is
acceptable: biff starts once per session and caches the result. The
alternative (importing ethos as a library) is impossible -- ethos is Go,
biff is Python.

## Fallback Chain

```text
ethos whoami --json  -->  gh api user  -->  OS username  -->  error
```

Each step is tried only if the previous one fails (exit code != 0 or
binary not found). The chain is evaluated once at biff server startup.
The result is cached for the session lifetime.

| Step | Provides | Exit behavior |
|------|----------|---------------|
| `ethos whoami --json` | handle, name, email, github, talents, role (via team) | exit 0 + JSON on stdout |
| `gh api user` | login, display name | exit 0 + TSV on stdout |
| `getpass.getuser()` | OS username | returns string or raises |
| (none) | -- | `SystemExit("No user configured...")` |

Error codes from ethos:

- Exit 0: success, JSON on stdout
- Exit 1: no identity found (not in a repo with identities, no global identity)
- Exit 2: usage error (bad flags)

Biff treats any non-zero exit from ethos as "ethos unavailable" and
falls to the next step. Stderr output from ethos is discarded.

---

## Layer 1: Identity Resolution

### What Changes on the Biff Side

`config.py` gains a new function `get_ethos_identity()` that runs
`ethos whoami --json` and parses the result. The existing
`get_github_identity()` becomes the second fallback. The `load_config()`
function tries ethos first.

```python
@dataclass(frozen=True)
class EthosIdentity:
    handle: str
    name: str
    github: str
    email: str

def get_ethos_identity() -> EthosIdentity | None:
    """Resolve identity from ethos. Returns None if ethos is absent or fails."""
    try:
        result = subprocess.run(
            ["ethos", "whoami", "--json"],
            capture_output=True, text=True, check=False, timeout=5,
        )
        if result.returncode != 0:
            return None
        data = json.loads(result.stdout)
        handle = data.get("handle", "")
        if not handle:
            return None
        return EthosIdentity(
            handle=handle,
            name=data.get("name", ""),
            github=data.get("github", ""),
            email=data.get("email", ""),
        )
    except (FileNotFoundError, json.JSONDecodeError, TimeoutError):
        return None
```

The resolution order in `load_config()` becomes:

```python
# 1. CLI override
if user_override is not None:
    user = user_override
# 2. Ethos
elif (ethos := get_ethos_identity()) is not None:
    user = ethos.github or ethos.handle  # biff uses github login as user
    display_name = ethos.name
# 3. GitHub CLI
elif (gh := get_github_identity()) is not None:
    user = gh.login
    display_name = gh.display_name
# 4. OS username
else:
    user = get_os_user()
```

### Exact JSON Response Shape

`ethos whoami --json` returns:

```json
{
  "name": "Claude Agento",
  "handle": "claude",
  "kind": "agent",
  "email": "claude@punt-labs.com",
  "github": "claude-puntlabs",
  "writing_style": "direct-with-quips",
  "personality": "friendly-direct",
  "talents": ["management", "engineering", "product-development", "operations"],
  "writing_style_content": "...",
  "personality_content": "...",
  "talent_contents": ["...", "..."]
}
```

Biff reads: `handle`, `name`, `github`, `email`. The `*_content` fields
and `talents` list are ignored in layer 1 (used in layer 3).

### Error Handling

| Condition | Behavior |
|-----------|----------|
| `ethos` not on PATH | `FileNotFoundError` caught, returns `None`, falls to `gh` |
| ethos exits non-zero | `result.returncode != 0`, returns `None`, falls to `gh` |
| ethos returns invalid JSON | `json.JSONDecodeError` caught, returns `None`, falls to `gh` |
| ethos returns JSON without `handle` | empty string check, returns `None`, falls to `gh` |
| ethos hangs | 5-second timeout, `TimeoutError` caught, returns `None`, falls to `gh` |

### User-Visible Before/After

**Before (no ethos):**

```text
$ /who
NAME           REPO              IDLE  S  P  HOST
@claude-...    punt-labs/ethos   0m    +  +  mac-mini

$ /finger @claude-puntlabs
Login: claude-puntlabs              Name: Claude Agento
```

The `user` field is the GitHub login (`claude-puntlabs`). The display
name comes from GitHub's API (`name` field).

**After (with ethos):**

```text
$ /who
NAME           REPO              IDLE  S  P  HOST
@claude-...    punt-labs/ethos   0m    +  +  mac-mini

$ /finger @claude-puntlabs
Login: claude-puntlabs              Name: Claude Agento
```

Layer 1 alone produces the same visible output because biff already uses
`github` login as the user key and `name` as display name. The value
comes from ethos instead of `gh api user`, but the output is identical.

The difference is operational: ethos resolves in ~20ms (local binary,
no network). `gh api user` requires a GitHub API call (~200-500ms, can
fail if rate-limited or offline). Identity resolution is faster and
more reliable.

---

## Layer 2: Team from Ethos

### What Changes on the Biff Side

The `.biff` `[team]` section becomes optional. When ethos is available,
biff calls `ethos team for-repo --json` to discover team membership for
the current repository. The static member list in `.biff` is the
fallback.

A new function in `config.py`:

```python
def get_ethos_team_members(repo_slug: str | None = None) -> list[str] | None:
    """Resolve team members from ethos. Returns None if unavailable."""
    cmd = ["ethos", "team", "for-repo", "--json"]
    if repo_slug:
        cmd.insert(3, repo_slug)  # ethos team for-repo <repo> --json
    try:
        result = subprocess.run(
            cmd, capture_output=True, text=True, check=False, timeout=5,
        )
        if result.returncode != 0:
            return None
        teams = json.loads(result.stdout)
        if not isinstance(teams, list) or not teams:
            return None
        # Collect unique member handles across all matching teams
        members: list[str] = []
        seen: set[str] = set()
        for team in teams:
            for member in team.get("members", []):
                ident = member.get("identity", "")
                if ident and ident not in seen:
                    members.append(ident)
                    seen.add(ident)
        return members if members else None
    except (FileNotFoundError, json.JSONDecodeError, TimeoutError):
        return None
```

In `load_config()`, after resolving the repo root:

```python
# Team: ethos > .biff [team]
ethos_members = get_ethos_team_members()
if ethos_members is not None:
    team = tuple(ethos_members)
else:
    # Existing .biff [team] parsing
    team, relay_url, relay_auth, peers, orgs = extract_biff_fields(raw)
```

### Exact JSON Response Shape

`ethos team for-repo --json` (no argument = current repo's git remote):

```json
[
  {
    "name": "engineering",
    "repositories": [
      "punt-labs/ethos",
      "punt-labs/biff",
      "punt-labs/quarry"
    ],
    "members": [
      {"identity": "jfreeman", "role": "ceo"},
      {"identity": "claude", "role": "coo"},
      {"identity": "bwk", "role": "go-specialist"},
      {"identity": "mdm", "role": "cli-specialist"},
      {"identity": "djb", "role": "security-engineer"},
      {"identity": "adb", "role": "infra-engineer"},
      {"identity": "rmh", "role": "python-specialist"},
      {"identity": "kwb", "role": "smalltalk-specialist"}
    ],
    "collaborations": [
      {"from": "coo", "to": "ceo", "type": "reports_to"},
      {"from": "go-specialist", "to": "coo", "type": "reports_to"}
    ]
  }
]
```

The response is an array because a repo can belong to multiple teams.
Biff flattens all members across teams into a single roster.

`ethos team for-repo` resolves by git remote. It runs
`git remote get-url origin`, parses the slug (`owner/repo`), and matches
against the `repositories` array in each team definition. No path-based
heuristics.

When no team covers the repo, ethos exits 0 with an empty array `[]`.
Biff treats this the same as "ethos unavailable" and falls back to
`.biff` `[team]`.

### Error Handling

| Condition | Behavior |
|-----------|----------|
| ethos not on PATH | Falls to `.biff` `[team]` |
| No team covers repo | Empty array `[]`, falls to `.biff` `[team]` |
| ethos exits non-zero | Falls to `.biff` `[team]` |
| `.biff` has no `[team]` | Empty tuple, solo mode |

### User-Visible Before/After

**Before:** The operator maintains `.biff` by hand:

```toml
# .biff
[team]
members = ["jmf-pobox", "github-actions"]
```

Adding a new team member requires editing `.biff` in every repo, then
committing and pushing. The member list is a flat array of strings with
no role or identity metadata.

**After:** Team membership is declared once in ethos
(`.punt-labs/ethos/teams/engineering.yaml`) and discovered automatically:

```toml
# .biff — [team] section is optional, kept as fallback
[relay]
url = "tls://connect.ngs.global"
```

The operator adds a member to the ethos team definition. Every biff
instance in every repo covered by that team picks up the change on next
startup. No per-repo edits.

The `[team]` section in `.biff` is still respected as a fallback. Repos
that do not have ethos configured continue to work exactly as before.

### Migration Path

1. Ensure ethos is installed and the team submodule is current.
2. Remove `[team]` from `.biff` (or leave it as fallback).
3. Biff discovers members from ethos on next startup.

No flag day. Both paths coexist.

---

## Layer 3: Collaboration Protocol

### What Changes on the Biff Side

Biff gains awareness of the collaboration graph. Today, `/who` shows a
flat list of sessions. With the collaboration graph from ethos, biff can
annotate sessions with role and reporting relationship.

The collaboration data comes from the same `ethos team for-repo --json`
call used in layer 2. The `collaborations` array describes directed
edges in the team graph.

A new function parses the graph:

```python
@dataclass(frozen=True)
class TeamMember:
    identity: str
    role: str

@dataclass(frozen=True)
class Collaboration:
    from_role: str
    to_role: str
    type: str  # "reports_to", "collaborates_with", etc.

@dataclass(frozen=True)
class TeamContext:
    members: list[TeamMember]
    collaborations: list[Collaboration]

def parse_team_context(team_json: list[dict]) -> TeamContext | None:
    """Parse ethos team JSON into structured context."""
    if not team_json:
        return None
    members = []
    collabs = []
    seen = set()
    for team in team_json:
        for m in team.get("members", []):
            ident = m.get("identity", "")
            role = m.get("role", "")
            if ident and ident not in seen:
                members.append(TeamMember(identity=ident, role=role))
                seen.add(ident)
        for c in team.get("collaborations", []):
            collabs.append(Collaboration(
                from_role=c.get("from", ""),
                to_role=c.get("to", ""),
                type=c.get("type", ""),
            ))
    return TeamContext(members=members, collaborations=collabs)
```

### How It Informs Communication Behavior

The collaboration graph enables three behaviors:

**1. Role display in `/finger`.** When a user runs `/finger @bwk`, biff
can show the role from the team context:

```text
Login: bwk                          Name: Brian Kernighan
Role: go-specialist
On since Sat Apr 12 09:30 (UTC) on tty50, idle 0:03
Plan: implementing ws transport for beadle-email
```

The role comes from the team member entry, not from a separate ethos
call. It is available as soon as layer 2 resolves the team.

**2. Reporting chain in `/finger`.** For agents, the collaboration graph
shows who they report to:

```text
Login: bwk                          Name: Brian Kernighan
Role: go-specialist (reports to: coo)
```

This uses the `collaborations` array: find the edge where
`from == member.role` and `type == "reports_to"`, then display the `to`
role.

**3. Presence filtering.** When the collaboration graph is available,
`/who` can optionally group by reporting chain or filter to direct
reports. This is a future enhancement, not required for initial
implementation.

### Exact JSON Fields Used

From `ethos team for-repo --json`, the `collaborations` array:

```json
[
  {"from": "coo", "to": "ceo", "type": "reports_to"},
  {"from": "go-specialist", "to": "coo", "type": "reports_to"},
  {"from": "cli-specialist", "to": "coo", "type": "reports_to"},
  {"from": "security-engineer", "to": "coo", "type": "reports_to"},
  {"from": "infra-engineer", "to": "coo", "type": "reports_to"},
  {"from": "python-specialist", "to": "coo", "type": "reports_to"},
  {"from": "smalltalk-specialist", "to": "coo", "type": "reports_to"}
]
```

Edges use roles, not identities. To resolve "who does bwk report to":
find bwk's role (`go-specialist`), find the edge `from: go-specialist`,
read `to: coo`, find the member with role `coo` (`claude`).

### Error Handling

| Condition | Behavior |
|-----------|----------|
| No collaborations array | Omit role/reporting from `/finger` output |
| Member has no matching edge | Show role without reporting chain |
| Multiple teams, conflicting roles | First match wins (teams are ordered in the array) |

### User-Visible Before/After

**Before:**

```text
$ /finger @bwk
Login: bwk                          Name: Brian Kernighan
On since Sat Apr 12 09:30 (UTC) on tty50, idle 0:03
Plan: implementing ws transport
```

**After:**

```text
$ /finger @bwk
Login: bwk                          Name: Brian Kernighan
Role: go-specialist (reports to: coo)
On since Sat Apr 12 09:30 (UTC) on tty50, idle 0:03
Plan: implementing ws transport
```

The role line appears between the header and the tty block. When ethos
is unavailable, the role line is omitted and the output is identical to
the current format.

---

## Layer 4: Extensions

### What Changes on the Biff Side

Biff-specific per-identity configuration is stored in ethos extensions,
following the same pattern as vox and quarry.

Storage path:

```text
~/.punt-labs/ethos/identities/<handle>.ext/biff.yaml
```

### Extension Schema

Example `jfreeman.ext/biff.yaml`:

```yaml
notification_level: all
mute_from: []
```

Example `claude.ext/biff.yaml`:

```yaml
notification_level: mentions
mute_from: []
auto_plan_source: ethos
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `notification_level` | string | `"all"` | `"all"`, `"mentions"`, `"none"` |
| `mute_from` | list[string] | `[]` | Handles whose messages are silently dropped |
| `auto_plan_source` | string | `"manual"` | Where `/plan` auto-detection reads from |

### CLI Operations

```bash
# Read biff extension
ethos ext get jfreeman biff --json
# {"notification_level": "all", "mute_from": []}

# Set a value
ethos ext set jfreeman biff notification_level mentions

# Read a single key
ethos ext get jfreeman biff notification_level
# mentions
```

### Exact JSON Response Shape

`ethos ext get <handle> biff --json`:

```json
{
  "notification_level": "all",
  "mute_from": [],
  "auto_plan_source": "manual"
}
```

When no biff extension exists:

```text
$ ethos ext get jfreeman biff --json
ethos: namespace "biff" not found for "jfreeman"
(exit 1)
```

### How Biff Reads Extensions

At server startup, after identity resolution:

```python
def get_biff_extension(handle: str) -> dict[str, object] | None:
    """Read biff extension from ethos. Returns None if unavailable."""
    try:
        result = subprocess.run(
            ["ethos", "ext", "get", handle, "biff", "--json"],
            capture_output=True, text=True, check=False, timeout=5,
        )
        if result.returncode != 0:
            return None
        return json.loads(result.stdout)
    except (FileNotFoundError, json.JSONDecodeError, TimeoutError):
        return None
```

### Error Handling

| Condition | Behavior |
|-----------|----------|
| No biff extension for this handle | Returns `None`, biff uses defaults |
| ethos not on PATH | Returns `None`, biff uses defaults |
| Extension has unknown keys | Ignored (forward compatibility) |
| Extension has wrong types | Use defaults for malformed keys |

### User-Visible Before/After

**Before:** No per-identity biff configuration exists. All users get
the same notification behavior.

**After:** An operator can configure per-identity notification
preferences:

```bash
# Mute CI bot messages for a human
ethos ext set jfreeman biff mute_from '["github-actions"]'

# Set agent to mentions-only
ethos ext set claude biff notification_level mentions
```

The extension is read once at startup. Changes take effect on the next
biff server restart (which happens on each Claude Code session start).

### Ethos Changes Required

None for the extension mechanism itself -- `ethos ext` already supports
arbitrary namespaces and key-value pairs. The `biff` namespace is
created on first `ethos ext set`.

Layer 4 requires biff-side implementation only. Ethos does not validate
biff-specific keys; biff owns the schema.

---

## Flow: Biff Server Startup

```text
biff serve
  |
  v
load_config()
  |
  +-- user_override provided? --> use it
  |
  +-- ethos available?
  |     |
  |     +-- ethos whoami --json (exit 0?)
  |     |     |
  |     |     +-- YES: user = github or handle
  |     |     |        display_name = name
  |     |     |        (cache EthosIdentity)
  |     |     |
  |     |     +-- NO: fall through
  |     |
  |     +-- ethos team for-repo --json (exit 0, non-empty array?)
  |     |     |
  |     |     +-- YES: team = flattened member handles
  |     |     |        team_context = parsed members + collaborations
  |     |     |
  |     |     +-- NO: fall through to .biff [team]
  |     |
  |     +-- ethos ext get <handle> biff --json (exit 0?)
  |           |
  |           +-- YES: apply extension config
  |           +-- NO: use defaults
  |
  +-- gh api user (exit 0?)
  |     |
  |     +-- YES: user = login, display_name = name
  |     +-- NO: fall through
  |
  +-- getpass.getuser()
  |     |
  |     +-- YES: user = os_user
  |     +-- NO: SystemExit
  |
  v
BiffConfig(user=..., display_name=..., team=..., ...)
ServerState(config=..., team_context=...)
```

All three ethos calls happen sequentially at startup. Total overhead
when ethos is available: ~60ms (3 subprocess calls at ~20ms each).
When ethos is absent: ~0ms for the `FileNotFoundError` catch, then
existing `gh api user` path (~200-500ms).

## Flow: `/finger @bwk`

```text
finger tool receives user="@bwk"
  |
  v
resolve sessions for bwk across visible repos
  |
  v
team_context available?
  |
  +-- YES: find member where identity == "bwk"
  |         role = "go-specialist"
  |         find collaboration where from == "go-specialist"
  |         reports_to = "coo"
  |         format: "Role: go-specialist (reports to: coo)"
  |
  +-- NO: omit role line
  |
  v
format_finger(session, role_line=...)
```

---

## What Ethos Does Not Need to Change

Layers 1-3 use existing ethos CLI commands with no modifications:

| Command | Layer | Status |
|---------|-------|--------|
| `ethos whoami --json` | 1 | Exists, ships identity JSON |
| `ethos team for-repo --json` | 2 | Exists, resolves by git remote |
| `ethos team show <name> --json` | 3 | Exists, includes collaborations |
| `ethos ext get <handle> biff --json` | 4 | Exists, generic extension mechanism |

The ethos CLI surface is sufficient. All implementation work is on the
biff side.

## Implementation Order

1. **Layer 1** -- identity resolution from ethos. Smallest change,
   biggest reliability win (no network call for identity).
2. **Layer 2** -- team from ethos. Replaces static `.biff` config.
3. **Layer 4** -- extensions. Per-identity configuration.
4. **Layer 3** -- collaboration protocol. Requires layers 1+2, adds
   role display to `/finger`.

Layer 4 before 3 because extensions are self-contained (one function,
read at startup) while the collaboration protocol touches formatting
code across multiple tools.
