package audit

import (
	"os"
	"os/exec"
	"path/filepath"
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

func fakeGitOnPath(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+body), 0o755))
	t.Setenv("PATH", dir)
}

// A git failure is not "no gitlink" and not an empty identity: not a
// repository and no remote are the only absent answers.
func TestGitReadsClassifyFailures(t *testing.T) {
	dir := t.TempDir()
	t.Run("not a repository is absent", func(t *testing.T) {
		fakeGitOnPath(t, "echo 'fatal: not a git repository (or any of the parent directories): .git' >&2\nexit 128\n")
		g, err := IsGitlinkMount(dir)
		require.NoError(t, err)
		assert.False(t, g)
		id, err := RepoIdentity(dir)
		require.NoError(t, err)
		assert.Empty(t, id)
	})
	t.Run("no origin is absent", func(t *testing.T) {
		fakeGitOnPath(t, "echo 'error: No such remote' >&2\nexit 2\n")
		id, err := RepoIdentity(dir)
		require.NoError(t, err)
		assert.Empty(t, id)
	})
	t.Run("dubious ownership is an error", func(t *testing.T) {
		fakeGitOnPath(t, "echo 'fatal: detected dubious ownership' >&2\nexit 128\n")
		g, err := IsGitlinkMount(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dubious ownership")
		assert.False(t, g)
		_, err = RepoIdentity(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dubious ownership")
	})
	t.Run("git not installed is an error", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := IsGitlinkMount(dir)
		assert.ErrorIs(t, err, exec.ErrNotFound)
		_, err = RepoIdentity(dir)
		assert.ErrorIs(t, err, exec.ErrNotFound)
	})
}
