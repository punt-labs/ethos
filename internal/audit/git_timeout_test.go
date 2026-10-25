package audit

import (
	"testing"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every git command the audit package runs is bounded; a git that does not
// answer is an error naming the timeout, never a hang and never an empty or
// "false" answer.
func TestGitCommandsTimeOut(t *testing.T) {
	dir := t.TempDir()
	testhelpers.HangingGit(t)
	t.Run("GitAdd", func(t *testing.T) {
		assert.ErrorIs(t, GitAdd(dir, "a"), gitenv.ErrTimeout)
	})
	t.Run("GitMv", func(t *testing.T) {
		assert.ErrorIs(t, GitMv(dir, "a", "b"), gitenv.ErrTimeout)
	})
	t.Run("IsGitlinkMount", func(t *testing.T) {
		got, err := IsGitlinkMount(dir)
		assert.ErrorIs(t, err, gitenv.ErrTimeout)
		assert.False(t, got)
	})
	t.Run("UntrackedOrModified", func(t *testing.T) {
		got, err := UntrackedOrModified(dir, []string{"a"})
		assert.ErrorIs(t, err, gitenv.ErrTimeout)
		assert.Nil(t, got)
	})
	t.Run("RepoIdentity", func(t *testing.T) {
		got, err := RepoIdentity(dir)
		require.ErrorIs(t, err, gitenv.ErrTimeout)
		assert.Empty(t, got)
	})
}
