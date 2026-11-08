package hook

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

const ignoreSessions = ".punt-labs/ethos/sessions/\n"

// A manual seal reports damage even in ignored history, but still archives
// independent healthy streams. Only the CLI commit-context may soften errors.
func TestSealRepo_DamagedIgnoredStreamDoesNotStopHealthyArchive(t *testing.T) {
	now := time.Now().UTC()
	cases := map[string]func(t *testing.T, dir string) string{
		"corrupt chunk": func(t *testing.T, dir string) string {
			// Last ts 150 disagrees with the filename's 200.
			name := audit.SessionChunkFile(100, 200)
			writeChunkFile(t, dir, name, 100, 150)
			return name
		},
		"orphan corrupt": func(t *testing.T, dir string) string {
			name := audit.SessionChunkFile(100, 200)
			writeChunkFile(t, dir, name+".corrupt", 100, 200)
			return name
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			initGitRepo(t, repo)
			require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(ignoreSessions), 0o600))
			badDir, _ := resolveRepoSessionDir(repo, "sess-bad", now)
			damage(t, badDir)
			writeLiveLines(t, repo, "sess-bad", 300)
			writeLiveLines(t, repo, "sess-good", 100, 200)

			res, err := SealRepo(repo, now, SealOptions{})
			require.Error(t, err)
			assert.Equal(t, 1, res.SessionsSealed, "only the undamaged session seals")
			goodDir := archiveSessionDir(t, repo, "sess-good", "1970-01-01")
			assert.FileExists(t, filepath.Join(goodDir, audit.SessionChunkFile(100, 200)))
			assert.Contains(t, err.Error(), badDir)
			assert.Contains(t, err.Error(), "live data retained")
			assert.FileExists(t, liveAuditPath(repo, "sess-bad"))
		})
	}
}

// Manual archival reports damage regardless of Git's tracking state.
func TestSealRepo_DamagedChunkInTrackedSessionStillBlocks(t *testing.T) {
	now := time.Now().UTC()
	repo := t.TempDir()
	initGitRepo(t, repo)
	badDir, _ := resolveRepoSessionDir(repo, "sess-bad", now)
	writeChunkFile(t, badDir, audit.SessionChunkFile(100, 200), 100, 150)
	writeLiveLines(t, repo, "sess-bad", 300)

	_, err := SealRepo(repo, now, SealOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "corrupt chunk")
}

func gitIn(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := testhelpers.GitCommand(args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// A repo that committed session chunks and later gitignored the directory
// still tracks them: damage there blocks, and so does a damaged chunk beside a
// tracked good one.
func TestSealRepo_TrackedChunkInIgnoredDirStillBlocks(t *testing.T) {
	now := time.Now().UTC()
	repo := t.TempDir()
	initGitRepo(t, repo)
	dir, _ := resolveRepoSessionDir(repo, "sess-t", now)
	writeChunkFile(t, dir, audit.SessionChunkFile(100, 200), 100, 200)
	gitIn(t, repo, "add", "-f", ".punt-labs")
	gitIn(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-m", "chunk")
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(ignoreSessions), 0o600))
	// A later chunk is damaged: the tracked one beside it is why this blocks.
	writeChunkFile(t, dir, audit.SessionChunkFile(300, 400), 300, 350)
	writeLiveLines(t, repo, "sess-t", 500)

	_, err := SealRepo(repo, now, SealOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "corrupt chunk")
}

// A tracked chunk staged as renamed to .corrupt, with no marker, is an
// interrupted quarantine in a tracked tree: the original path is no longer in
// the index and matches the ignore rule, but the rename is staged.
func TestSealRepo_StagedRenameToCorruptInIgnoredDirStillBlocks(t *testing.T) {
	now := time.Now().UTC()
	repo := t.TempDir()
	initGitRepo(t, repo)
	dir, _ := resolveRepoSessionDir(repo, "sess-t", now)
	name := audit.SessionChunkFile(100, 200)
	writeChunkFile(t, dir, name, 100, 200)
	gitIn(t, repo, "add", "-f", ".punt-labs")
	gitIn(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-m", "chunk")
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(ignoreSessions), 0o600))
	gitIn(t, repo, "mv", filepath.Join(dir, name), filepath.Join(dir, name+".corrupt"))
	writeLiveLines(t, repo, "sess-t", 300)

	_, err := SealRepo(repo, now, SealOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "orphan")
}

// A chunk that cannot be read (permissions) is not damaged: quarantine would
// not help, so even a gitignored session blocks.
func TestSealRepo_UnreadableChunkInIgnoredSessionStillBlocks(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	now := time.Now().UTC()
	repo := t.TempDir()
	initGitRepo(t, repo)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(ignoreSessions), 0o600))
	dir, _ := resolveRepoSessionDir(repo, "sess-u", now)
	name := audit.SessionChunkFile(100, 200)
	writeChunkFile(t, dir, name, 100, 200)
	require.NoError(t, os.Chmod(filepath.Join(dir, name), 0o000))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, name), 0o600) })

	var err error
	stderr := captureStderr(t, func() { _, err = SealRepo(repo, now, SealOptions{}) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading chunk")
	assert.NotContains(t, stderr, "ethos audit quarantine")
}

// Diagnostic paths retain spaces and quotes, without being interpreted as shell.
func TestSealRepo_DamageDiagnosticPreservesQuotedPath(t *testing.T) {
	now := time.Now().UTC()
	repo := filepath.Join(t.TempDir(), "my repo's checkout")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	initGitRepo(t, repo)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(ignoreSessions), 0o600))
	dir, _ := resolveRepoSessionDir(repo, "sess-q", now)
	name := audit.SessionChunkFile(100, 200)
	writeChunkFile(t, dir, name, 100, 150)
	writeLiveLines(t, repo, "sess-q", 300)

	_, err := SealRepo(repo, now, SealOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), filepath.Join(dir, name))
	assert.Contains(t, err.Error(), "live data retained")
}
