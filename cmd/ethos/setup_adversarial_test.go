//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Adversarial CLI tests for the setup-consistency work: attribute files
// that vanish or become unreadable mid-resolution, and a seed whose
// destination cannot be written. Each asserts the binary degrades
// gracefully (actionable warning, no crash) or fails loud (non-zero exit,
// nothing silently claimed), and pins the actual layered semantic rather
// than an assumed one.

// seededFoundationEnv returns a fresh env with content seeded and the
// foundation bundle set up, ready for a test to corrupt.
func seededFoundationEnv(t *testing.T) *cliSubprocessEnv {
	t.Helper()
	se := freshCLIEnv(t, "tester")
	_, stderr, code := runCLI(t, se, "seed")
	require.Equal(t, 0, code, "seed: %s", stderr)
	answers := writeAnswers(t, se.repo, "name: Tester\nhandle: tester\nbundle: foundation\n")
	_, stderr, code = runCLI(t, se, "setup", "--file", answers, "--bundle", "foundation")
	require.Equal(t, 0, code, "setup: %s", stderr)
	return se
}

// TestCLI_MissingBundleAttr_FallsThroughThenWarns pins the layered
// degradation for a bundle attribute that disappears from disk. Deleting
// only the bundle copy is masked by the global-seed belt (Part B): the
// slug still resolves, no warning. Deleting the global copy too leaves the
// slug absent from every layer — resolution then emits an actionable
// warning naming the slug and the path, and `show` still exits 0 (a
// missing attribute degrades, it does not brick the command).
func TestCLI_MissingBundleAttr_FallsThroughThenWarns(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := seededFoundationEnv(t)

	ethosRoot := filepath.Join(se.home, ".punt-labs", "ethos")
	bundleAttr := filepath.Join(ethosRoot, "bundles", "foundation", "talents", "engineering.md")
	globalAttr := filepath.Join(ethosRoot, "talents", "engineering.md")

	// foundation-architect is a bundle identity referencing talent
	// "engineering". Sanity: it resolves cleanly before any corruption.
	_, _, errOut, code := showJSON(t, se, "foundation-architect")
	require.Equal(t, 0, code)
	require.NotContains(t, errOut, "warning:")

	// Delete the bundle copy only: the global seed copy catches the tail,
	// so resolution still succeeds with zero warnings. This is the
	// belt-and-suspenders behavior working, not a bug.
	require.NoError(t, os.Remove(bundleAttr))
	_, _, errOut, code = showJSON(t, se, "foundation-architect")
	require.Equal(t, 0, code, "still exits 0 after bundle-copy delete; stderr=%s", errOut)
	assert.NotContains(t, errOut, "warning:",
		"deleting only the bundle copy must fall through to the global seed; stderr=%s", errOut)

	// Now delete the global copy too: the slug is absent from every layer.
	require.NoError(t, os.Remove(globalAttr))
	stdout, errOut, code := runCLI(t, se, "show", "foundation-architect")
	assert.Equal(t, 0, code, "missing attribute must warn, not crash; stdout=%s stderr=%s", stdout, errOut)
	assert.Contains(t, errOut, "warning:", "expected a resolution warning; stderr=%s", errOut)
	assert.Contains(t, errOut, `talent "engineering"`, "warning must name the missing slug")
	assert.Contains(t, errOut, "not found", "warning must say the attribute was not found")
}

// TestCLI_UnreadableGlobalAttr_WarnsGracefully covers an attribute file
// that exists but cannot be read. Attribute content is plain markdown with
// no schema, so there is no parse step to fail — the failure mode is an
// inaccessible path. Replacing the file with a directory of the same name
// forces os.ReadFile to error on every platform regardless of uid (unlike
// chmod, which root bypasses). `show` must name the personality and exit 0.
func TestCLI_UnreadableGlobalAttr_WarnsGracefully(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := seededFoundationEnv(t)

	// claude's personality "principal-engineer" lives only in the global
	// layer. Replace the file with a directory so it cannot be read.
	attr := filepath.Join(se.home, ".punt-labs", "ethos", "personalities", "principal-engineer.md")
	require.NoError(t, os.Remove(attr))
	require.NoError(t, os.Mkdir(attr, 0o755))

	stdout, errOut, code := runCLI(t, se, "show", "claude")
	assert.Equal(t, 0, code, "unreadable attribute must warn, not crash; stdout=%s stderr=%s", stdout, errOut)
	assert.Contains(t, errOut, "warning:", "expected a resolution warning; stderr=%s", errOut)
	assert.Contains(t, errOut, `personality "principal-engineer"`,
		"warning must name the unreadable personality; stderr=%s", errOut)
}

// TestCLI_SeedFailsLoud_UnwritableDest pins seed's behavior when its
// destination cannot be written. A regular file at ~/.punt-labs blocks
// MkdirAll for every ethos attribute path (uid-independent — you cannot
// create a directory under a file even as root). Seed is best-effort
// per-file: it aggregates per-file errors, reports them, and exits
// non-zero rather than claiming success. Here every ethos write fails, so
// the ethos tree is never created — nothing half-deployed under it.
func TestCLI_SeedFailsLoud_UnwritableDest(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := t.TempDir()
	// Block the ethos destination: ~/.punt-labs is a file, not a dir.
	require.NoError(t, os.WriteFile(filepath.Join(home, ".punt-labs"), []byte("x"), 0o644))

	se := &cliSubprocessEnv{
		home: home,
		repo: t.TempDir(),
		env: []string{
			"HOME=" + home,
			"USER=tester",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"PATH=" + os.Getenv("PATH"),
		},
	}

	stdout, stderr, code := runCLI(t, se, "seed")
	require.NotEqual(t, 0, code, "seed must exit non-zero when its dest is unwritable; stdout=%s", stdout)
	assert.Contains(t, stderr, "error", "seed must report the write failure on stderr; stderr=%s", stderr)

	// The ethos tree was never created — the blocking path is still a file.
	info, statErr := os.Stat(filepath.Join(home, ".punt-labs"))
	require.NoError(t, statErr)
	assert.False(t, info.IsDir(), ".punt-labs must remain the blocking file, not a directory")
	assert.NoDirExists(t, filepath.Join(home, ".punt-labs", "ethos"),
		"no ethos content should be half-deployed under the blocked path")
}
