//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hangGit puts a git that never answers first on PATH and shortens the
// deadline for the test.
func hangGit(t *testing.T) {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nexec "+sleep+" 60\n"), 0o755))
	t.Setenv("PATH", dir)
	testhelpers.GitTimeout(t, 200*time.Millisecond)
}

// team for-repo with no argument reports a git that hung reading the
// remote, on the same error path as "no git remote".
func TestTeamForRepoGitTimeout(t *testing.T) {
	t.Chdir(t.TempDir())
	hangGit(t)
	err := runTeamForRepo(&cobra.Command{}, "")
	require.ErrorIs(t, err, gitenv.ErrTimeout)
	assert.Contains(t, err.Error(), "could not determine repo name")
}

// setup --file with no email reports a git that hung reading user.email,
// on the same error path as an unset user.email.
func TestSetupFileGitTimeout(t *testing.T) {
	_, repo := setupTestEnv(t)
	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, "name: Test User\nsolo: true\n")
	setupFile = cfgPath
	hangGit(t)
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	err := runSetup(cmd)
	require.ErrorIs(t, err, gitenv.ErrTimeout)
	assert.Contains(t, err.Error(), "user.email")
}

// Interactive setup offers no email default when the git read hung, and
// says why on stderr rather than leaving the prompt silently empty.
func TestSetupInteractiveGitTimeout(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString("Test User\n\nme@example.com\n\n\n\n\n\n")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; _ = r.Close() })
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	hangGit(t)

	var errb bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&errb)
	cfg, err := setupInteractive(cmd)
	require.NoError(t, err)
	assert.Equal(t, "me@example.com", cfg.Email)
	assert.Contains(t, errb.String(), "did not answer")
	assert.Contains(t, errb.String(), "Email: ", "no default is offered")
}
