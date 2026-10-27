package resolve

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain isolates git: no inherited GIT_DIR or kin may point the helpers
// at a real repository, and no temporary directory may resolve to the
// checkout that holds TMPDIR.
func TestMain(m *testing.M) {
	testhelpers.MustIsolateGit()
	os.Exit(m.Run())
}

// gitState is what a stray git command would change in a repository.
type gitState struct{ config, head string }

func readGitState(t *testing.T, repo string) gitState {
	t.Helper()
	cfg, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	require.NoError(t, err)
	cmd := testhelpers.GitCommand("-C", repo, "rev-parse", "HEAD")
	head, err := cmd.Output()
	require.NoError(t, err)
	return gitState{config: string(cfg), head: strings.TrimSpace(string(head))}
}

// The incident: with GIT_DIR exported, the helpers ran init, config and
// commit against the repository GIT_DIR named. The helpers must act on the
// directory they are given, whatever the environment says.
func TestGitHelpersIgnoreInheritedGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	victim := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.Mkdir(victim, 0o755))
	gitRepo(t, victim)
	before := readGitState(t, victim)

	t.Setenv("GIT_DIR", filepath.Join(victim, ".git"))
	t.Setenv("GIT_WORK_TREE", victim)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(victim, ".git", "index"))
	scratch := filepath.Join(t.TempDir(), "scratch")
	require.NoError(t, os.Mkdir(scratch, 0o755))
	gitRepo(t, scratch)
	runGit(t, scratch, "config", "core.bare", "true")

	assert.Equal(t, before, readGitState(t, victim), "the victim repository must be untouched")
	assert.DirExists(t, filepath.Join(scratch, ".git"), "the helper must act on its own directory")
}

// A temporary directory with no repository of its own never resolves to an
// enclosing one, even when TMPDIR sits inside a checkout.
func TestGitHelpersStopAtTempRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	cmd := testhelpers.GitCommand("-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "git found a repository above the temp root: %s", out)
	assert.Contains(t, string(out), "not a git repository")
}
