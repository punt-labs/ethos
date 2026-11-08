//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/provenance"
)

func publicationGitCLI(t *testing.T, se *cliSubprocessEnv, args ...string) string {
	t.Helper()
	out, err := gitenv.OutputEnv(se.repo, "git", se.env, args...)
	require.NoError(t, err, "git %v", args)
	return strings.TrimSpace(string(out))
}

func publicationFixtureCLI(t *testing.T) (*cliSubprocessEnv, string, string) {
	t.Helper()
	require.NotEmpty(t, ethosBinary, "publication acceptance requires the actual CLI")
	root := t.TempDir()
	se := &cliSubprocessEnv{home: t.TempDir(), repo: root}
	se.env = []string{"HOME=" + se.home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull}
	publicationGitCLI(t, se, "init", "-b", "main")
	publicationGitCLI(t, se, "config", "user.name", "Fixture")
	publicationGitCLI(t, se, "config", "user.email", "fixture@example.invalid")
	require.NoError(t, os.WriteFile(filepath.Join(root, "source.txt"), []byte("base\n"), 0o600))
	publicationGitCLI(t, se, "add", "source.txt")
	publicationGitCLI(t, se, "commit", "-m", "base")
	base := publicationGitCLI(t, se, "rev-parse", "HEAD")
	id := "m-2026-11-04-001"
	stamp := "2026-11-04T10:00:00Z"
	c := mission.Contract{MissionID: id, Status: mission.StatusOpen, CreatedAt: stamp, UpdatedAt: stamp,
		Leader: "lead", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "rsc", PinnedAt: stamp}, WriteSet: []string{"source.txt"}, SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 1}, CurrentRound: 1,
		RoundDecisions: &mission.RoundDecisions{Version: 1, Entries: []mission.RoundDecision{}}}
	dir := filepath.Join(root, ".punt-labs", "ethos", "missions", id)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "delegations", "d-one"), 0o700))
	data, err := yaml.Marshal(c)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "contract.yaml"), data, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "corrections.yaml"), []byte("version: 1\nmission: "+id+"\nentries: []\n"), 0o600))
	d := mission.Delegation{ID: "d-one", Mission: id, Tier: mission.TierB, AgentType: "bwk", CreatedAt: stamp, Verdict: "open"}
	data, err = yaml.Marshal(d)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "delegations", "d-one", "record.yaml"), data, 0o600))
	checkout := filepath.Join(t.TempDir(), "linked")
	publicationGitCLI(t, se, "worktree", "add", "-b", "feature", checkout)
	se.repo = checkout
	require.NoError(t, os.WriteFile(filepath.Join(checkout, "source.txt"), []byte("branch source\n"), 0o600))
	publicationGitCLI(t, se, "add", "source.txt")
	publicationGitCLI(t, se, "commit", "-m", "source\n\nMission: "+id+"\nDelegation: d-one")
	return se, base, id
}

func TestPublicationCLIInvokingWorktreeSubdirectoryAndAlternateIndex(t *testing.T) {
	se, base, id := publicationFixtureCLI(t)
	index := publicationGitCLI(t, se, "rev-parse", "--path-format=absolute", "--git-path", "index")
	normal, err := os.ReadFile(index)
	require.NoError(t, err)
	checkout := se.repo
	alternate := filepath.Join(filepath.Dir(checkout), "committing-index")
	require.NoError(t, os.WriteFile(alternate, normal, 0o600))
	sub := filepath.Join(se.repo, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o700))
	se.repo = sub
	se.env = append(se.env, "GIT_INDEX_FILE=../committing-index")
	require.Equal(t, alternate, publicationGitCLI(t, se, "rev-parse", "--path-format=absolute", "--git-path", "index"))
	stdout, stderr, exit := runCLI(t, se, "mission", "publish", id, "--base", base, "--json")
	require.Equal(t, 0, exit, stderr)
	var output struct {
		Receipt provenance.Receipt `json:"receipt"`
		Error   string             `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &output))
	assert.True(t, output.Receipt.IndexInstalled)
	assert.Empty(t, output.Error)
	assert.Equal(t, alternate, output.Receipt.Index)
	assert.FileExists(t, filepath.Join(filepath.Dir(sub), filepath.FromSlash(output.Receipt.Manifest)))
	assert.NoDirExists(t, filepath.Join(sub, ".punt-labs"))
	actualNormal, err := os.ReadFile(index)
	require.NoError(t, err)
	assert.Equal(t, normal, actualNormal)
	stdout, stderr, exit = runCLI(t, se, "provenance", "verify-index", output.Receipt.Manifest, "--json")
	require.Equal(t, 0, exit, stderr)
	assert.Contains(t, stdout, output.Receipt.Manifest)
	publicationGitCLI(t, se, "commit", "-m", "publication")
	assert.Contains(t, publicationGitCLI(t, se, "ls-tree", "--full-tree", "-r", "--name-only", "HEAD"), output.Receipt.Manifest)
	stdout, stderr, exit = runCLI(t, se, "provenance", "message", "HEAD", "--json")
	require.Equal(t, 0, exit, stderr)
	var message struct {
		Head string `json:"head"`
		Body string `json:"body"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &message))
	assert.Equal(t, publicationGitCLI(t, se, "rev-parse", "HEAD"), message.Head)
	assert.Contains(t, message.Body, "Ethos-Provenance: "+output.Receipt.Manifest)
}

func TestPublicationCLIFailureIncludesIndexReceipt(t *testing.T) {
	se, base, id := publicationFixtureCLI(t)
	index := publicationGitCLI(t, se, "rev-parse", "--path-format=absolute", "--git-path", "index")
	require.NoError(t, os.WriteFile(index+".lock", []byte("another writer"), 0o600))
	stdout, stderr, exit := runCLI(t, se, "mission", "publish", id, "--base", base, "--json")
	require.Equal(t, 1, exit, stderr)
	var output struct {
		Receipt provenance.Receipt `json:"receipt"`
		Error   string             `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &output))
	assert.False(t, output.Receipt.IndexInstalled)
	assert.Contains(t, output.Error, "actual index lock")
	assert.Empty(t, output.Receipt.Copied)
}
