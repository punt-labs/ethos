package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

const ignoreSessions = ".punt-labs/ethos/sessions/\n"

// A damaged chunk in a gitignored session is local only: the seal skips that
// session with one line naming the chunk and the recovery command, and seals
// the others.
func TestSealRepo_DamagedChunkInIgnoredSessionIsSkipped(t *testing.T) {
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
			chunk := damage(t, badDir)
			writeLiveLines(t, repo, "sess-bad", 300)
			writeLiveLines(t, repo, "sess-good", 100, 200)

			var res SealResult
			var err error
			stderr := captureStderr(t, func() { res, err = SealRepo(repo, now, SealOptions{}) })

			require.NoError(t, err)
			assert.Equal(t, 1, res.SessionsSealed, "only the undamaged session seals")
			goodDir, _ := resolveRepoSessionDir(repo, "sess-good", now)
			assert.FileExists(t, filepath.Join(goodDir, audit.SessionChunkFile(100, 200)))
			lines := strings.Split(strings.TrimSpace(stderr), "\n")
			require.Len(t, lines, 1, "stderr: %q", stderr)
			assert.Contains(t, lines[0], "ethos audit quarantine "+filepath.Join(badDir, chunk))
			assert.Contains(t, lines[0], "sess-bad")
		})
	}
}

// The same damage in a tracked session still blocks the commit.
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
