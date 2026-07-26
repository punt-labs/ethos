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

// testSeeder builds a seeder with an empty manifest for unit-testing the
// per-file placement branches directly.
func testSeeder(destRoot, skillsRoot string, force bool) *seeder {
	return &seeder{
		destRoot:   destRoot,
		skillsRoot: skillsRoot,
		force:      force,
		mf:         &Manifest{Schema: manifestSchema, Entries: map[string]Entry{}},
		r:          &Result{},
	}
}

// TestPlace_ForceCreateTempFails exercises the place branch where
// os.MkdirAll(filepath.Dir(dest)) succeeds but the atomic create in that
// directory fails. It does so by pre-creating the destination directory
// without write permission, so the directory already exists for MkdirAll but
// cannot accept a new tempfile.
//
// Skipped as root, which may still create files in a permission-restricted
// directory, making the intended failure unreliable.
func TestPlace_ForceCreateTempFails(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "ro")
	require.NoError(t, os.MkdirAll(dir, 0o500))
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("running as root; cannot simulate write-denied directory")
	}

	s := testSeeder(parent, "", true)
	s.place(scopeEthos, filepath.Join(dir, "out.txt"), []byte("x"))
	require.NotEmpty(t, s.r.Errors)
	assert.Contains(t, s.r.Errors[0], "writing")
}

// TestPlace_NonForceCreateFails covers the non-force create error branch with
// an error other than ErrExist (permission denied).
func TestPlace_NonForceCreateFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; cannot simulate write-denied directory")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "ro")
	require.NoError(t, os.MkdirAll(dir, 0o500))
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	s := testSeeder(parent, "", false)
	s.place(scopeEthos, filepath.Join(dir, "out.txt"), []byte("x"))
	require.NotEmpty(t, s.r.Errors)
}

// TestPlace_MkdirFails makes the parent of dest a regular file, so MkdirAll
// fails with ENOTDIR.
func TestPlace_MkdirFails(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a dir"), 0o600))

	s := testSeeder(parent, "", false)
	s.place(scopeEthos, filepath.Join(blocker, "child", "out.txt"), []byte("x"))
	require.NotEmpty(t, s.r.Errors)
	assert.Contains(t, s.r.Errors[0], "mkdir")
}

// TestPlace_FreshDeploy exercises the happy create path, pinning that a fresh
// file is deployed and recorded.
func TestPlace_FreshDeploy(t *testing.T) {
	dir := t.TempDir()
	s := testSeeder(dir, "", false)
	dest := filepath.Join(dir, "out.txt")
	s.place(scopeEthos, dest, []byte("hello"))
	require.Empty(t, s.r.Errors)
	assert.Contains(t, s.r.Deployed, dest)

	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))

	// A fresh deploy is recorded in the manifest.
	entry, ok := s.mf.Entries[s.key(scopeEthos, dest)]
	require.True(t, ok, "deployed file must be recorded")
	assert.Equal(t, hashBytes([]byte("hello")), entry.Hash)
}

// TestPlace_UntrackedDifferingFileSkips pins the no-clobber rule: a file that
// exists, differs from the shipped content, and has no manifest entry is left
// untouched and reported as skipped — never overwritten, never recorded.
func TestPlace_UntrackedDifferingFileSkips(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.txt")
	require.NoError(t, os.WriteFile(dest, []byte("preexisting"), 0o600))

	s := testSeeder(dir, "", false)
	s.place(scopeEthos, dest, []byte("new"))
	require.Empty(t, s.r.Errors)
	assert.Contains(t, s.r.Skipped, dest)

	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "preexisting", string(data),
		"an untracked differing file must not be clobbered")

	// It is not recorded — it has not entered the manifest era.
	_, ok := s.mf.Entries[s.key(scopeEthos, dest)]
	assert.False(t, ok, "an untracked skip must not create a manifest entry")
}

func TestSeedFS_SkipsDirAndWrongExtension(t *testing.T) {
	dest := t.TempDir()
	s := testSeeder(dest, "", false)
	s.seedFS(mixedFS, "testdata/mixed", dest, ".md")
	require.Empty(t, s.r.Errors)
	// keep.md was deployed.
	assert.FileExists(t, filepath.Join(dest, "keep.md"))
	// skip.txt and sub/ were skipped.
	_, err := os.Stat(filepath.Join(dest, "skip.txt"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(dest, "sub"))
	assert.True(t, os.IsNotExist(err))
}

func TestSeedFS_ReadDirError(t *testing.T) {
	dir := t.TempDir()
	s := testSeeder(dir, "", false)
	s.seedFS(emptyFS, "nonexistent", dir, ".yaml")
	require.NotEmpty(t, s.r.Errors)
	assert.Contains(t, s.r.Errors[0], "reading")
	assert.Contains(t, s.r.Errors[0], "nonexistent")
}

func TestSeedFile_ReadError(t *testing.T) {
	dir := t.TempDir()
	s := testSeeder(dir, dir, false)
	s.seedFile(emptyFS, "nonexistent/file.md", filepath.Join(dir, "out.md"))
	require.NotEmpty(t, s.r.Errors)
	assert.Contains(t, s.r.Errors[0], "reading")
}

func TestSeedReadmes_WalkError(t *testing.T) {
	// emptyFS has no "sidecar" root. fs.WalkDir invokes the callback
	// once with walkErr set ("open sidecar: file does not exist") and
	// then returns nil. The callback's walkErr branch records the
	// error as "walking sidecar: ..." — the outer `if err != nil`
	// block is not reached.
	dir := t.TempDir()
	s := testSeeder(dir, "", false)
	s.seedReadmes(emptyFS, dir)
	require.NotEmpty(t, s.r.Errors)
	var found bool
	for _, e := range s.r.Errors {
		if strings.Contains(e, "walking") {
			found = true
		}
	}
	assert.True(t, found, "errors: %v", s.r.Errors)
}

// TestSeed_MkdirError makes the dest root a regular file so every
// os.MkdirAll inside place fails with ENOTDIR. Every file-level write records
// an error, Seed returns a non-nil error, and the errors surface.
func TestSeed_MkdirError(t *testing.T) {
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
	// existing directory is a no-op, so place proceeds to the atomic
	// create, which fails with EACCES.
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

// TestSeed_ForceReadOnlyDest exercises the force branch's create error path.
// The atomic create opens a tempfile in filepath.Dir(dest); when that
// directory is not writable, the create fails and the error is recorded.
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
// embedded FS successfully, then place fails to mkdir under a file.
func TestSeed_SkillsPathBlocked(t *testing.T) {
	dest := t.TempDir()
	parent := t.TempDir()
	skills := filepath.Join(parent, "skills")
	require.NoError(t, os.WriteFile(skills, []byte("not a dir"), 0o600))

	result, err := Seed(dest, skills, false)
	require.Error(t, err)
	// seedFile reads the embedded SKILL.md, then place's MkdirAll fails
	// with ENOTDIR because the skills destination is a regular file, not
	// a directory. The wrapper in place prefixes "mkdir".
	var sawMkdirError bool
	for _, e := range result.Errors {
		if strings.Contains(e, "mkdir") &&
			strings.Contains(e, "not a directory") {
			sawMkdirError = true
		}
	}
	assert.True(t, sawMkdirError, "errors: %v", result.Errors)
}

// TestSeed_DirAtForceDest pins that a directory at a file dest is a hard
// error even under force: the guard fires before any write.
func TestSeed_DirAtForceDest(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// Pre-create a directory at the exact path where implementer.yaml
	// should be written.
	roleDir := filepath.Join(dest, "roles", "implementer.yaml")
	require.NoError(t, os.MkdirAll(roleDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(roleDir, "sentinel"), []byte("x"), 0o600))

	result, err := Seed(dest, skills, true)
	require.Error(t, err)

	var named bool
	for _, e := range result.Errors {
		if strings.Contains(e, "implementer.yaml") &&
			strings.Contains(e, "directory") {
			named = true
		}
	}
	assert.True(t, named, "errors: %v", result.Errors)
}

// TestSeed_NonForceWriteFails exercises the create error path where the
// atomic create fails with something other than ErrExist. Setting the
// parent directory to 0o500 triggers EACCES.
func TestSeed_NonForceWriteFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; cannot simulate write-denied directory")
	}
	dest := t.TempDir()
	skills := t.TempDir()

	// Create the existing talents dir with non-writable permissions so
	// MkdirAll is a no-op and the file-level create is what fails.
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

// TestSeed_ForceOverwritesEdit exercises the full force overwrite path: a
// tracked file the user has edited is rewritten to the shipped content and
// reported as updated, with 0o644 perms and an empty skipped list.
func TestSeed_ForceOverwritesEdit(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// Seed once so implementer.yaml is tracked.
	_, err := Seed(dest, skills, false)
	require.NoError(t, err)

	// Edit the tracked file.
	rolePath := filepath.Join(dest, "roles", "implementer.yaml")
	require.NoError(t, os.WriteFile(rolePath, []byte("edited"), 0o644))

	// Force re-seed overwrites the edit.
	result, err := Seed(dest, skills, true)
	require.NoError(t, err)
	assert.Empty(t, result.Skipped)
	assert.Empty(t, result.Errors)
	assert.Contains(t, result.Updated, rolePath)

	data, err := os.ReadFile(rolePath)
	require.NoError(t, err)
	assert.NotEqual(t, "edited", string(data), "force must overwrite the edit")

	info, err := os.Stat(rolePath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}
