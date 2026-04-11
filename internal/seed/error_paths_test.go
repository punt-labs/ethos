package seed

import (
	"embed"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emptyFS is an embed.FS declared without a //go:embed directive.
// Per the embed docs it is an empty file system: every Read call
// returns a not-found error. Used to exercise the error branches in
// seedFS, seedFile, and seedReadmes that cannot be reached through
// the real package-level Roles/Talents/Readmes FSes.
var emptyFS embed.FS

// mixedFS embeds a testdata directory containing a .md file, a .txt
// file, and a subdirectory. seedFS must keep the .md, skip the .txt
// via the extension filter, and skip the subdirectory via IsDir. Both
// skip branches are unreachable through the real sidecar FSes (whose
// contents are hand-curated to match their glob patterns exactly).
//
//go:embed testdata/mixed
var mixedFS embed.FS

// TestWriteFile_ForceCreateTempFails makes filepath.Dir(dest) a
// non-existent path so MkdirAll is called; then immediately removes
// the directory so CreateTemp fails. The window between MkdirAll and
// CreateTemp is small but reliable on a single-threaded test: the
// helper uses a path that MkdirAll cannot actually create.
//
// A more reliable trick: make the parent a regular file via pre-stage,
// then force writeFile to try creating a tempfile under it. MkdirAll
// returns early (dir exists — wait, it's a file), and CreateTemp fails
// with ENOTDIR.
func TestWriteFile_ForceCreateTempFails(t *testing.T) {
	parent := t.TempDir()
	// dest's parent is a regular file, not a directory. os.MkdirAll
	// called on a path whose parent is a file returns an error
	// immediately, which is the mkdir error branch. To hit the
	// CreateTemp error branch we need MkdirAll to succeed and
	// CreateTemp to fail — which happens when the destination
	// directory exists but has no write permission.
	dir := filepath.Join(parent, "ro")
	require.NoError(t, os.MkdirAll(dir, 0o500))
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("running as root; cannot simulate write-denied directory")
	}

	r := &Result{}
	writeFile(filepath.Join(dir, "out.txt"), []byte("x"), true, r)
	require.NotEmpty(t, r.Errors)
	assert.Contains(t, r.Errors[0], "writing")
}

// TestWriteFile_NonForceOpenFails covers the non-force OpenFile error
// branch with an error other than ErrExist (permission denied).
func TestWriteFile_NonForceOpenFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; cannot simulate write-denied directory")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "ro")
	require.NoError(t, os.MkdirAll(dir, 0o500))
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	r := &Result{}
	writeFile(filepath.Join(dir, "out.txt"), []byte("x"), false, r)
	require.NotEmpty(t, r.Errors)
}

// TestWriteFile_MkdirFails makes the parent of dest a regular file,
// so MkdirAll fails with ENOTDIR.
func TestWriteFile_MkdirFails(t *testing.T) {
	parent := t.TempDir()
	// A regular file where a directory is expected.
	blocker := filepath.Join(parent, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a dir"), 0o600))

	r := &Result{}
	writeFile(filepath.Join(blocker, "child", "out.txt"), []byte("x"), false, r)
	require.NotEmpty(t, r.Errors)
	assert.Contains(t, r.Errors[0], "mkdir")
}

// TestWriteFile_NonForceSuccess and TestWriteFile_ForceSuccess exercise
// the happy paths directly, pinning the count of deployed files.
func TestWriteFile_NonForceSuccess(t *testing.T) {
	dir := t.TempDir()
	r := &Result{}
	dest := filepath.Join(dir, "out.txt")
	writeFile(dest, []byte("hello"), false, r)
	require.Empty(t, r.Errors)
	assert.Contains(t, r.Deployed, dest)

	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestWriteFile_NonForceSkipExisting(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.txt")
	require.NoError(t, os.WriteFile(dest, []byte("preexisting"), 0o600))

	r := &Result{}
	writeFile(dest, []byte("new"), false, r)
	require.Empty(t, r.Errors)
	assert.Contains(t, r.Skipped, dest)

	// Preexisting content is preserved.
	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "preexisting", string(data))
}

func TestSeedFS_SkipsDirAndWrongExtension(t *testing.T) {
	dest := t.TempDir()
	r := &Result{}
	seedFS(mixedFS, "testdata/mixed", dest, ".md", false, r)
	require.Empty(t, r.Errors)
	// keep.md was deployed.
	assert.FileExists(t, filepath.Join(dest, "keep.md"))
	// skip.txt and sub/ were skipped.
	_, err := os.Stat(filepath.Join(dest, "skip.txt"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(dest, "sub"))
	assert.True(t, os.IsNotExist(err))
}

func TestSeedFS_ReadDirError(t *testing.T) {
	r := &Result{}
	seedFS(emptyFS, "nonexistent", t.TempDir(), ".yaml", false, r)
	require.NotEmpty(t, r.Errors)
	assert.Contains(t, r.Errors[0], "reading")
	assert.Contains(t, r.Errors[0], "nonexistent")
}

func TestSeedFile_ReadError(t *testing.T) {
	r := &Result{}
	seedFile(emptyFS, "nonexistent/file.md",
		filepath.Join(t.TempDir(), "out.md"), false, r)
	require.NotEmpty(t, r.Errors)
	assert.Contains(t, r.Errors[0], "reading")
}

func TestSeedReadmes_WalkError(t *testing.T) {
	// emptyFS has no "sidecar" root. fs.WalkDir invokes the callback
	// once with walkErr set ("open sidecar: file does not exist") and
	// then returns nil. The callback's walkErr branch records the
	// error as "walking sidecar: ..." — the outer `if err != nil`
	// block is not reached.
	r := &Result{}
	seedReadmes(emptyFS, t.TempDir(), false, r)
	require.NotEmpty(t, r.Errors)
	var found bool
	for _, e := range r.Errors {
		if strings.Contains(e, "walking") {
			found = true
		}
	}
	assert.True(t, found, "errors: %v", r.Errors)
}

// TestSeed_MkdirError makes the dest root a regular file so every
// os.MkdirAll inside writeFile fails with ENOTDIR. Every file-level
// write records an error, Seed returns a non-nil error, and the errors
// surface for every category (roles, talents, skills, readmes).
func TestSeed_MkdirError(t *testing.T) {
	// dest is a file, not a directory. MkdirAll(filepath.Dir(dest/...))
	// will try to create subdirs under the file and fail.
	parent := t.TempDir()
	dest := filepath.Join(parent, "blocked")
	require.NoError(t, os.WriteFile(dest, []byte("not a dir"), 0o600))

	skills := t.TempDir()

	result, err := Seed(dest, skills, false)
	require.Error(t, err)
	assert.NotEmpty(t, result.Errors)
	// Every error should mention mkdir or writing.
	for _, e := range result.Errors {
		assert.True(t,
			strings.Contains(e, "mkdir") || strings.Contains(e, "writing"),
			"error should reference mkdir or writing: %s", e)
	}
}

// TestSeed_ReadOnlyDest exercises the write-error path on a directory
// that exists but has no write permission.
func TestSeed_ReadOnlyDest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; cannot simulate write-denied directory")
	}
	dest := t.TempDir()
	skills := t.TempDir()

	// Pre-create roles as a read-only directory. MkdirAll on an
	// existing directory is a no-op, so writeFile proceeds to
	// OpenFile which fails with EACCES.
	rolesDir := filepath.Join(dest, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o500))
	t.Cleanup(func() { os.Chmod(rolesDir, 0o700) })

	result, err := Seed(dest, skills, false)
	require.Error(t, err)

	// At least one error should reference the roles directory.
	var sawRolesError bool
	for _, e := range result.Errors {
		if strings.Contains(e, "roles") {
			sawRolesError = true
		}
	}
	assert.True(t, sawRolesError, "errors: %v", result.Errors)
}

// TestSeed_ForceReadOnlyDest exercises the force branch's rename error
// path. The force path opens a tempfile in filepath.Dir(dest); when
// that directory is not writable, CreateTemp fails and the error is
// recorded via the r.Errors slice.
func TestSeed_ForceReadOnlyDest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; cannot simulate write-denied directory")
	}
	dest := t.TempDir()
	skills := t.TempDir()

	rolesDir := filepath.Join(dest, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o500))
	t.Cleanup(func() { os.Chmod(rolesDir, 0o700) })

	result, err := Seed(dest, skills, true)
	require.Error(t, err)
	assert.NotEmpty(t, result.Errors)
}

// TestSeed_SkillsPathBlocked exercises the seedFile error surface by
// making the skills destination unwritable. seedFile reads from the
// embedded FS successfully, then writeFile fails to mkdir under a
// file.
func TestSeed_SkillsPathBlocked(t *testing.T) {
	dest := t.TempDir()
	parent := t.TempDir()
	skills := filepath.Join(parent, "skills")
	require.NoError(t, os.WriteFile(skills, []byte("not a dir"), 0o600))

	result, err := Seed(dest, skills, false)
	require.Error(t, err)
	// seedFile reads the embedded SKILL.md, then writeFile's MkdirAll
	// fails with ENOTDIR because the skills destination is a regular
	// file, not a directory. The wrapper in writeFile prefixes "mkdir".
	var sawMkdirError bool
	for _, e := range result.Errors {
		if strings.Contains(e, "mkdir") &&
			strings.Contains(e, "not a directory") {
			sawMkdirError = true
		}
	}
	assert.True(t, sawMkdirError, "errors: %v", result.Errors)
}

// TestSeed_ForceRenameFails exercises the Rename error branch in the
// force path by making the destination an existing non-empty directory
// at the path where the file should land. Rename then returns
// ENOTDIR (or ISDIR) and writeFile records the error.
func TestSeed_ForceRenameFails(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// Pre-create a directory at the exact path where implementer.yaml
	// should be written. os.Rename onto a non-empty directory fails.
	roleDir := filepath.Join(dest, "roles", "implementer.yaml")
	require.NoError(t, os.MkdirAll(roleDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(roleDir, "sentinel"), []byte("x"), 0o600))

	result, err := Seed(dest, skills, true)
	require.Error(t, err)

	var sawRename bool
	for _, e := range result.Errors {
		if strings.Contains(e, "renaming") ||
			strings.Contains(e, "implementer.yaml") {
			sawRename = true
		}
	}
	assert.True(t, sawRename, "errors: %v", result.Errors)
}

// TestSeed_NonForceWriteFails exercises writeFile's non-force error
// path where OpenFile fails with something other than ErrExist.
// Setting the parent directory to 0o500 triggers EACCES on O_CREATE.
func TestSeed_NonForceWriteFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; cannot simulate write-denied directory")
	}
	dest := t.TempDir()
	skills := t.TempDir()

	// Create an existing writable roles dir first so MkdirAll is a
	// no-op and the file-level OpenFile is what fails.
	talentsDir := filepath.Join(dest, "talents")
	require.NoError(t, os.MkdirAll(talentsDir, 0o500))
	t.Cleanup(func() { os.Chmod(talentsDir, 0o700) })

	result, err := Seed(dest, skills, false)
	require.Error(t, err)
	var sawTalentError bool
	for _, e := range result.Errors {
		if strings.Contains(e, "talents") || strings.Contains(e, ".md") {
			sawTalentError = true
		}
	}
	assert.True(t, sawTalentError, "errors: %v", result.Errors)
}

// TestSeed_ForceOverwrite exercises the full force=true rename path
// (CreateTemp + Write + Close + Chmod + Rename) for several files at
// once. The baseline TestSeedForce already covers one file; doing
// multiple categories here ensures the rename success path is taken
// for talents, roles, and readmes in the same run.
func TestSeed_ForceOverwriteAllCategories(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// Seed once with defaults.
	_, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// Seed again with force — every file should be rewritten via the
	// tempfile+rename path.
	result, err := Seed(dest, skills, true)
	require.NoError(t, err)
	assert.Empty(t, result.Skipped)
	assert.Empty(t, result.Errors)
	assert.NotEmpty(t, result.Deployed)

	// Spot check a force-rewritten file has 0o644 perms.
	info, err := os.Stat(filepath.Join(dest, "roles", "implementer.yaml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}
