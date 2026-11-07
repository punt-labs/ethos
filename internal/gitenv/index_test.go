package gitenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActualIndexEnvPreservesGitRelativePath(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GIT_INDEX_FILE", "relative-index")
	t.Setenv("GIT_DIR", "/not-the-invoking-repo")
	t.Setenv("GIT_WORK_TREE", "/not-the-invoking-checkout")
	env, err := ActualIndexEnv()
	require.NoError(t, err)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	assert.Contains(t, env, "GIT_INDEX_FILE=relative-index")
	for _, entry := range env {
		assert.False(t, strings.HasPrefix(entry, "GIT_DIR="))
		assert.False(t, strings.HasPrefix(entry, "GIT_WORK_TREE="))
	}
	copyEnv := WithIndex(env, filepath.Join(cwd, "candidate"))
	assert.Contains(t, env, "GIT_INDEX_FILE=relative-index", "input remains immutable")
	assert.Contains(t, copyEnv, "GIT_INDEX_FILE="+filepath.Join(cwd, "candidate"))
	count := 0
	for _, entry := range copyEnv {
		if strings.HasPrefix(entry, "GIT_INDEX_FILE=") {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestActualIndexEnvRejectsEmpty(t *testing.T) {
	t.Setenv("GIT_INDEX_FILE", "")
	_, err := ActualIndexEnv()
	require.ErrorContains(t, err, "empty")
}
