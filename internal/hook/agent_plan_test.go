package hook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func physicalAgentTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return root
}

func TestAgentPlanLastRenderedMembershipWins(t *testing.T) {
	for _, lastRole := range []string{"reviewer", "go-specialist", "missing-role"} {
		t.Run(lastRole, func(t *testing.T) {
			root, ids, teams, roles := setupTestRepo(t)
			ethos := filepath.Join(root, ".punt-labs", "ethos")
			writeFile(t, filepath.Join(ethos, "roles", "reviewer.yaml"), "name: reviewer\nresponsibilities: [Review only]\ntools: [Read]\n")
			firstRole := "go-specialist"
			if lastRole == "go-specialist" {
				firstRole = "reviewer"
			}
			writeFile(t, filepath.Join(ethos, "teams", "engineering.yaml"), "name: engineering\nmembers:\n  - identity: bwk\n    role: "+firstRole+"\n  - identity: bwk\n    role: "+lastRole+"\n")
			plan, err := PlanGeneratedAgentFiles(root, root, ids, teams, roles)
			require.NoError(t, err)
			require.Len(t, plan.Files, 1, "one effective file, not one per role")
			assert.Equal(t, map[string]bool{"bwk": true}, plan.Owned)
			if lastRole != "reviewer" {
				assert.Contains(t, plan.Files[0].Content, "Go package implementation with tests")
			} else {
				assert.Contains(t, plan.Files[0].Content, "Review only")
			}
			if lastRole == "missing-role" {
				require.Len(t, plan.Problems, 1, "a skipped membership must not erase a prior rendered file")
			} else {
				assert.Empty(t, plan.Problems)
			}
			require.NoError(t, GenerateAgentFilesTo(root, root, ids, teams, roles))
			data, err := os.ReadFile(filepath.Join(root, ".claude", "agents", "bwk.md"))
			require.NoError(t, err)
			assert.Equal(t, plan.Files[0].Content, string(data))
		})
	}
}

func TestAgentPlanCountsIncompleteDestinationsOnce(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	ethos := filepath.Join(root, ".punt-labs", "ethos")
	writeFile(t, filepath.Join(ethos, "teams", "engineering.yaml"), "name: engineering\nmembers:\n  - identity: bwk\n    role: go-specialist\n  - identity: bwk\n    role: reviewer\n")
	require.NoError(t, os.Remove(filepath.Join(ethos, "personalities", "kernighan.md")))
	authoritative := identity.NewLayeredStoreWithBundle(identity.NewStore(ids.Root()), nil, identity.NewStore(physicalAgentTempDir(t)), true)
	plan, err := PlanGeneratedAgentFiles(root, root, authoritative, teams, roles)
	require.ErrorContains(t, err, "generated 0 of 1 expected agent files (failed: bwk)")
	assert.Empty(t, plan.Files)
	assert.True(t, plan.Owned["bwk"])
}

func TestAgentPlanProblemsVisibleAtEntryPoints(t *testing.T) {
	for _, entry := range []string{"compatibility writer", "direct plan and apply", "SessionStart"} {
		t.Run(entry, func(t *testing.T) {
			root, ids, teams, roles := setupTestRepo(t)
			isolateGitConfig(t, "test-human")
			require.NoError(t, GenerateAgentFilesTo(root, root, ids, teams, roles))
			path := filepath.Join(root, ".claude", "agents", "bwk.md")
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			// A skipped member stays owned, so even an available legacy stub
			// must not replace the stale generated file.
			writeFile(t, filepath.Join(root, ".punt-labs", "ethos", "agents", "bwk.md"), "legacy stub\n")
			writeFile(t, filepath.Join(root, ".punt-labs", "ethos", "personalities", "kernighan.md"), "")
			cfg, err := resolve.ConfigOf(root)
			require.NoError(t, err)
			loc := resolve.Located{Store: root, Checkout: root, Ethos: filepath.Join(root, ".punt-labs", "ethos"), Config: cfg}
			stderr := captureStderr(t, func() {
				switch entry {
				case "compatibility writer":
					require.NoError(t, GenerateAgentFilesTo(root, root, ids, teams, roles))
				case "direct plan and apply":
					plan, err := PlanGeneratedAgentFilesFromConfig(cfg, AgentProjectPatterns(root), nil, AgentIdentities(ids), teams, roles)
					require.NoError(t, err)
					require.Len(t, plan.Problems, 1)
					p, err := loc.Layout()
					require.NoError(t, err)
					require.NoError(t, ApplyAgentPlan(p, plan))
				case "SessionStart":
					deps := SessionStartDeps{Located: &loc, Store: ids, Sessions: session.NewStore(physicalAgentTempDir(t)), Teams: teams, Roles: roles}
					out, err := captureStdout(t, func() error {
						return HandleSessionStart(strings.NewReader(`{"session_id":"agent-plan-warning"}`), deps)
					})
					require.NoError(t, err)
					assert.Contains(t, out, "Claude Agento", "exercise the real agent generation branch")
				}
			})
			assert.Equal(t, 1, strings.Count(stderr, "ethos: generate-agents: skipping bwk: no personality content"), stderr)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after, "a skipped render intentionally leaves the existing bytes alone")
		})
	}
}

func TestAgentPlanPhysicalRootsUnderAliasedTMPDIR(t *testing.T) {
	base := physicalAgentTempDir(t)
	alias := filepath.Join(base, "tmp-alias")
	physical := filepath.Join(base, "tmp-physical")
	require.NoError(t, os.Mkdir(physical, 0o700))
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Setenv("TMPDIR", alias)
	t.Setenv("TMP", alias)
	t.Setenv("TEMP", alias)
	t.Run("physical staging and checkout", func(t *testing.T) {
		raw := t.TempDir()
		resolved, err := filepath.EvalSymlinks(raw)
		require.NoError(t, err)
		require.NotEqual(t, raw, resolved, "the fixture must actually traverse the TMPDIR alias")
		root, ids, teams, roles := setupTestRepo(t)
		checkout := physicalAgentTempDir(t)
		plan, err := PlanGeneratedAgentFiles(root, checkout, ids, teams, roles)
		require.NoError(t, err)
		p, err := layout.NewRepository(root, checkout, layout.RecordBlame)
		require.NoError(t, err)
		require.NoError(t, ApplyAgentPlan(p, plan))
		require.Len(t, plan.Files, 1)
		data, err := os.ReadFile(filepath.Join(checkout, ".claude", "agents", "bwk.md"))
		require.NoError(t, err)
		assert.Equal(t, plan.Files[0].Content, string(data))
		assert.DirExists(t, filepath.Join(root, ".punt-labs", "local", "ethos", "tmp"))
		assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs", "local"))
		require.ErrorContains(t, CheckAgentPath(raw, filepath.Join(raw, "agent.md")), "symlink", "strict production validation is not relaxed for aliased fixtures")
	})
}

func TestAgentPlanUsesSnapshotAndExactWriterBytes(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	cfg, err := resolve.ConfigOf(root)
	require.NoError(t, err)
	plan, err := PlanGeneratedAgentFilesFromConfig(cfg, projectFilePatterns(root), nil, AgentIdentities(ids), teams, roles)
	require.NoError(t, err)
	require.Len(t, plan.Files, 1)
	assert.NoDirExists(t, filepath.Join(root, ".claude"), "planning must not install anything")
	writeFile(t, filepath.Join(root, ".punt-labs", "ethos.yaml"), "record: invalid\n")
	again, err := PlanGeneratedAgentFilesFromConfig(cfg, projectFilePatterns(root), nil, AgentIdentities(ids), teams, roles)
	require.NoError(t, err)
	assert.Equal(t, plan, again, "the plan must not reread the on-disk configuration")
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	require.NoError(t, ApplyAgentPlan(p, plan))
	data, err := os.ReadFile(filepath.Join(root, ".claude", "agents", "bwk.md"))
	require.NoError(t, err)
	assert.Equal(t, plan.Files[0].Content, string(data))
	require.NoError(t, ApplyAgentPlan(p, again))
}

func TestAgentToolCatalogNormalizationIsStableAndNonmutating(t *testing.T) {
	id := &identity.Identity{Name: "Brian", Handle: "bwk", Kind: "agent", PersonalityContent: "Clear prose.\n"}
	a := &role.Role{Tools: []string{" Write ", "Read", "Bash", "Read", ""}}
	b := &role.Role{Tools: []string{"Bash", "Write", "Read"}}
	want := buildAgentFile(id, b, nil, nil, "*.go", nil)
	for range 20 {
		assert.Equal(t, want, buildAgentFile(id, a, nil, nil, "*.go", nil))
	}
	assert.Equal(t, []string{" Write ", "Read", "Bash", "Read", ""}, a.Tools)
}

func TestAgentStagingStaysUnderSharedLocalOnSuccessAndError(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "rename failure"}[fail], func(t *testing.T) {
			store, checkout := physicalAgentTempDir(t), physicalAgentTempDir(t)
			path := filepath.Join(checkout, ".claude", "agents", "bwk.md")
			if fail {
				require.NoError(t, os.MkdirAll(path, 0o700))
			} else {
				writeFile(t, path, "old\n")
			}
			p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
			require.NoError(t, err)
			_, err = writeAgentFileWithPolicy(p, path, "new\n")
			if fail {
				require.Error(t, err)
				assert.DirExists(t, path)
			} else {
				require.NoError(t, err)
			}
			// Independently assert the physical temp owner, not its class.
			assert.DirExists(t, filepath.Join(store, ".punt-labs", "local", "ethos", "tmp"))
			assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs", "local"))
			for _, root := range []string{store, checkout} {
				require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
					require.NoError(t, err)
					assert.NotContains(t, entry.Name(), ".ethos-tmp", "staging must be cleaned on either outcome: %s", path)
					return nil
				}))
			}
		})
	}
}

func TestAgentWriterRefusesSymlinkSourceAndDestination(t *testing.T) {
	store, checkout, other := physicalAgentTempDir(t), physicalAgentTempDir(t), physicalAgentTempDir(t)
	writeFile(t, filepath.Join(store, "agents", "stub.md"), "stub\n")
	require.NoError(t, os.Symlink(filepath.Join(store, "agents", "stub.md"), filepath.Join(store, "agents", "alias.md")))
	_, err := PlanInstalledAgentDefinitions(store, nil)
	require.ErrorContains(t, err, "symlink")
	require.NoError(t, os.MkdirAll(filepath.Join(checkout, ".claude"), 0o700))
	require.NoError(t, os.Symlink(other, filepath.Join(checkout, ".claude", "agents")))
	_, err = writeAgentFile(store, checkout, filepath.Join(checkout, ".claude", "agents", "bwk.md"), "new\n")
	require.ErrorContains(t, err, "symlink")
	assert.NoFileExists(t, filepath.Join(other, "bwk.md"))
}

func TestAgentRenameFailurePreservesOldBytesAndCleansStage(t *testing.T) {
	for _, failure := range []error{syscall.EXDEV, os.ErrPermission} {
		t.Run(failure.Error(), func(t *testing.T) {
			store, checkout := physicalAgentTempDir(t), physicalAgentTempDir(t)
			p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
			require.NoError(t, err)
			path := filepath.Join(checkout, ".claude", "agents", "bwk.md")
			writeFile(t, path, "old complete agent\n")
			rename := func(stage, dest string) error {
				assert.Equal(t, p.TempDir(), filepath.Dir(stage), "observe the actual staging path")
				assert.Equal(t, path, dest)
				data, err := os.ReadFile(stage)
				require.NoError(t, err)
				assert.Equal(t, "new complete agent\n", string(data))
				return failure
			}
			changed, err := writeAgentFileWithRename(p, path, "new complete agent\n", rename)
			assert.False(t, changed)
			require.ErrorIs(t, err, failure)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, "old complete agent\n", string(data))
			entries, err := os.ReadDir(p.TempDir())
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestConcurrentAgentReplacementHasNoPartialReads(t *testing.T) {
	store, checkout := physicalAgentTempDir(t), physicalAgentTempDir(t)
	path := filepath.Join(checkout, ".claude", "agents", "bwk.md")
	initial := strings.Repeat("old\n", 4096)
	writeFile(t, path, initial)
	contents := []string{initial}
	valid := map[string]bool{initial: true}
	for i := range 8 {
		body := strings.Repeat(fmt.Sprintf("agent-%d\n", i), 4096)
		contents = append(contents, body)
		valid[body] = true
	}
	start, done := make(chan struct{}), make(chan struct{})
	errs := make(chan error, 9)
	var writers sync.WaitGroup
	for _, body := range contents[1:] {
		writers.Go(func() {
			<-start
			_, err := writeAgentFile(store, checkout, path, body)
			errs <- err
		})
	}
	go func() {
		<-start
		for {
			data, err := os.ReadFile(path)
			if err != nil || !valid[string(data)] {
				errs <- errors.Join(err, fmt.Errorf("observed incomplete agent (%d bytes)", len(data)))
				return
			}
			select {
			case <-done:
				errs <- nil
				return
			default:
			}
		}
	}()
	close(start)
	writers.Wait()
	close(done)
	for range 9 {
		require.NoError(t, <-errs)
	}
}

func TestAgentPlanDoesNotMigrateLegacyVoice(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)
	path := filepath.Join(root, ".punt-labs", "ethos", "identities", "bwk.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	data = append(data, []byte("voice: {provider: example, voice_id: legacy}\n")...)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	cfg, err := resolve.ConfigOf(root)
	require.NoError(t, err)
	_, err = PlanGeneratedAgentFilesFromConfig(cfg, projectFilePatterns(root), nil, AgentIdentities(ids), teams, roles)
	require.NoError(t, err)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, data, after)
	assert.NoFileExists(t, filepath.Join(ids.ExtDir("bwk"), "vox.yaml"))
}

func TestAgentPlanRejectsUninitializedPolicy(t *testing.T) {
	require.Error(t, ApplyAgentPlan(layout.Policy{}, AgentPlan{Files: []AgentFile{{Handle: "bwk", Content: "agent"}}}))
}

func TestAgentPlanRejectsMissingIdentityStore(t *testing.T) {
	root, _, teams, roles := setupTestRepo(t)
	cfg, err := resolve.ConfigOf(root)
	require.NoError(t, err)
	_, err = PlanGeneratedAgentFilesFromConfig(cfg, projectFilePatterns(root), nil, AgentIdentities(nil), teams, roles)
	require.ErrorContains(t, err, "agent rendering stores are unavailable")
}

func TestAgentDirectorySyncStaysWithinCheckout(t *testing.T) {
	checkout := physicalAgentTempDir(t)
	dir := filepath.Join(checkout, ".claude", "agents")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, syncAgentParents(checkout, dir))
	require.ErrorContains(t, syncAgentParents(checkout, physicalAgentTempDir(t)), "outside root")
}
