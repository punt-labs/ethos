package hook

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitStatus is `git status --porcelain -uall` run with the real git at gitBin.
func gitStatus(t *testing.T, gitBin, repo string) string {
	t.Helper()
	cmd := exec.Command(gitBin, "-C", repo, "status", "--porcelain", "-uall")
	cmd.Env = testhelpers.GitEnv()
	out, err := cmd.Output()
	require.NoError(t, err)
	return string(out)
}

// withoutLocks drops the flock files from a porcelain status.
func withoutLocks(status string) string {
	var keep []string
	for _, l := range strings.Split(status, "\n") {
		if l != "" && !strings.HasSuffix(l, ".lock") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

// sealedFiles lists every file under the repo's sealed sessions tree.
func sealedFiles(t *testing.T, repo string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repo, ".punt-labs", "ethos", "sessions", "*", "*"))
	require.NoError(t, err)
	return files
}

// A seal that cannot finish leaves the tree as it found it. The read-only git
// queries run first, every chunk is written, and one `git add` stages them; a
// git that hangs on the status or on the add blocks the commit, and the chunks
// this run wrote are removed, for two sessions with new lines each.
func TestSealIsAllOrNothingWhenGitHangs(t *testing.T) {
	for _, hang := range []string{"status", "add"} {
		t.Run("hang on "+hang, func(t *testing.T) {
			gitBin, err := exec.LookPath("git")
			require.NoError(t, err)
			repo := t.TempDir()
			initGitRepo(t, repo)
			now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
			// A first seal leaves chunks on disk (so phase 1 has something to
			// ask git about), then each session gets new lines.
			writeLiveLines(t, repo, "sess-one", 100, 200)
			writeLiveLines(t, repo, "sess-two", 300, 400)
			_, err = SealRepo(repo, now, SealOptions{})
			require.NoError(t, err)
			writeLiveLines(t, repo, "sess-one", 100, 200, 500, 600)
			writeLiveLines(t, repo, "sess-two", 300, 400, 700, 800)
			files := sealedFiles(t, repo)
			before := gitStatus(t, gitBin, repo)

			testhelpers.ShimGit(t, hang)
			_, err = SealRepo(repo, now, SealOptions{})
			require.Error(t, err)
			assert.ErrorIs(t, err, gitenv.ErrTimeout)
			assert.Contains(t, err.Error(), "the commit is blocked and the tree is unchanged")

			assert.Equal(t, files, sealedFiles(t, repo), "no chunk this run wrote survives")
			// The per-session flock files sit in the gitignored live zone of a real
			// repo; they are not part of the tree the seal must leave alone.
			assert.Equal(t, withoutLocks(before), withoutLocks(gitStatus(t, gitBin, repo)),
				"git status matches the state before the seal")
		})
	}

	t.Run("the next seal, with git answering, seals both", func(t *testing.T) {
		repo := t.TempDir()
		initGitRepo(t, repo)
		writeLiveLines(t, repo, "sess-one", 100, 200)
		writeLiveLines(t, repo, "sess-two", 300, 400)
		res, err := SealRepo(repo, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC), SealOptions{})
		require.NoError(t, err)
		assert.Equal(t, 2, res.SessionsSealed)
		assert.Equal(t, 2, res.ChunksStaged)
	})
}
