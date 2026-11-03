//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A gitfile naming a missing gitdir is a *dotRefusedError that names the
// gitdir from the gitfile, whichever way the git that runs words it.
func TestIsOwnGitRepoGitfileToNothingInAnyGitWording(t *testing.T) {
	for name, stderr := range map[string]string{
		"older": "fatal: not a git repository: /nonexistent",
		"newer": "fatal: gitfile does not point to a valid repository: DIR/.git",
		"null":  "fatal: not a git repository: (null)",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /nonexistent\n"), 0o644))
			fakeGitOnPath(t, "echo '"+stderr+"' >&2\nexit 128\n")
			ok, _, err := isOwnGitRepo(dir)
			assert.False(t, ok)
			var refused *dotRefusedError
			require.ErrorAs(t, err, &refused)
			assert.Contains(t, err.Error(), "not a git repository: /nonexistent")
		})
	}
}
