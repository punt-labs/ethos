package hook

import (
	"os/exec"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Blame archival must not ask Git about status, ignores or the index, even when
// Git is unavailable. New healthy tails are retained independently of Git.
func TestSealBlameArchivesWhenGitHangs(t *testing.T) {
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
			before := testhelpers.GitCommand("diff", "--cached", "--name-status")
			before.Dir = repo
			indexBefore, err := before.Output()
			require.NoError(t, err)

			testhelpers.ShimGit(t, hang)
			res, err := SealRepo(repo, now, SealOptions{})
			require.NoError(t, err)
			assert.Equal(t, 2, res.SessionsSealed)
			assert.Equal(t, 4, res.LinesSealed)
			assert.Zero(t, res.ChunksStaged)
			p, err := auditPolicy(repo)
			require.NoError(t, err)
			for _, id := range []string{"sess-one", "sess-two"} {
				n, err := audit.UnarchivedCount(p, audit.Stream{Namespace: audit.SessionNS, Session: id})
				require.NoError(t, err)
				assert.Zero(t, n)
			}
			after := exec.Command(gitBin, "diff", "--cached", "--name-status")
			after.Dir = repo
			indexAfter, err := after.Output()
			require.NoError(t, err)
			assert.Equal(t, indexBefore, indexAfter, "local archival preserves index")
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
		assert.Zero(t, res.ChunksStaged)
	})
}
