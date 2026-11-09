//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/provenance"
)

func TestProvenanceCLIActualSquashVerification(t *testing.T) {
	se, base, id := publicationFixtureCLI(t)
	stdout, stderr, exit := runCLI(t, se, "mission", "publish", id, "--base", base, "--json")
	require.Equal(t, 0, exit, stderr)
	var output struct {
		Receipt provenance.Receipt `json:"receipt"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &output))
	publicationGitCLI(t, se, "commit", "-m", "publication")
	body, stderr, exit := runCLI(t, se, "provenance", "message", "HEAD")
	require.Equal(t, 0, exit, stderr)
	// Local squash is an executable fixture for the documented remote procedure:
	// the product itself neither merges nor makes remote calls.
	publicationGitCLI(t, se, "checkout", "--detach", base)
	publicationGitCLI(t, se, "merge", "--squash", "feature")
	publicationGitCLI(t, se, "commit", "-m", body)
	merge := publicationGitCLI(t, se, "rev-parse", "HEAD")
	stdout, stderr, exit = runCLI(t, se, "provenance", "verify", merge, "--json")
	require.Equal(t, 0, exit, stderr)
	var verified provenance.Verification
	require.NoError(t, json.Unmarshal([]byte(stdout), &verified))
	assert.Equal(t, merge, verified.Commit)
	assert.Equal(t, []string{output.Receipt.Manifest}, verified.Manifests)
	// Removing the reference is a failed verification, not remote success.
	publicationGitCLI(t, se, "commit", "--amend", "-m", "reference omitted")
	_, _, exit = runCLI(t, se, "provenance", "verify", "HEAD", "--json")
	assert.Equal(t, 1, exit)
	// A malformed manifest name must not escape the tree lookup.
	_, _, exit = runCLI(t, se, "provenance", "verify-index", "../../outside")
	assert.Equal(t, 1, exit)
	assert.NoFileExists(t, filepath.Join(se.repo, ".git", "index.lock"))
	_, err := os.Stat(se.repo)
	require.NoError(t, err)
}

func TestProvenanceCLIVerifiesActualPartialCommitIndex(t *testing.T) {
	se, base, id := publicationFixtureCLI(t)
	stdout, stderr, exit := runCLI(t, se, "mission", "publish", id, "--base", base, "--json")
	require.Equal(t, 0, exit, stderr)
	var output struct {
		Receipt provenance.Receipt `json:"receipt"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &output))
	// Enable installs the real product hooks. Expose only the test binary as
	// ethos; never install to the workstation or substitute a verifier script.
	bin := t.TempDir()
	require.NoError(t, os.Symlink(ethosBinary, filepath.Join(bin, "ethos")))
	for i, entry := range se.env {
		if strings.HasPrefix(entry, "PATH=") {
			se.env[i] = "PATH=" + bin + string(os.PathListSeparator) + strings.TrimPrefix(entry, "PATH=")
		}
	}
	_, stderr, exit = runCLI(t, se, "enable", "--json")
	require.Equal(t, 0, exit, stderr)
	hook := publicationGitCLI(t, se, "rev-parse", "--path-format=absolute", "--git-path", "hooks/pre-commit")
	hookData, err := os.ReadFile(hook)
	require.NoError(t, err)
	require.Contains(t, string(hookData), "hook pre-commit")
	assert.FileExists(t, filepath.Join(se.repo, ".punt-labs", "ethos", "enabled"))
	require.NoError(t, os.WriteFile(filepath.Join(se.repo, "only.txt"), []byte("unrelated source\n"), 0o600))
	publicationGitCLI(t, se, "add", "only.txt")
	before, err := os.ReadFile(output.Receipt.Index)
	require.NoError(t, err)
	// Git's temporary --only index contains the manifest, but omits its required
	// mission records even though the ordinary index has the entire publication.
	var hookErr bytes.Buffer
	_, err = gitenv.Run(gitenv.Spec{Git: "git", Dir: se.repo, Env: se.env, Args: []string{"commit", "--only", "-m", "must refuse incomplete publication", "--", output.Receipt.Manifest, "only.txt"}, Stderr: &hookErr})
	require.Error(t, err)
	assert.Contains(t, hookErr.String(), "required record missing")
	after, err := os.ReadFile(output.Receipt.Index)
	require.NoError(t, err)
	assert.Equal(t, before, after, "failed partial commit must preserve the staged publication and unrelated source")
	// An ordinary partial commit has no new manifest and must neither block nor
	// claim to have verified the publication left in the ordinary index.
	hookErr.Reset()
	out, err := gitenv.Run(gitenv.Spec{Git: "git", Dir: se.repo, Env: se.env, Args: []string{"commit", "--only", "-m", "ordinary partial commit", "--", "only.txt"}, Stderr: &hookErr})
	require.NoError(t, err, hookErr.String())
	assert.NotContains(t, string(out)+hookErr.String(), "Verified selected snapshots")
	assert.NotContains(t, publicationGitCLI(t, se, "ls-tree", "--full-tree", "-r", "--name-only", "HEAD"), output.Receipt.Manifest)
	_, stderr, exit = runCLI(t, se, "provenance", "verify-index", output.Receipt.Manifest)
	require.Equal(t, 0, exit, stderr)
	publicationGitCLI(t, se, "commit", "-m", "complete publication")
	_, stderr, exit = runCLI(t, se, "provenance", "message", "HEAD")
	require.Equal(t, 0, exit, stderr)
}

func TestProvenanceCLIStagedSelectionUsesActualIndex(t *testing.T) {
	se, base, id := publicationFixtureCLI(t)
	for _, flags := range [][]string{nil, {"--json"}} {
		args := append([]string{"provenance", "verify-index", "--staged"}, flags...)
		stdout, stderr, exit := runCLI(t, se, args...)
		require.Equal(t, 0, exit, stderr)
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)
	}
	_, _, exit := runCLI(t, se, "provenance", "verify-index")
	assert.NotZero(t, exit, "explicit mode still requires a manifest")
	stdout, stderr, exit := runCLI(t, se, "mission", "publish", id, "--base", base, "--json")
	require.Equal(t, 0, exit, stderr)
	var output struct {
		Receipt provenance.Receipt `json:"receipt"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &output))
	index, err := os.ReadFile(output.Receipt.Index)
	require.NoError(t, err)
	alternate := filepath.Join(t.TempDir(), "committing-index")
	require.NoError(t, os.WriteFile(alternate, index, 0o600))

	// Corrupt the ordinary index only. The inherited alternate index must be
	// both the selector's source and the verifier's source.
	bad := provenance.Directory + strings.Repeat("f", 64) + ".yaml"
	require.NoError(t, os.WriteFile(filepath.Join(se.repo, filepath.FromSlash(bad)), []byte("bad manifest\n"), 0o600))
	publicationGitCLI(t, se, "add", bad)
	_, _, exit = runCLI(t, se, "provenance", "verify-index", "--staged")
	assert.NotZero(t, exit)
	se.env = append(se.env, "GIT_INDEX_FILE="+alternate)
	stdout, stderr, exit = runCLI(t, se, "provenance", "verify-index", "--staged", "--json")
	require.Equal(t, 0, exit, stderr)
	var selected struct {
		Manifests []string `json:"manifests"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &selected))
	assert.Equal(t, []string{output.Receipt.Manifest}, selected.Manifests)
	publicationGitCLI(t, se, "reset", "HEAD", "--", output.Receipt.Manifest)
	stdout, stderr, exit = runCLI(t, se, "provenance", "verify-index", "--staged", "--json")
	require.Equal(t, 0, exit, stderr)
	assert.Empty(t, stdout, "no manifest in the actual index means no verification claim")
	assert.Empty(t, stderr)
}

func TestPublicationCLIRefusesUnsafeUnrelatedMergedSummary(t *testing.T) {
	for _, target := range []string{"checkout", "index"} {
		t.Run(target, func(t *testing.T) {
			se, base, id := publicationFixtureCLI(t)
			summary := filepath.Join(se.repo, filepath.FromSlash(provenance.SummaryPath))
			require.NoError(t, os.MkdirAll(filepath.Dir(summary), 0o700))
			unsafe := []byte(" {\"id\":\"m-2026-11-04-090\",\"future\":{\"note\":\"private@example.invalid /home/private-user/work\"}} \n")
			indexed := []byte(" {\"id\":\"m-2026-11-04-090\",\"future\":true} \n")
			work := []byte("\n{\"id\":\"m-2026-11-04-091\",\"only_work\":true}\n")
			if target == "index" {
				indexed = unsafe
			} else {
				work = unsafe
			}
			require.NoError(t, os.WriteFile(summary, indexed, 0o600))
			publicationGitCLI(t, se, "add", provenance.SummaryPath)
			require.NoError(t, os.WriteFile(summary, work, 0o600))
			index := publicationGitCLI(t, se, "rev-parse", "--path-format=absolute", "--git-path", "index")
			before, err := os.ReadFile(index)
			require.NoError(t, err)
			stdout, stderr, exit := runCLI(t, se, "mission", "publish", id, "--base", base, "--json")
			require.Equal(t, 1, exit, stderr)
			var output struct {
				Receipt provenance.Receipt `json:"receipt"`
				Error   string             `json:"error"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdout), &output))
			assert.Contains(t, output.Error, "merged "+target+" summary")
			assert.NotContains(t, stdout+stderr, "private-user")
			assert.NotContains(t, stdout+stderr, "private@example.invalid")
			assert.False(t, output.Receipt.IndexInstalled)
			assert.Empty(t, output.Receipt.Copied)
			after, err := os.ReadFile(index)
			require.NoError(t, err)
			assert.Equal(t, before, after, "actual linked-worktree index remains unchanged")
			after, err = os.ReadFile(summary)
			require.NoError(t, err)
			assert.Equal(t, work, after, "checkout summary remains unchanged")
			assert.NoDirExists(t, filepath.Join(filepath.Dir(summary), "missions"))
			assert.NoDirExists(t, filepath.Join(filepath.Dir(summary), "provenance"))
			assert.NoFileExists(t, index+".lock")
		})
	}
}
