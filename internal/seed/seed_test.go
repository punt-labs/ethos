package seed

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSeedEmptyDir(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	result, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// Should have deployed roles
	assert.FileExists(t, filepath.Join(dest, "roles", "implementer.yaml"))
	assert.FileExists(t, filepath.Join(dest, "roles", "reviewer.yaml"))
	assert.FileExists(t, filepath.Join(dest, "roles", "architect.yaml"))
	assert.FileExists(t, filepath.Join(dest, "roles", "security-reviewer.yaml"))
	assert.FileExists(t, filepath.Join(dest, "roles", "researcher.yaml"))
	assert.FileExists(t, filepath.Join(dest, "roles", "test-engineer.yaml"))

	// Every starter role must ship with an `output_format` template so
	// the generator emits the `## Output Format` section in the agent
	// file. Parsing the deployed YAML into a real role.Role catches
	// regressions where `output_format:` appears as a comment or as
	// part of a responsibility string — a substring check would miss
	// both. The check is per-file so the failure message points at
	// the specific role that lost it.
	for _, name := range []string{
		"implementer", "test-engineer",
		"reviewer", "architect", "security-reviewer",
		"researcher",
	} {
		path := filepath.Join(dest, "roles", name+".yaml")
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr, "reading %s.yaml", name)
		var r role.Role
		require.NoError(t, yaml.Unmarshal(data, &r),
			"deployed role %q must parse", name)
		assert.NotEmpty(t, r.OutputFormat,
			"deployed role %q must have output_format after seed", name)
	}

	// Should have deployed all 10 talents
	assert.FileExists(t, filepath.Join(dest, "talents", "go.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "python.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "security.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "typescript.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "testing.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "code-review.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "devops.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "documentation.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "api-design.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "cli-design.md"))

	// Should have deployed the conventional attributes that setup-created
	// identities reference, so a fresh machine resolves them from global
	// even with no bundle active.
	assert.FileExists(t, filepath.Join(dest, "personalities", "principal-engineer.md"))
	assert.FileExists(t, filepath.Join(dest, "writing-styles", "concise-quantified.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "engineering.md"))

	// The previously-dead sidecar files now deploy as live starter content.
	assert.FileExists(t, filepath.Join(dest, "personalities", "sprint-architect.md"))
	assert.FileExists(t, filepath.Join(dest, "personalities", "product-thinker.md"))
	assert.FileExists(t, filepath.Join(dest, "writing-styles", "reviewer-prose.md"))

	// README.md must deploy only via seedReadmes, never as attribute content.
	assert.FileExists(t, filepath.Join(dest, "personalities", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "writing-styles", "README.md"))

	// Should have deployed every top-level sidecar skill — this is the
	// regression guard for the deploy loop in seedSkills: deleting the
	// loop (or dropping a slug from it) leaves TestTopLevelSkillSlugs
	// green, since that test only reads the embed directive.
	// No top-level sidecar skill seeds to the global skills dir anymore:
	// every ethos skill (baseline-ops and the gstack bundle skills included)
	// ships through the plugin as ethos:<skill> (plugin/skills/) and is
	// referenced by that namespaced name in generated agents (PL-532).
	assert.NoFileExists(t, filepath.Join(skills, "baseline-ops", "SKILL.md"),
		"baseline-ops ships via the plugin now, not the flat skills dir")

	// Should have deployed all 8 generic pipeline files at the top
	// level. The 5 gstack-* pipelines now ship inside the gstack
	// bundle (asserted below) and no longer live at sidecar/pipelines.
	assert.FileExists(t, filepath.Join(dest, "pipelines", "quick.yaml"))
	assert.FileExists(t, filepath.Join(dest, "pipelines", "standard.yaml"))
	assert.FileExists(t, filepath.Join(dest, "pipelines", "full.yaml"))
	assert.FileExists(t, filepath.Join(dest, "pipelines", "product.yaml"))
	assert.FileExists(t, filepath.Join(dest, "pipelines", "formal.yaml"))
	assert.FileExists(t, filepath.Join(dest, "pipelines", "docs.yaml"))
	assert.FileExists(t, filepath.Join(dest, "pipelines", "coe.yaml"))
	assert.FileExists(t, filepath.Join(dest, "pipelines", "coverage.yaml"))
	assert.NoFileExists(t, filepath.Join(dest, "pipelines", "gstack-plan.yaml"),
		"gstack pipelines must live inside the bundle, not at the top level")

	// Gstack bundle — manifest + representative content from every
	// subdirectory. Covers manifest deploy, team copy, identity copy,
	// and pipeline migration into the bundle tree.
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "bundle.yaml"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "teams", "gstack.yaml"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "identities", "gstack-architect.yaml"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "identities", "gstack-implementer.yaml"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "personalities", "gstack-architect.md"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "writing-styles", "gstack-builder.md"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "roles", "architect.yaml"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "talents", "engineering.md"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "talents", "product-development.md"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "pipelines", "gstack-plan.yaml"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "pipelines", "gstack-ship.yaml"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "pipelines", "gstack-design.yaml"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "pipelines", "gstack-debug.yaml"))
	assert.FileExists(t, filepath.Join(dest, "bundles", "gstack", "pipelines", "gstack-review.yaml"))

	// Should have deployed all 7 READMEs (sessions excluded)
	assert.FileExists(t, filepath.Join(dest, "identities", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "personalities", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "writing-styles", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "roles", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "skills", "README.md"))
	assert.FileExists(t, filepath.Join(dest, "README.md"))

	assert.NotEmpty(t, result.Deployed)
	assert.Empty(t, result.Skipped)
	assert.Empty(t, result.Errors)
}

// TestSeedNoClobber pins the untracked no-clobber contract: a non-empty file
// with no manifest entry is never overwritten by a plain seed, and is reported
// as skipped.
func TestSeedNoClobber(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// Pre-create a role file with custom content, with no prior seed — so it
	// has no manifest entry.
	rolesDir := filepath.Join(dest, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o755))
	customContent := []byte("name: implementer\nmodel: opus\n")
	require.NoError(t, os.WriteFile(filepath.Join(rolesDir, "implementer.yaml"), customContent, 0o644))

	result, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// Custom file should be preserved.
	data, err := os.ReadFile(filepath.Join(rolesDir, "implementer.yaml"))
	require.NoError(t, err)
	assert.Equal(t, customContent, data, "an untracked existing file must not be overwritten")

	// implementer.yaml should be in the skipped list.
	found := false
	for _, s := range result.Skipped {
		if filepath.Base(s) == "implementer.yaml" {
			found = true
		}
	}
	assert.True(t, found, "implementer.yaml should be in the skipped list")

	// Other roles should still be deployed.
	assert.FileExists(t, filepath.Join(rolesDir, "reviewer.yaml"))
}

// TestSeedPreservesTrackedEdit pins the new tracked-edit contract: a file
// edited after seed has recorded its hash is a proven user edit — preserved and
// reported under Edited, not overwritten.
func TestSeedPreservesTrackedEdit(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// First seed tracks every deployed file in the manifest.
	_, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// Edit a tracked role.
	rolePath := filepath.Join(dest, "roles", "implementer.yaml")
	custom := []byte("name: implementer\nmodel: opus\n")
	require.NoError(t, os.WriteFile(rolePath, custom, 0o644))

	// Re-seed: the edit differs from the recorded hash, so it is preserved.
	result, err := Seed(dest, skills, false)
	require.NoError(t, err)

	data, err := os.ReadFile(rolePath)
	require.NoError(t, err)
	assert.Equal(t, custom, data, "a tracked user edit must not be overwritten")

	found := false
	for _, e := range result.Edited {
		if filepath.Base(e) == "implementer.yaml" {
			found = true
		}
	}
	assert.True(t, found, "edited implementer.yaml should be in the edited list")
}

// TestSeedRepairsZeroByteFile pins the S3 fix: a zero-byte file left by an
// interrupted seed is corruption, not user content. A re-seed overwrites it
// atomically and reports it as repaired, while a non-empty existing file is
// left untouched.
func TestSeedRepairsZeroByteFile(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// A zero-byte talent (partial from a killed seed).
	talentsDir := filepath.Join(dest, "talents")
	require.NoError(t, os.MkdirAll(talentsDir, 0o755))
	zeroPath := filepath.Join(talentsDir, "engineering.md")
	require.NoError(t, os.WriteFile(zeroPath, []byte{}, 0o644))

	// A non-empty user-edited role that must survive.
	rolesDir := filepath.Join(dest, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o755))
	custom := []byte("name: implementer\nmodel: opus\n")
	require.NoError(t, os.WriteFile(filepath.Join(rolesDir, "implementer.yaml"), custom, 0o644))

	result, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// Zero-byte file repaired to real content.
	repaired, err := os.ReadFile(zeroPath)
	require.NoError(t, err)
	assert.NotEmpty(t, repaired, "zero-byte file must be repaired to real content")
	assert.Contains(t, result.Repaired, zeroPath, "repaired file must be reported")
	assert.NotContains(t, result.Skipped, zeroPath, "a zero-byte file is not a no-clobber skip")

	// Non-empty untracked user file untouched.
	got, err := os.ReadFile(filepath.Join(rolesDir, "implementer.yaml"))
	require.NoError(t, err)
	assert.Equal(t, custom, got, "an untracked non-empty file must not be clobbered")
}

// TestSeed_DirAtDestFailsLoud pins the B1 ruling: a directory occupying a
// file dest is corruption, not a no-clobber skip. Seed must fail loud and
// name the path rather than report "skipped (exists)" and leave the real
// file undeployed.
func TestSeed_DirAtDestFailsLoud(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// A directory where a talent file belongs.
	badPath := filepath.Join(dest, "talents", "engineering.md")
	require.NoError(t, os.MkdirAll(badPath, 0o755))

	result, err := Seed(dest, skills, false)
	require.Error(t, err, "a directory at a file dest must fail seed")

	var named bool
	for _, e := range result.Errors {
		if strings.Contains(e, badPath) && strings.Contains(e, "directory") {
			named = true
		}
	}
	assert.True(t, named, "error must name the directory dest; got %v", result.Errors)
	assert.NotContains(t, result.Skipped, badPath, "a directory must not be reported as a skip")

	// The failure is isolated: other files still deploy.
	assert.FileExists(t, filepath.Join(dest, "roles", "implementer.yaml"))
}

// TestLinkInstall_FreshCreate pins the atomic create path: a fresh dest is
// written with 0644 perms and the exact content, leaving no temp file.
func TestLinkInstall_FreshCreate(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "attr.md")
	require.NoError(t, linkInstall(dest, []byte("# content\n")))

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "# content\n", string(got))
	info, err := os.Stat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "temp file must be cleaned up")
}

// TestLinkInstall_FailsOnExisting pins the TOCTOU close: os.Link refuses to
// clobber an existing dest, surfacing os.ErrExist atomically. That signal is
// what installNoClobber keys off to re-decide (skip or repair) rather than
// replacing a file that raced in after the Stat.
func TestLinkInstall_FailsOnExisting(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "attr.md")
	require.NoError(t, os.WriteFile(dest, []byte("user content\n"), 0o644))

	err := linkInstall(dest, []byte("seed content\n"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrExist), "link to an existing dest must report ErrExist; got %v", err)

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "user content\n", string(got), "existing file must not be clobbered")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "temp file must be cleaned up even on link failure")
}

func TestSeedForce(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// Pre-create a role file
	rolesDir := filepath.Join(dest, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rolesDir, "implementer.yaml"), []byte("custom"), 0o644))

	result, err := Seed(dest, skills, true)
	require.NoError(t, err)

	// File should be overwritten with embedded content
	rolePath := filepath.Join(rolesDir, "implementer.yaml")
	data, err := os.ReadFile(rolePath)
	require.NoError(t, err)
	assert.NotEqual(t, "custom", string(data), "force should overwrite")
	assert.Contains(t, string(data), "name: implementer")

	// Force-written files must have 0644 permissions, not 0600 from CreateTemp
	info, err := os.Stat(rolePath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "force-seeded file should be 0644")

	assert.Empty(t, result.Skipped)
}

func TestSeedPartialState(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// Create roles dir with one file, but no talents dir
	rolesDir := filepath.Join(dest, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rolesDir, "implementer.yaml"), []byte("custom"), 0o644))

	result, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// Untracked existing role preserved (no-clobber).
	data, _ := os.ReadFile(filepath.Join(rolesDir, "implementer.yaml"))
	assert.Equal(t, "custom", string(data))

	// Missing talents should be created
	assert.FileExists(t, filepath.Join(dest, "talents", "go.md"))
	assert.FileExists(t, filepath.Join(dest, "talents", "python.md"))

	// No skill seeds to the flat skills dir anymore (PL-532): ethos skills
	// ship through the plugin.
	assert.NoFileExists(t, filepath.Join(skills, "baseline-ops", "SKILL.md"))

	assert.NotEmpty(t, result.Deployed)
}

// TestBaselineOpsSkillContent checks baseline-ops ships with its operational
// content. It now ships through the plugin (plugin/skills/baseline-ops/) as
// ethos:baseline-ops rather than being seeded to the flat skills dir, so this
// reads the plugin file. `go test` runs with the package dir as cwd, so the
// repo root is two levels up from internal/seed.
func TestBaselineOpsSkillContent(t *testing.T) {
	skillPath := filepath.Join("..", "..", "plugin", "skills", "baseline-ops", "SKILL.md")
	assert.FileExists(t, skillPath)

	data, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "## Tool Usage")
	assert.Contains(t, string(data), "## Verification")
}

// TestMissionSkillContent checks the mission skill ships with the Phase 3
// schema content. The skill is the user-facing surface for driving the
// mission primitive; its drift would silently teach the wrong shape. The
// mission skill now ships through the plugin (plugin/skills/mission/SKILL.md)
// as ethos:mission rather than being seeded to the global skills dir, so this
// reads the plugin file directly. `go test` runs with the package dir as cwd,
// so the repo root is two levels up from internal/seed.
func TestMissionSkillContent(t *testing.T) {
	skillPath := filepath.Join("..", "..", "plugin", "skills", "mission", "SKILL.md")
	assert.FileExists(t, skillPath)

	data, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	content := string(data)

	// Structural anchors: every step plus the worked example must
	// be present. A future edit that drops a section fails here.
	assert.Contains(t, content, "## Step 1 — Resolve the worker")
	assert.Contains(t, content, "## Step 2 — Scaffold the contract YAML")
	assert.Contains(t, content, "## Step 3 — Pick the evaluator")
	assert.Contains(t, content, "## Step 4 — Create the mission")
	assert.Contains(t, content, "## Step 5 — Spawn the worker")
	assert.Contains(t, content, "## Step 6 — Track and review")
	assert.Contains(t, content, "## Worked example")

	// Phase 3 schema anchors: every required field name from
	// mission.Contract must appear so a future schema drift
	// surfaces as a test failure, not as a SKILL.md that teaches
	// the wrong shape.
	assert.Contains(t, content, "leader:")
	assert.Contains(t, content, "worker:")
	assert.Contains(t, content, "evaluator:")
	assert.Contains(t, content, "write_set:")
	assert.Contains(t, content, "success_criteria:")
	assert.Contains(t, content, "budget:")

	// context is a TOP-LEVEL field on mission.Contract, NOT a
	// subfield of Inputs. An earlier draft nested it under inputs;
	// Phase 3.1's strict YAML decode (KnownFields true) would have
	// rejected the worked example at `ethos mission create` time.
	// Assert top-level placement so future drift fails here
	// instead of at the store boundary.
	assert.Contains(t, content, "\ncontext: |",
		"worked example must have top-level `context: |`, not nested under inputs")
	assert.NotContains(t, content, "  context: |",
		"`context: |` must NOT be indented under inputs — mission.Contract has Context at the top level")

	// Command anchors: the real CLI surfaces the skill teaches.
	assert.Contains(t, content, "ethos mission create --file")
	assert.Contains(t, content, "ethos mission show")
	assert.Contains(t, content, "ethos mission log")
	assert.Contains(t, content, "ethos mission result")
	assert.Contains(t, content, "ethos mission close")

	// Background-spawn discipline: the Agent call MUST be
	// described as run_in_background.
	assert.Contains(t, content, "run_in_background")
}

func TestSeedIntegrationWithRoleStore(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	_, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// Verify seeded roles are loadable — just check YAML validity
	data, err := os.ReadFile(filepath.Join(dest, "roles", "implementer.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "name: implementer")
	assert.Contains(t, string(data), "model: sonnet")
	assert.Contains(t, string(data), "- Bash")
}

// TestSeedBundleNoClobber checks that a user's edits to a bundle
// file are not overwritten by a second Seed. Bundles are shipped as
// starter content; consumers may edit in place before migrating to a
// writable layer, and the preserve contract must hold for the whole
// bundle tree, not only the legacy flat directories. The first seed
// tracks the file, so a later edit is reported under Edited.
func TestSeedBundleNoClobber(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	_, err := Seed(dest, skills, false)
	require.NoError(t, err)

	manifest := filepath.Join(dest, "bundles", "gstack", "bundle.yaml")
	custom := []byte("name: gstack\nversion: 999\n")
	require.NoError(t, os.WriteFile(manifest, custom, 0o644))

	result, err := Seed(dest, skills, false)
	require.NoError(t, err)

	data, err := os.ReadFile(manifest)
	require.NoError(t, err)
	assert.Equal(t, custom, data, "bundle file must not be overwritten without --force")

	found := false
	for _, e := range result.Edited {
		if e == manifest {
			found = true
			break
		}
	}
	assert.True(t, found, "edited bundle manifest should be in the edited list on second seed")
}

// TestTopLevelSkillSlugs_DerivedFromEmbeddedFS pins the Copilot MEDIUM
// finding on PR #481: the sidecar top-level skill set must be enumerated
// from the embedded Skills FS, not hardcoded, so a new skill added under
// sidecar/skills/ is picked up with no code change here.
func TestTopLevelSkillSlugs_DerivedFromEmbeddedFS(t *testing.T) {
	got := topLevelSkillSlugs()
	// No top-level sidecar skill seeds to the flat skills dir anymore
	// (PL-532): every ethos skill ships through the plugin. The Skills FS
	// embeds only the README (not a directory), so the slug set is empty.
	assert.Empty(t, got)
}

// TestSeedDeploysNoFlatSkills pins PL-532: no ethos skill seeds to the flat
// ~/.claude/skills/ namespace anymore — neither the gstack bundle skills nor
// baseline-ops. They all ship through the plugin (plugin/skills/) as
// ethos:<skill> and are referenced by that namespaced name in generated
// agents. A seed, with the gstack bundle active or not, deploys no SKILL.md to
// the skills root.
func TestSeedDeploysNoFlatSkills(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	_, err := SeedVersionWithBundle(dest, skills, "", "gstack", "", false)
	require.NoError(t, err)

	assert.NoFileExists(t, filepath.Join(skills, "gstack-plan", "SKILL.md"))
	assert.NoFileExists(t, filepath.Join(skills, "gstack-ship", "SKILL.md"))
	assert.NoFileExists(t, filepath.Join(skills, "baseline-ops", "SKILL.md"))

	noBundleSkills := t.TempDir()
	_, err = Seed(dest, noBundleSkills, false)
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(noBundleSkills, "gstack-plan", "SKILL.md"))
	assert.NoFileExists(t, filepath.Join(noBundleSkills, "baseline-ops", "SKILL.md"))
}

// TestSeedDeploysRepoLocalBundleSkills pins the Bugbot MEDIUM finding on
// PR #481: a repo-local bundle override (the same on-disk source
// bundle.ResolveActive gives the generator) must have ITS skills
// deployed, not the embedded sidecar copy. A repo-local bundle here
// carries a "foo" skill the embedded gstack bundle does not ship.
func TestSeedDeploysRepoLocalBundleSkills(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	bundleDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bundleDir, "skills", "foo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "skills", "foo", "SKILL.md"),
		[]byte("# Foo\n\nRepo-local bundle skill, not shipped in the embedded bundle.\n"), 0o644))

	_, err := SeedVersionWithBundleDir(dest, skills, "", "my-bundle", bundleDir, "", false)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(skills, "foo", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "Repo-local bundle skill")

	// The embedded gstack skills must NOT appear — the resolved bundle
	// (my-bundle, at bundleDir) is what's deployed, not the embedded one.
	assert.NoFileExists(t, filepath.Join(skills, "gstack-plan", "SKILL.md"))
}

func TestSeedIdempotent(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// First seed
	r1, err := Seed(dest, skills, false)
	require.NoError(t, err)
	assert.NotEmpty(t, r1.Deployed)
	assert.Empty(t, r1.Skipped)

	// Second seed — everything is already at the shipped content, so nothing
	// is written and nothing is a user edit.
	r2, err := Seed(dest, skills, false)
	require.NoError(t, err)
	assert.Empty(t, r2.Deployed)
	assert.Empty(t, r2.Updated)
	assert.Empty(t, r2.Skipped)
	assert.NotEmpty(t, r2.Unchanged)
	assert.Empty(t, r2.Errors)
}

// TestSeedAgents_Category pins the DES-070 review-checklist agent category
// specifically: it is skipped when no agentsRoot is given (a bare global
// seed), deployed under agentsRoot rather than destRoot when one is given,
// and obeys the same no-clobber/upgrade/preserve-edit contract every other
// sidecar category gets from decide/place — proven here rather than assumed,
// since sidecar/agents is the one category with a caller-supplied,
// non-destRoot destination.
func TestSeedAgents_Category(t *testing.T) {
	const agentFile = "code-reviewer.md"

	cases := []struct {
		name            string
		setup           func(t *testing.T, agentsDir string)
		agentsRootUnset bool
		wantContent     string // "" means: shipped content
		wantBucket      string // Deployed, Updated, Unchanged, Skipped, Edited, or "" for none
	}{
		{
			name:            "no agentsRoot skips the category entirely",
			agentsRootUnset: true,
			wantBucket:      "",
		},
		{
			name:       "absent deploys",
			wantBucket: "Deployed",
		},
		{
			name: "untracked existing file is no-clobber skipped",
			setup: func(t *testing.T, agentsDir string) {
				require.NoError(t, os.MkdirAll(agentsDir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(agentsDir, agentFile),
					[]byte("operator narrowed this agent's scope\n"), 0o644))
			},
			wantContent: "operator narrowed this agent's scope\n",
			wantBucket:  "Skipped",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			skills := t.TempDir()
			agentsDir := filepath.Join(t.TempDir(), ".claude", "agents")

			if tc.setup != nil {
				tc.setup(t, agentsDir)
			}

			agentsRoot := agentsDir
			if tc.agentsRootUnset {
				agentsRoot = ""
			}

			result, err := SeedVersion(dest, skills, agentsRoot, "", false)
			require.NoError(t, err)
			require.Empty(t, result.Errors)

			agentPath := filepath.Join(agentsDir, agentFile)

			if tc.agentsRootUnset {
				assert.NoFileExists(t, agentPath, "no agentsRoot must deploy nothing under it")
				assert.NoFileExists(t, filepath.Join(dest, "agents", agentFile),
					"no agentsRoot must not fall back to destRoot either")
				return
			}

			require.FileExists(t, agentPath)
			data, readErr := os.ReadFile(agentPath)
			require.NoError(t, readErr)
			if tc.wantContent != "" {
				assert.Equal(t, tc.wantContent, string(data))
			} else {
				assert.Contains(t, string(data), "name: code-reviewer",
					"deployed file should be the shipped code-reviewer content")
			}

			var bucket []string
			switch tc.wantBucket {
			case "Deployed":
				bucket = result.Deployed
			case "Updated":
				bucket = result.Updated
			case "Unchanged":
				bucket = result.Unchanged
			case "Skipped":
				bucket = result.Skipped
			case "Edited":
				bucket = result.Edited
			}
			assert.Contains(t, bucket, agentPath,
				"agent file should land in the %s bucket", tc.wantBucket)
		})
	}
}

// TestSeedAgents_PreservesTrackedEdit mirrors TestSeedPreservesTrackedEdit
// for the agents category: a second seed against the same agentsRoot must
// upgrade an untouched tracked file and preserve one the operator edited,
// exactly like roles/talents/etc.
func TestSeedAgents_PreservesTrackedEdit(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()
	agentsDir := filepath.Join(t.TempDir(), ".claude", "agents")

	// First seed tracks the agent files in the manifest.
	_, err := SeedVersion(dest, skills, agentsDir, "", false)
	require.NoError(t, err)

	agentPath := filepath.Join(agentsDir, "silent-failure-hunter.md")
	custom := []byte("operator's narrowed silent-failure-hunter\n")
	require.NoError(t, os.WriteFile(agentPath, custom, 0o644))

	// Re-seed: the edit differs from the recorded hash, so it is preserved.
	result, err := SeedVersion(dest, skills, agentsDir, "", false)
	require.NoError(t, err)

	data, err := os.ReadFile(agentPath)
	require.NoError(t, err)
	assert.Equal(t, custom, data, "a tracked operator edit to a seeded agent must not be overwritten")
	assert.Contains(t, result.Edited, agentPath)
}

// TestSeedAgents_CommittedCopyMatchesShippedSource pins this repo's own
// dogfooding of DES-070 against silent drift. This repo's .claude/agents/
// stays committed by policy (never gitignored — generated agent files are
// tracked so blame/review sees them), so on a fresh clone the three seeded
// review agents already exist there, untracked by the seed manifest.
// `ethos seed` then takes the no-clobber path for all three and can never
// upgrade them here without --force — this repo is the one place the
// feature would go silently inert. Rather than relying on a human to
// remember to re-run `ethos seed --force` after every prompt edit, this
// test fails CI the moment the committed copy and the shipped source
// diverge, so the two are edited together or not at all.
func TestSeedAgents_CommittedCopyMatchesShippedSource(t *testing.T) {
	entries, err := fs.ReadDir(Agents, "sidecar/agents")
	require.NoError(t, err)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			shipped, err := fs.ReadFile(Agents, "sidecar/agents/"+name)
			require.NoError(t, err, "reading shipped %s from the embedded sidecar", name)

			// `go test` runs with the package directory as cwd, so the repo
			// root is two levels up from internal/seed.
			committedPath := filepath.Join("..", "..", ".claude", "agents", name)
			committed, err := os.ReadFile(committedPath)
			require.NoError(t, err, "reading this repo's own committed %s", committedPath)

			assert.Equal(t, string(shipped), string(committed),
				"internal/seed/sidecar/agents/%s and %s have drifted — "+
					"re-run `ethos seed --force` in this repo and commit the result", name, committedPath)
		})
	}
}

// TestSeedRetiresMovedSkills guards the upgrade path: every ethos skill that
// moved to plugin/skills/ (the PL-319 three plus the PL-532 baseline-ops and
// gstack skills) is no longer seeded, so a re-seed on an existing install must
// remove its previously-seeded ~/.claude/skills/<slug>/ copy — but only the
// unmodified copy seed itself wrote, never a user edit, and never a skill ethos
// did not ship (an unrelated slug the user put there).
func TestSeedRetiresMovedSkills(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	writeSkill := func(slug, body string) string {
		dir := filepath.Join(skills, slug)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		p := filepath.Join(dir, "SKILL.md")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
		return p
	}
	unmodified := "---\nname: mission\n---\n\n# old seeded body\n"
	missionPath := writeSkill("mission", unmodified)
	// baseline-ops is now a retired skill too; present and unmodified, so it
	// must be removed on re-seed.
	baselineBody := "---\nname: baseline-ops\n---\n\n# baseline\n"
	baselinePath := writeSkill("baseline-ops", baselineBody)
	// optimize-team is present but USER-MODIFIED relative to what the
	// manifest records, so it must be left untouched.
	optPath := writeSkill("optimize-team", "---\nname: optimize-team\n---\n\n# MY LOCAL EDIT\n")
	// my-custom-skill is a skill ethos never shipped (not in
	// retiredTopLevelSkills), so it must never be touched even if present.
	customBody := "---\nname: my-custom-skill\n---\n\n# mine\n"
	customPath := writeSkill("my-custom-skill", customBody)

	key := func(slug string) string { return "skills/" + slug + "/SKILL.md" }
	mf := &Manifest{Schema: manifestSchema, Entries: map[string]Entry{
		key("mission"):         {Scope: scopeSkills, Hash: hashBytes([]byte(unmodified))},
		key("baseline-ops"):    {Scope: scopeSkills, Hash: hashBytes([]byte(baselineBody))},
		key("optimize-team"):   {Scope: scopeSkills, Hash: hashBytes([]byte("different shipped content"))},
		key("my-custom-skill"): {Scope: scopeSkills, Hash: hashBytes([]byte(customBody))},
	}}
	require.NoError(t, mf.save(dest))

	result, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// (a) unmodified retired skills are removed (incl. baseline-ops), and
	// (b) their manifest entries are dropped.
	assert.NoFileExists(t, missionPath, "unmodified retired mission skill should be removed")
	assert.NoFileExists(t, baselinePath, "unmodified retired baseline-ops skill should be removed")
	assert.Contains(t, result.Retired, missionPath)
	assert.Contains(t, result.Retired, baselinePath)

	// (c) a USER-MODIFIED retired skill is left in place.
	assert.FileExists(t, optPath, "user-modified optimize-team skill must not be removed")
	assert.NotContains(t, result.Retired, optPath)

	// (d) a skill ethos never shipped is never a retirement target.
	assert.FileExists(t, customPath, "a non-ethos skill must never be retired")
	assert.NotContains(t, result.Retired, customPath)

	after, err := loadManifest(dest)
	require.NoError(t, err)
	assert.NotContains(t, after.Entries, key("mission"))
	assert.NotContains(t, after.Entries, key("baseline-ops"))
	assert.Contains(t, after.Entries, key("optimize-team"))
	assert.Contains(t, after.Entries, key("my-custom-skill"))

	// (e) a second seed does not resurrect the removed skills.
	_, err = Seed(dest, skills, false)
	require.NoError(t, err)
	assert.NoFileExists(t, missionPath)
	assert.NoFileExists(t, baselinePath)
}

// TestRetireSkillBranches covers retireSkill's error and edge paths directly:
// a tracked-but-already-gone file (no error, nothing to do), and a read error
// that is not os.IsNotExist (recorded, not fatal). The happy path and the
// user-modified/untracked skips are covered by TestSeedRetiresMovedSkills.
func TestRetireSkillBranches(t *testing.T) {
	t.Run("tracked but already gone on disk", func(t *testing.T) {
		skills := t.TempDir()
		s := testSeeder(t.TempDir(), skills, false)
		key := "skills/mission/SKILL.md"
		s.mf.Entries[key] = Entry{Scope: scopeSkills, Hash: "sha256:whatever"}

		s.retireSkill(skills, "mission") // file never created

		assert.Empty(t, s.r.Errors, "a missing tracked file is not an error")
		assert.Empty(t, s.r.Retired, "nothing was removed")
		assert.Contains(t, s.mf.Entries, key, "manifest entry left intact when the file was already gone")
	})

	t.Run("read error that is not NotExist is recorded", func(t *testing.T) {
		skills := t.TempDir()
		s := testSeeder(t.TempDir(), skills, false)
		key := "skills/mission/SKILL.md"
		s.mf.Entries[key] = Entry{Scope: scopeSkills, Hash: "sha256:whatever"}

		// Put a directory where retireSkill expects the SKILL.md file, so
		// os.ReadFile returns EISDIR — an error, but not os.IsNotExist.
		require.NoError(t, os.MkdirAll(filepath.Join(skills, "mission", "SKILL.md"), 0o755))

		s.retireSkill(skills, "mission")

		require.Len(t, s.r.Errors, 1, "a non-NotExist read error must be recorded")
		assert.Contains(t, s.r.Errors[0], "for retirement")
		assert.Empty(t, s.r.Retired)
		assert.Contains(t, s.mf.Entries, key, "manifest entry left intact on a read error")
	})
}
