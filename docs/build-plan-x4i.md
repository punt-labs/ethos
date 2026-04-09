# Build Plan: ethos-x4i

> **HISTORICAL — SHIPPED**: This build plan is preserved for reference.
> It describes the early implementation work for `ethos-x4i`
> (repo-level identity config and team resolution). That feature
> shipped and was superseded by the Phase 1 teams work
> ([`build-plan-teams.md`](build-plan-teams.md), also historical).
> The current design is documented in [`architecture.tex`](architecture.tex)
> under Identity Resolution and Teams and Roles.
>
> **Current build planning lives in
> [`ETHOS-ROADMAP.md`](ETHOS-ROADMAP.md)**. Do not use this file as a
> source of truth for current priorities.

---

Repo-level identity config and team resolution.

## Problem

`.punt-labs/ethos/config.yaml` lives inside `.punt-labs/ethos/`, which is
now a git submodule (pointing to `punt-labs/team`). Repo-specific config
(`agent: claude`) cannot live inside the submodule.

No way to query which team works on a given repo. Teams have
`Repositories []string` but no lookup method.

## Deliverables

1. Move repo config to `.punt-labs/ethos.yaml` (next to submodule dir).
2. Add `team` field to `RepoConfig`.
3. Add `FindByRepo` to team store.
4. CLI: `ethos team for-repo [repo]`.
5. MCP: `team` tool, method `for_repo`.

---

## Phase 1: Config path migration

Move `RepoConfig` read path from `.punt-labs/ethos/config.yaml` to
`.punt-labs/ethos.yaml`. Backward compatible: try new path first, fall
back to old path during transition.

### Changes

| File | Change |
|------|--------|
| `internal/resolve/resolve.go` | `ResolveAgent`: read `.punt-labs/ethos.yaml` first, fall back to `.punt-labs/ethos/config.yaml`. Extract helper `LoadRepoConfig(repoRoot) (*RepoConfig, error)` for reuse. |
| `internal/resolve/resolve_test.go` | Table-driven tests: new path only, old path only, both present (new wins), neither present. |
| `internal/hook/session_start.go` | Update comment on line 88 referencing old path. |
| `internal/hook/session_start_test.go` | Update `setupRepoWithAgent` to write `.punt-labs/ethos.yaml` (new path). Add a NEW `setupRepoWithAgentLegacy` helper that writes `.punt-labs/ethos/config.yaml` for the fallback test. Do not mutate the existing helper to serve both purposes. |
| `internal/doctor/doctor.go` | No code change; verify call site correct after `LoadRepoConfig` extraction. |

### Acceptance criteria

- `ResolveAgent` returns agent from `.punt-labs/ethos.yaml`.
- `ResolveAgent` returns agent from old `.punt-labs/ethos/config.yaml` when new file absent.
- New path wins when both exist.
- All 3 call sites compile and behave correctly: `cmd/ethos/main.go:runResolveAgent`, `internal/doctor/doctor.go:checkRepoConfig`, `internal/hook/session_start.go:RunSessionStart`.
- `make check` passes.

---

## Phase 2: Team field in RepoConfig

Add `Team string` to `RepoConfig`. New exported function
`ResolveTeam(repoRoot string) string` mirrors `ResolveAgent`.

### Changes

| File | Change |
|------|--------|
| `internal/resolve/resolve.go` | Add `Team string` yaml tag to `RepoConfig`. Add `ResolveTeam(repoRoot string) string` that calls `LoadRepoConfig` and returns `cfg.Team`. |
| `internal/resolve/resolve_test.go` | Table-driven tests for `ResolveTeam`: set, empty, missing config. |

### Acceptance criteria

- `ResolveTeam` returns team name from `team:` field.
- Returns empty string when not configured.
- `make check` passes.

### Config file format

```yaml
# .punt-labs/ethos.yaml
agent: claude
team: engineering
```

---

## Phase 3: FindByRepo on team store

Add `FindByRepo(repo string) ([]*Team, error)` to `Store` and
`LayeredStore`. Iterates all teams, returns those whose `Repositories`
list contains `repo`.

### Changes

| File | Change |
|------|--------|
| `internal/team/store.go` | Add `FindByRepo(repo string) ([]*Team, error)`. List all teams, load each, filter by `Repositories` membership. |
| `internal/team/store_test.go` | Table-driven: repo matches one team, matches multiple, matches none. |
| `internal/team/layered.go` | Add `FindByRepo(repo string) ([]*Team, error)`. Merge results from repo and global stores, deduplicate by `Name`. |
| `internal/team/layered_test.go` | **New file.** Test layered dedup: same team in both layers, repo-only, global-only. |

### Acceptance criteria

- `FindByRepo("ethos")` returns teams with "ethos" in Repositories.
- Returns empty slice (not nil) when no match.
- Layered store deduplicates by team name (repo layer wins).
- `make check` passes.

---

## Phase 4: CLI command

`ethos team for-repo [repo]` prints the team(s) for a repo. When `repo`
is omitted, derives the repo name from the current git remote.

### Changes

| File | Change |
|------|--------|
| `cmd/ethos/team.go` | Add `teamForRepoCmd` cobra command. Register in `init()`. Implement `runTeamForRepo(repo string)`. Uses `FindByRepo` on layered store. |
| `internal/resolve/resolve.go` | Add `RepoName() string` — parses `origin` remote URL. Returns empty string if no remote. |
| `internal/resolve/resolve_test.go` | Tests for `RepoName`: create a temp dir with `.git/config` setting `remote.origin.url` to a controlled value, chdir into it, and restore on cleanup. Follows the existing pattern at `resolve_test.go:180-185`. |

### Output

Text mode: team name, members with roles, collaborations. When multiple
teams match, separate each team block with a blank line.

`runTeamForRepo` shares display logic with existing `runTeamShow` by
extracting a `printTeam(t *team.Team)` helper that both call.

JSON mode (`--json`): full team object(s).

### Acceptance criteria

- `ethos team for-repo ethos` prints matching team.
- `ethos team for-repo` (no arg) infers repo name.
- `ethos team for-repo nonexistent` prints "no team found" and exits 0.
- `make check` passes.

---

## Phase 5: MCP method

Add `for_repo` method to the existing `team` MCP tool.

### Changes

| File | Change |
|------|--------|
| `internal/mcp/team_tools.go` | Add `"for_repo"` to method enum and update the tool description string to mention `for_repo`. Add `"repo"` string parameter with description "Repository name (org/repo). Required for for_repo. Defaults to current repo." Update existing param descriptions to clarify which methods they apply to. Add `handleTeamForRepo(req)` handler. Wire in `handleTeam` switch. |
| `internal/mcp/team_tools_test.go` | Table-driven: repo matches, no match, missing repo param. |

### Acceptance criteria

- `team` tool with `method: for_repo, repo: ethos` returns matching teams.
- Returns empty array when no match.
- Returns error when `repo` param missing.
- `make check` passes.

---

## Migration strategy

The old config path (`.punt-labs/ethos/config.yaml`) was only viable when
`.punt-labs/ethos/` was a plain directory. With the submodule migration
(PR #100), repos already cannot have a `config.yaml` inside the
submodule. The fallback in Phase 1 handles any repo that hasn't adopted
the submodule yet.

Migration steps for each consumer repo:

1. Create `.punt-labs/ethos.yaml` with `agent:` and optional `team:`.
2. Delete `.punt-labs/ethos/config.yaml` (if it was a plain directory).
3. Commit.

No deprecation warning needed -- the old path silently works until
removed. The fallback can be deleted in a future release once all repos
have migrated.

## Ordering

Phases 1-2 are one commit (config changes are small and coupled).
Phase 3 is one commit. Phase 4-5 can be one or two commits. Total: 2-3
commits, one PR.
