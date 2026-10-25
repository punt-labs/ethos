package testhelpers

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GitEnv strips every location variable set at call time and stops the
// upward walk at the temporary root.
func TestGitEnvStripsLocations(t *testing.T) {
	for _, v := range gitLocationVars {
		t.Setenv(v, "/victim")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null") // configuration is the caller's
	env := GitEnv()
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_CEILING_DIRECTORIES=") {
			assert.Equal(t, "GIT_CEILING_DIRECTORIES="+os.TempDir(), kv)
			continue
		}
		assert.False(t, isGitLocation(kv), "location variable kept: %s", kv)
	}
	assert.Contains(t, env, "GIT_CONFIG_GLOBAL=/dev/null")
	assert.Equal(t, GitEnv(), GitCommand("status").Env)
}

// IsolateGit clears location, configuration and identity variables from the
// process and sets the ceiling.
func TestIsolateGit(t *testing.T) {
	for _, v := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_COUNT", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME"} {
		t.Setenv(v, "x")
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", "/elsewhere")
	require.NoError(t, IsolateGit())
	for _, v := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_COUNT", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME"} {
		_, set := os.LookupEnv(v)
		assert.False(t, set, v)
	}
	assert.Equal(t, os.TempDir(), os.Getenv("GIT_CEILING_DIRECTORIES"))
}

// GitTimeout shortens the git deadline for one test and restores it after.
func TestGitTimeoutRestores(t *testing.T) {
	t.Run("shortened", func(t *testing.T) {
		GitTimeout(t, time.Millisecond)
		prev := gitenv.SetTimeout(time.Millisecond)
		assert.Equal(t, time.Millisecond, prev)
	})
	prev := gitenv.SetTimeout(gitenv.Timeout)
	assert.Equal(t, gitenv.Timeout, prev, "restored when the test ended")
}
