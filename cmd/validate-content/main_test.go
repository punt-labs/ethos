package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/enable"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/stretchr/testify/require"
)

// buildValidateContent compiles the validate-content binary once for the
// package's test run and returns its path.
func buildValidateContent(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "validate-content")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "building validate-content: %s", out)
	return bin
}

// minimalRepo writes the smallest ethos tree validate-content will accept:
// an identity, and empty directories for every content kind it scans.
// resolution, if non-empty, is written to .punt-labs/ethos.yaml.
func minimalRepo(t *testing.T, resolution string) (repoRoot, ethosRoot string) {
	t.Helper()
	repoRoot = t.TempDir()
	ethosRoot = filepath.Join(repoRoot, ".punt-labs", "ethos")
	for _, dir := range []string{
		"identities", "talents", "personalities", "writing-styles", "roles", "teams", "agents",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(ethosRoot, dir), 0o755))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "docs", "ETHOS-SETUP.md"), enable.Setup, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ethosRoot, "identities", "alice.yaml"), []byte(
		"name: Alice\nhandle: alice\nkind: human\n"), 0o644))

	cfg := "agent: alice\nteam: engineering\n"
	if resolution != "" {
		cfg += "resolution: " + resolution + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, ".punt-labs", "ethos.yaml"), []byte(cfg), 0o644))
	return repoRoot, ethosRoot
}

// globalWithInvalidIdentity writes an identity to globalRoot that fails
// referential integrity — it references a talent slug that does not exist
// anywhere. Consulting the global layer must never let this identity's
// failure leak into a repo-only run.
func globalWithInvalidIdentity(t *testing.T) string {
	t.Helper()
	globalRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(globalRoot, "identities"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(globalRoot, "identities", "ghost.yaml"), []byte(
		"name: Ghost\nhandle: ghost\nkind: human\ntalents:\n  - nonexistent-talent\n"), 0o644))
	return globalRoot
}

func runValidateContent(t *testing.T, bin, repoRoot, ethosRoot, globalRoot string) (stdout string, err error) {
	t.Helper()
	cmd := exec.Command(bin, "-ethos-root", ethosRoot, "-global-root", globalRoot)
	cmd.Dir = repoRoot
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

func TestValidateContent_LayeredModeUnchanged(t *testing.T) {
	bin := buildValidateContent(t)
	repoRoot, ethosRoot := minimalRepo(t, "")
	globalRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(globalRoot, "identities"), 0o755))

	out, err := runValidateContent(t, bin, repoRoot, ethosRoot, globalRoot)
	require.NoErrorf(t, err, "layered mode (unset resolution) must still pass: %s", out)
}

func TestValidateContent_RepoOnlyExcludesGlobal(t *testing.T) {
	bin := buildValidateContent(t)
	repoRoot, ethosRoot := minimalRepo(t, "repo-only")
	globalRoot := globalWithInvalidIdentity(t)

	out, err := runValidateContent(t, bin, repoRoot, ethosRoot, globalRoot)
	require.NoErrorf(t, err, "resolution: repo-only must exclude the global identity, not fail on its bad reference: %s", out)
	require.NotContains(t, out, "ghost", "the global-only identity must not appear in validate-content output at all")
}

func TestValidateContent_LayeredModeConsultsGlobal(t *testing.T) {
	bin := buildValidateContent(t)
	repoRoot, ethosRoot := minimalRepo(t, "")
	globalRoot := globalWithInvalidIdentity(t)

	out, err := runValidateContent(t, bin, repoRoot, ethosRoot, globalRoot)
	require.Error(t, err, "layered mode must still consult global, so the bad global identity should fail the run")
	require.Contains(t, out, "ghost")
}

// writeRepoConfig writes .punt-labs/ethos.yaml under repoRoot with the
// given resolution and active_bundle settings (either may be empty to
// omit the key).
func writeRepoConfig(t *testing.T, repoRoot, resolution, activeBundle string) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".punt-labs")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	var b strings.Builder
	b.WriteString("agent: test\n")
	if resolution != "" {
		b.WriteString("resolution: " + resolution + "\n")
	}
	if activeBundle != "" {
		b.WriteString("active_bundle: " + activeBundle + "\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ethos.yaml"), []byte(b.String()), 0o600))
}

// writeIdentityYAML writes an identity file directly, bypassing
// identity.Store.Save's own ValidateRefs — that check only ever sees a
// single (non-layered) store, so it would reject exactly the
// bundle-supplied talent this test exists to prove resolves.
func writeIdentityYAML(t *testing.T, ethosRoot, handle, name string, talents []string) {
	t.Helper()
	dir := filepath.Join(ethosRoot, "identities")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	var b strings.Builder
	b.WriteString("name: " + name + "\n")
	b.WriteString("handle: " + handle + "\n")
	b.WriteString("kind: human\n")
	if len(talents) > 0 {
		b.WriteString("talents:\n")
		for _, tal := range talents {
			b.WriteString("  - " + tal + "\n")
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, handle+".yaml"), []byte(b.String()), 0o600))
}

// findResult returns the first result whose label contains substr, or nil.
func findResult(results []result, substr string) *result {
	for i := range results {
		if strings.Contains(results[i].label, substr) {
			return &results[i]
		}
	}
	return nil
}

// TestRun_RepoOnlyResolvesBundleTalent is the regression test for
// ethos-ccjz: under `resolution: repo-only`, an identity whose talent is
// supplied only by a repo-local active bundle — not by the repo's own
// .punt-labs/ethos/ — must resolve. Before the fix, validate-content built
// its identity store with no bundle layer at all, so this identity's
// referential integrity check failed even though `ethos identity show`
// would have resolved it correctly.
func TestRun_RepoOnlyResolvesBundleTalent(t *testing.T) {
	repoRoot := t.TempDir()
	ethosRoot := filepath.Join(repoRoot, ".punt-labs", "ethos")
	bundleRoot := filepath.Join(repoRoot, ".punt-labs", "ethos-bundles", "ops")
	globalRoot := filepath.Join(t.TempDir(), "global") // deliberately absent

	writeRepoConfig(t, repoRoot, "repo-only", "ops")

	// Written directly rather than via identity.Store.Save: Save's own
	// ValidateRefs only ever sees a single (non-layered) store, so it would
	// reject the bundle-supplied talent this test exists to prove resolves.
	writeIdentityYAML(t, ethosRoot, "mal", "Mal Reynolds", []string{"piloting"})

	// The talent lives ONLY in the bundle, not in ethosRoot.
	require.NoError(t, attribute.NewStore(bundleRoot, attribute.Talents).Save(&attribute.Attribute{
		Slug:    "piloting",
		Content: "# Piloting\n",
	}))

	rep, err := run(ethosRoot, globalRoot)
	require.NoError(t, err)

	refCheck := findResult(rep.results, "referential integrity")
	require.NotNil(t, refCheck, "expected a referential integrity result")
	if !refCheck.pass {
		t.Fatalf("referential integrity failed with bundle-supplied talent invisible: %s", refCheck.detail)
	}
}

// TestRun_RepoOnlyRejectsGlobalBundle is the CI-side check for repoAuthoritativeMode's
// second fatal guard: `resolution: repo-only` cannot use an active bundle
// sourced from the user's home, because it does not travel with the
// checkout. cmd/ethos exits(1) on this; validate-content must refuse the
// same way rather than silently validating a config nothing would start
// under.
func TestRun_RepoOnlyRejectsGlobalBundle(t *testing.T) {
	repoRoot := t.TempDir()
	ethosRoot := filepath.Join(repoRoot, ".punt-labs", "ethos")
	globalRoot := t.TempDir()
	globalBundleRoot := filepath.Join(globalRoot, "bundles", "ops")

	writeRepoConfig(t, repoRoot, "repo-only", "ops")
	require.NoError(t, os.MkdirAll(ethosRoot, 0o700))
	require.NoError(t, os.MkdirAll(globalBundleRoot, 0o700))

	_, err := run(ethosRoot, globalRoot)
	require.Error(t, err)
	require.Contains(t, err.Error(), "repo-only cannot use the global bundle")
}

// TestRun_LayeredModeUnaffected confirms the default (no `resolution` key)
// behavior is unchanged: an identity that resolves entirely from the repo
// layer passes with no bundle and no global store present.
func TestRun_LayeredModeUnaffected(t *testing.T) {
	repoRoot := t.TempDir()
	ethosRoot := filepath.Join(repoRoot, ".punt-labs", "ethos")
	globalRoot := filepath.Join(t.TempDir(), "global")

	require.NoError(t, identity.NewStore(ethosRoot).Save(&identity.Identity{
		Name:   "Zoe Washburne",
		Handle: "zoe",
		Kind:   "human",
	}))

	rep, err := run(ethosRoot, globalRoot)
	require.NoError(t, err)

	refCheck := findResult(rep.results, "referential integrity")
	require.NotNil(t, refCheck)
	if !refCheck.pass {
		t.Fatalf("referential integrity failed in layered mode: %s", refCheck.detail)
	}
	require.Equal(t, 1, rep.nIdentities)
}
