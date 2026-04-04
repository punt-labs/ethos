# Build Plan: Installer Deploys Sidecar Starter Content

Bead: ethos-zx2.4
Parent epic: ethos-zx2 (Phase 1: Batteries Included)

---

## Design

### Problem

Phase 1 shipped starter roles (6), talents (10), and a baseline-ops
skill in `internal/seed/sidecar/`. These files exist in the repo but are unreachable
at runtime — `ethos role list` shows nothing because neither the
global store (`~/.punt-labs/ethos/`) nor the repo-local store
(`.punt-labs/ethos/`) contains the starter content.

The automated tests pass because they manually copy sidecar content
into temp directories. Manual verification (`ethos role show
implementer`) fails.

### Decision

The installer (`install.sh`) seeds global directories with starter
content from the release archive. Repo-local content (team submodule)
overrides global via the existing layered store.

**What gets deployed:**

| Source | Destination | Method |
|--------|-------------|--------|
| `internal/seed/sidecar/roles/*.yaml` | `~/.punt-labs/ethos/roles/` | `cp -n` (no clobber) |
| `internal/seed/sidecar/talents/*.md` (not README) | `~/.punt-labs/ethos/talents/` | `cp -n` (no clobber) |
| `internal/seed/sidecar/skills/baseline-ops/SKILL.md` | `~/.claude/skills/baseline-ops/SKILL.md` | `cp -n` (no clobber) |
| `internal/seed/sidecar/*/README.md` | `~/.punt-labs/ethos/*/README.md` | `cp -n` (no clobber) |

**Why `cp -n` (no clobber):**
- First install seeds defaults
- Reinstall preserves user customizations
- User can always reset by deleting and reinstalling

**Why global, not repo-local:**
- Global applies to every project without per-repo setup
- Repo-local content (team submodule) overrides global automatically
- The installer runs once, not per-repo

**Installer changes (shell only, no Go):**
- The installer already downloads a release binary OR builds from
  source. In the download path, the sidecar content is not available
  (only the binary is downloaded). In the source build path, the
  full repo is cloned.
- Solution: in the source build path, copy sidecar content from the
  cloned repo. In the download path, download a sidecar tarball from
  the release assets alongside the binary.
- Alternative (simpler): the ethos binary itself deploys sidecar
  content via `ethos init` or a post-install hook. This avoids
  modifying the installer's download path.

### Chosen approach: `ethos doctor --seed`

Add a `--seed` flag to `ethos doctor` (or a new `ethos seed` command)
that copies bundled sidecar content to the global directories. The
installer calls this after installing the binary.

Why this is better than shell-level copying:
- Works for both download and source-build install paths
- The binary can embed sidecar content via `go:embed`
- No separate tarball download needed
- `ethos seed` is independently useful (users can re-seed after
  customizing)
- Testable in Go (not shell)

### Rejected alternatives

- **Shell-level cp in install.sh**: requires sidecar files to be
  available at install time. The binary-download path doesn't have
  them. Would need a separate sidecar tarball asset.
- **SessionStart hook deploys content**: runs every session, creates
  latency. Deployment is a one-time operation.
- **Third resolution layer for internal/seed/sidecar/**: adds complexity to the
  store without solving the fundamental issue that users need
  content in known locations.

---

## Build Plan

### Phase 1: Embed sidecar content in binary

**Files:**
- `internal/seed/` — new package
- `internal/seed/embed.go` — `go:embed` directives for sidecar content
- `internal/seed/seed.go` — `Seed(destRoot string)` function

**Implementation:**

```go
package seed

import "embed"

//go:embed roles/*.yaml
var roles embed.FS

//go:embed talents/*.md
var talents embed.FS

//go:embed skills/baseline-ops/SKILL.md
var skills embed.FS

//go:embed readmes
var readmes embed.FS
```

Wait — `go:embed` paths are relative to the source file. The sidecar
content is now in `internal/seed/sidecar/`, directly embeddable from
`internal/seed/`.

**Revised approach**: create a `cmd/ethos/seed/` package adjacent to
the sidecar directory, or use a build step that copies sidecar
content into an embeddable location before `go build`.

**Simpler revised approach**: `ethos seed` reads sidecar content from
a known location relative to the binary, OR we restructure so the
embeddable content lives in a Go-accessible path.

**Simplest approach**: `ethos seed` reads from the plugin cache.
When ethos is installed as a plugin, the full repo (including
internal/seed/sidecar/) is cached at `~/.claude/plugins/cache/punt-labs/ethos/<version>/`.
The `seed` command reads from there.

This means:
1. Binary-only install (no plugin): `ethos seed` cannot find sidecar
   content → warns and skips
2. Plugin install: `ethos seed` reads from plugin cache → deploys

This is acceptable because the plugin install is the primary path.

### Phase 2: `ethos seed` CLI command

**File:** `cmd/ethos/seed.go`

```
ethos seed [--force]
```

- Discovers sidecar content from plugin cache
  (`~/.claude/plugins/cache/punt-labs/ethos/*/internal/seed/sidecar/`)
- Copies roles, talents, skills, READMEs to global directories
- Default: `cp -n` (no clobber) — skip existing files
- `--force`: overwrite existing files
- Reports what was deployed

**File:** `cmd/ethos/seed_test.go`

Tests:
- Seeds into empty directory → all files present
- Seeds into directory with existing files → existing preserved
- Seeds with --force → existing overwritten
- Seeds with no plugin cache → graceful error

### Phase 3: Installer calls `ethos seed`

**File:** `install.sh`

After Step 6 (plugin install), before Step 7 (health check):

```sh
# --- Step 6b: Seed starter content ---
info "Seeding starter content..."
if "$INSTALL_DIR/$BINARY" seed 2>/dev/null; then
  ok "Starter roles, talents, and skills deployed"
else
  warn "Could not seed starter content (plugin cache not found)"
  warn "Run 'ethos seed' after plugin installation completes"
fi
```

### Phase 4: Tests

**Automated (Go):**
- `cmd/ethos/seed_test.go` — unit tests for seed logic
- Verify: roles load from seeded directory, talents load, skill exists

**Automated (shell):**
- Not needed — installer changes are minimal and `ethos seed` is
  tested in Go

**Manual verification:**
- Fresh install: `ethos seed` → `ethos role list` shows 6 starter
  roles → `ethos talent list` shows 10 starter talents
- Reinstall: modify a role → `ethos seed` → modified file preserved
- `ethos seed --force` → modified file overwritten
- `ls ~/.claude/skills/baseline-ops/SKILL.md` → exists

### Phase 5: Documentation

- Update CHANGELOG [Unreleased]: add `ethos seed` command
- Update README: mention `ethos seed` in Quick Start or Setup
- Update install.sh Usage comment

### Phase 6: Local code review

- Two reviewers on all Go changes
- Two reviewers on installer changes
- Fix → verify → clean cycle

### Phase 7: Manual testing on branch

- Run `make install` to build the binary
- Run `ethos seed` manually
- Verify `ethos role list` shows starter roles
- Verify `ethos role show implementer` shows tools and model
- Verify `ethos talent list` shows starter talents
- Verify `ls ~/.claude/skills/baseline-ops/SKILL.md` exists
- Verify a second `ethos seed` preserves modifications
- Verify `ethos seed --force` overwrites

### Phase 8: PR

- Push branch, create PR
- Wait for CI + Copilot + Bugbot
- Address all findings
- Merge

---

## Sequence

```
Phase 1 (embed/discover) → Phase 2 (seed command) → Phase 3 (installer)
     → Phase 4 (tests) → Phase 5 (docs) → Phase 6 (review)
     → Phase 7 (manual test) → Phase 8 (PR)
```

All phases are sequential. Phase 4 tests are written alongside
Phase 2 code (TDD). Phase 6 review covers all code before Phase 7
manual testing.

---

## Risk

**Plugin cache discovery**: The sidecar content location depends on
the plugin cache path, which includes a version number. If the cache
structure changes, `ethos seed` breaks. Mitigation: discover by
walking the cache directory for the most recent version, not
hardcoding the path.

**go:embed limitation**: Cannot embed from parent directories.
Mitigation: read from plugin cache at runtime instead of embedding.

**Binary-only install**: Users who install only the binary (no plugin)
cannot run `ethos seed`. Mitigation: document this limitation, offer
`ethos seed --from <path>` for manual sidecar path specification.
