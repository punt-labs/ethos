//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

func scrubPlacementGit(t *testing.T, se *cliSubprocessEnv, args ...string) string {
	t.Helper()
	cmd := testhelpers.GitCommand(args...)
	cmd.Dir, cmd.Env = se.repo, se.env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return strings.TrimSpace(string(out))
}

func cliScrubPlacementFiles() map[string]string {
	const m = ".punt-labs/ethos/missions/m-2026-01-01-001/"
	return map[string]string{
		".punt-labs/ethos/missions.jsonl":               "{\"id\":\"m-2026-01-01-001\",\"success_criteria\":[\"a@b.org\"]}\n",
		m + "contract.yaml":                             "mission_id: m-2026-01-01-001\ncontext: a@b.org\nsuccess_criteria: [works]\n",
		m + "results.yaml":                              "results:\n  - prose: a@b.org\n",
		m + "reflections.yaml":                          "reflections:\n  - reason: a@b.org\n",
		m + "delegations/d-1/record.yaml":               "id: d-1\nsession: a@b.org\n",
		m + "delegations/d-1/prompt.md":                 "read a@b.org\n",
		".punt-labs/ethos/missions/plain.contract.yaml": "mission_id: m-2026-01-01-002\ncontext: a@b.org\nsuccess_criteria: [works]\n",
		".punt-labs/ethos/missions/plain.result.yaml":   "prose: a@b.org\n",
	}
}

func cliScrubPlacementFixture(t *testing.T, linked bool) (*cliSubprocessEnv, string, map[string]string) {
	t.Helper()
	require.NotEmpty(t, ethosBinary, "real CLI binary is required, not a skipped check")
	se := setupCLISubprocessEnv(t)
	// This isolated fixture needs no product Git hooks.
	require.NoError(t, os.Remove(filepath.Join(se.repo, ".git", "hooks", "pre-commit")))
	files := cliScrubPlacementFiles()
	for rel, body := range files {
		path := filepath.Join(se.repo, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	scrubPlacementGit(t, se, "add", ".punt-labs")
	scrubPlacementGit(t, se, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "scrub fixture")
	store := se.repo
	if linked {
		checkout := filepath.Join(t.TempDir(), "linked")
		scrubPlacementGit(t, se, "worktree", "add", "-qb", "scrub-linked", checkout)
		se.repo = checkout
	}
	return se, store, files
}

// Trace the real binary's file creation, not just its final tree.
func traceMissionScrubCLI(t *testing.T, se *cliSubprocessEnv) (string, string, error) {
	t.Helper()
	trace := filepath.Join(t.TempDir(), "scrub.strace")
	cmd := exec.Command("strace", "-f", "-qq", "-e", "trace=openat", "-o", trace, ethosBinary, "mission", "scrub", "--apply")
	cmd.Dir, cmd.Env = se.repo, withForcedNotUnderClaudeCode(se.env)
	out, err := cmd.CombinedOutput()
	return trace, string(out), err
}

func TestMissionScrubCLITransientPlacement(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "linked"}[linked], func(t *testing.T) {
			se, store, files := cliScrubPlacementFixture(t, linked)
			p, err := layout.NewRepository(store, se.repo, layout.RecordBlame)
			require.NoError(t, err)
			cTemp := filepath.Join(se.repo, ".punt-labs", "local", "ethos", "tmp")
			require.NoError(t, os.MkdirAll(p.TempDir(), 0o700))
			require.NoError(t, os.MkdirAll(cTemp, 0o700))
			index := scrubPlacementGit(t, se, "rev-parse", "--path-format=absolute", "--git-path", "index")
			before, err := os.ReadFile(index)
			require.NoError(t, err)
			trace, out, err := traceMissionScrubCLI(t, se)
			require.NoError(t, err, "%s", out)
			data, err := os.ReadFile(trace)
			require.NoError(t, err)
			seen := 0
			for line := range strings.SplitSeq(string(data), "\n") {
				if strings.Contains(line, layout.TmpMarker) && strings.Contains(line, "O_CREAT|O_EXCL") {
					seen++
					t.Logf("observed CLI staging syscall: %s", line)
					assert.Contains(t, line, `"`+p.TempDir()+string(filepath.Separator), "actual transient file creation")
				}
			}
			require.Equal(t, len(files), seen, "every supported rewrite must reach the actual atomic sink")
			for rel, raw := range files {
				data, err := os.ReadFile(filepath.Join(se.repo, rel))
				require.NoError(t, err)
				assert.NotContains(t, string(data), "a@b.org")
				if linked {
					data, err = os.ReadFile(filepath.Join(store, rel))
					require.NoError(t, err)
					assert.Equal(t, raw, string(data), "scrub changes C, not S authority")
				}
			}
			after, err := os.ReadFile(index)
			require.NoError(t, err)
			assert.Equal(t, before, after, "scrub never stages the rewritten records")
		})
	}
}

func TestMissionScrubCLIRefusesStagingObstruction(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, symlink := range []bool{false, true} {
			t.Run(map[bool]string{false: "ordinary", true: "linked"}[linked]+"/"+map[bool]string{false: "file", true: "symlink"}[symlink], func(t *testing.T) {
				se, store, files := cliScrubPlacementFixture(t, linked)
				p, err := layout.NewRepository(store, se.repo, layout.RecordBlame)
				require.NoError(t, err)
				require.NoError(t, os.MkdirAll(filepath.Dir(p.TempDir()), 0o700))
				if symlink {
					require.NoError(t, os.Symlink(t.TempDir(), p.TempDir()))
				} else {
					require.NoError(t, os.WriteFile(p.TempDir(), []byte("obstruction"), 0o600))
				}
				index := scrubPlacementGit(t, se, "rev-parse", "--path-format=absolute", "--git-path", "index")
				before, err := os.ReadFile(index)
				require.NoError(t, err)
				trace, out, err := traceMissionScrubCLI(t, se)
				require.Error(t, err, "%s", out)
				assert.Contains(t, out, "0 rewritten, 8 failed")
				data, err := os.ReadFile(trace)
				require.NoError(t, err)
				for line := range strings.SplitSeq(string(data), "\n") {
					if strings.Contains(line, "O_CREAT|O_EXCL") {
						assert.NotContains(t, line, layout.TmpMarker, "no adjacent staging after refusal")
					}
				}
				for rel, raw := range files {
					data, err := os.ReadFile(filepath.Join(se.repo, rel))
					require.NoError(t, err)
					assert.Equal(t, raw, string(data), "%s", rel)
				}
				after, err := os.ReadFile(index)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				if linked {
					assert.NoDirExists(t, filepath.Join(se.repo, ".punt-labs", "local"), "S obstruction must not trigger C fallback")
				}
			})
		}
	}
}

func TestMissionScrubUsesValidatedSnapshot(t *testing.T) {
	se, store, files := cliScrubPlacementFixture(t, true)
	t.Setenv("HOME", se.home)
	savedRoots := cmdRoots.Load()
	savedApply := missionScrubApply
	t.Cleanup(func() { cmdRoots.Store(savedRoots); missionScrubApply = savedApply })
	cmdRoots.Store(&repoRoots{Located: resolve.Located{
		Store: store, Checkout: se.repo, Config: &resolve.RepoConfig{Record: resolve.RecordFull},
	}})
	// Neither the invalid current config nor a conflicting override can alter
	// the immutable roots/config snapshot already validated for this request.
	require.NoError(t, os.WriteFile(filepath.Join(store, ".punt-labs", "ethos.yaml"), []byte("record: invalid\n"), 0o600))
	t.Setenv("ETHOS_REPO_ROOT", filepath.Join(t.TempDir(), "not-a-store"))
	missionScrubApply = true
	var out, errOut bytes.Buffer
	require.NoError(t, runMissionScrub(&out, &errOut))
	assert.Contains(t, out.String(), "8 file(s) rewritten")
	for rel := range files {
		data, err := os.ReadFile(filepath.Join(se.repo, rel))
		require.NoError(t, err)
		assert.NotContains(t, string(data), "a@b.org")
	}
	assert.NoDirExists(t, filepath.Join(se.repo, ".punt-labs", "local"))
}

func TestMissionScrubRejectsInvalidSnapshot(t *testing.T) {
	se, store, files := cliScrubPlacementFixture(t, true)
	saved := cmdRoots.Load()
	t.Cleanup(func() { cmdRoots.Store(saved) })
	cmdRoots.Store(&repoRoots{Located: resolve.Located{
		Store: store, Checkout: se.repo, Config: &resolve.RepoConfig{Record: "invalid"},
	}})
	var out, errOut bytes.Buffer
	err := runMissionScrub(&out, &errOut)
	require.ErrorContains(t, err, "record")
	assert.Empty(t, out.String())
	for rel, raw := range files {
		data, err := os.ReadFile(filepath.Join(se.repo, rel))
		require.NoError(t, err)
		assert.Equal(t, raw, string(data))
	}
	assert.NoDirExists(t, filepath.Join(store, ".punt-labs", "local"))
}
