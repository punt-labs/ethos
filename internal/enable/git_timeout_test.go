package enable

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every git question enable asks is bounded; a git that does not answer is
// an error naming the timeout (not "no tracked files", not "ignored").
func TestEnableGitTimeouts(t *testing.T) {
	repo := t.TempDir()
	testhelpers.HangingGit(t)

	t.Run("EnableTo", func(t *testing.T) {
		_, err := EnableTo(repo, repo)
		require.Error(t, err)
		assert.Contains(t, err.Error(), gitenv.ErrTimeout.Error())
	})
	t.Run("gitExcludes", func(t *testing.T) {
		_, err := gitExcludes(repo, []string{"a"})
		assert.ErrorIs(t, err, gitenv.ErrTimeout)
	})
	t.Run("trackedRuntimeFiles", func(t *testing.T) {
		files, err := trackedRuntimeFiles(repo)
		require.ErrorIs(t, err, gitenv.ErrTimeout)
		assert.Empty(t, files)
	})
	t.Run("enabledSiblingWorktrees", func(t *testing.T) {
		got, err := enabledSiblingWorktrees(repo)
		require.ErrorIs(t, err, gitenv.ErrTimeout)
		assert.Empty(t, got)
	})
}

// A .git git cannot use is an error for LocalIgnored, not "nothing covered".
func TestLocalIgnoredBrokenGitDirIsAnError(t *testing.T) {
	for name, dir := range testhelpers.BrokenGitDirs(t) {
		t.Run(name, func(t *testing.T) {
			got, err := LocalIgnored(dir)
			require.Error(t, err)
			assert.False(t, got)
		})
	}
}
