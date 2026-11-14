package enable

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryStagingIsSharedAndUnique(t *testing.T) {
	store, checkout := physicalTempDir(t), physicalTempDir(t)
	p, err := layout.NewRepository(store, checkout, layout.RecordFull)
	require.NoError(t, err)
	for name, create := range map[string]func() (*os.File, error){
		"gitignore": func() (*os.File, error) { return createGitignoreTemp(filepath.Join(checkout, ".gitignore"), p) },
		"vendored":  func() (*os.File, error) { return createVendoredTemp(filepath.Join(checkout, guideRel), p) },
	} {
		t.Run(name, func(t *testing.T) {
			first, err := create()
			require.NoError(t, err)
			defer os.Remove(first.Name())
			defer first.Close()
			second, err := create()
			require.NoError(t, err)
			defer os.Remove(second.Name())
			defer second.Close()
			assert.Equal(t, p.TempDir(), filepath.Dir(first.Name()))
			assert.Contains(t, filepath.Base(first.Name()), layout.TmpMarker)
			assert.NotEqual(t, first.Name(), second.Name())
			_, err = os.Stat(first.Name()) // inspect while it exists, not after cleanup
			require.NoError(t, err)
		})
	}
}

func TestEnableToLinkedWorktreeKeepsRuntimeInStore(t *testing.T) {
	store := gitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(store, "README"), []byte("repo\n"), 0o644))
	gitRun(t, store, "add", "README")
	gitRun(t, store, "commit", "-qm", "initial")
	checkout := filepath.Join(physicalTempDir(t), "linked")
	gitRun(t, store, "worktree", "add", "--detach", checkout)
	// An in-tree hooks directory must not become a staging directory.
	gitRun(t, checkout, "config", "core.hooksPath", filepath.Join(checkout, ".githooks"))
	for i := 0; i < 2; i++ {
		_, err := EnableTo(checkout, store)
		require.NoError(t, err)
	}
	p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
	require.NoError(t, err)
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
	locks, err := filepath.Glob(filepath.Join(p.LocalRoot(), "locks", "claudemd", "*.lock"))
	require.NoError(t, err)
	assert.Len(t, locks, 1)
	ignored, err := LocalIgnored(store)
	require.NoError(t, err)
	assert.True(t, ignored, "the canonical owner's runtime must also be ignored")
	_, err = os.Stat(filepath.Join(checkout, ".punt-labs", "local"))
	assert.True(t, os.IsNotExist(err), "linked checkout acquired local runtime: %v", err)
	assert.Equal(t, gitignoreMarker+"\n"+LocalIgnoreRule+"\n", readFile(t, filepath.Join(checkout, ".gitignore")))
	_, err = DisableTo(checkout, store, false)
	require.NoError(t, err)
	for _, root := range []string{checkout, store} {
		require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == ".git" || path == p.LocalRoot()) {
				return filepath.SkipDir
			}
			assert.False(t, strings.Contains(d.Name(), layout.TmpMarker) || strings.HasSuffix(d.Name(), ".lock"), path)
			return nil
		}))
	}
}

func TestDepositInterruptedRetryAndStagingFailure(t *testing.T) {
	root := physicalTempDir(t)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	// Seed a valid legacy manifest so the guide directory fails installation,
	// not bootstrap preflight. Leave setup absent for the legacy upgrade.
	manifestBoundaryWrite(t, filepath.Join(root, manifestRel), string(manifestBytes([]string{guideRel, manifestRel})))
	// The manifest lands first, then publishing the guide fails. A retry must
	// not mistake our completed manifest for an unrelated collision.
	require.NoError(t, os.MkdirAll(filepath.Join(root, guideRel), 0o755))
	_, err = deposit(root, []byte("guide"), []byte("setup"), p)
	require.Error(t, err)
	assert.FileExists(t, filepath.Join(root, manifestRel))
	assert.Equal(t, string(manifestBytes([]string{guideRel, setupRel, manifestRel})), readFile(t, filepath.Join(root, manifestRel)),
		"updated manifest must land before the failing content installation")
	assert.NoFileExists(t, filepath.Join(root, markerRel))
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
	require.NoError(t, os.Remove(filepath.Join(root, guideRel)))
	_, err = deposit(root, []byte("guide"), []byte("setup"), p)
	require.NoError(t, err)
	// Block only local staging. Neither host writer may fall back beside its
	// destination, even though that directory is writable.
	require.NoError(t, os.Remove(p.TempDir()))
	require.NoError(t, os.WriteFile(p.TempDir(), []byte("blocked"), 0o600))
	path := filepath.Join(root, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))
	require.Error(t, writeGitignore(path, []byte("new"), p))
	assert.Equal(t, "old", readFile(t, path))
	require.Error(t, writeVendored(filepath.Join(root, guideRel), []byte("new"), p))
	assert.Equal(t, "guide", readFile(t, filepath.Join(root, guideRel)))
}

func TestEnableOnlyLocalZoneIgnoresInBothModes(t *testing.T) {
	for _, mode := range []string{"blame", "full"} {
		t.Run(mode, func(t *testing.T) {
			root := gitRepo(t)
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("record: "+mode+"\n"), 0o644))
			_, err := Enable(root)
			require.NoError(t, err)
			assert.Equal(t, gitignoreMarker+"\n"+LocalIgnoreRule+"\n", readFile(t, filepath.Join(root, ".gitignore")))
		})
	}
}

func TestEnableRetiresOnlyRecognizedManagedRules(t *testing.T) {
	root := gitRepo(t)
	userRules := "*.log\n.punt-labs/ethos/missions/*/prompt.md\n"
	oldBlock := legacyGitignoreMarker + "\n.punt-labs/**/local/**\n" + missionLockPat + "\n" + stagingPat + "\n# ethos: machine-local files (.local.*) — may hold secrets, never track\n.punt-labs/**/*.local.*\n"
	oldBlock += ".punt-labs/ethos/missions/*/delegations/*/prompt.md\n.punt-labs/ethos/missions/*/reflections.yaml\n.punt-labs/ethos/missions/*/artifacts/\n.punt-labs/ethos/sessions/*/audit-*.jsonl\n"
	tail := "\n# user-owned policy\n.punt-labs/ethos/missions/*/artifacts/\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(userRules+oldBlock+tail), 0o644))
	_, err := Enable(root)
	require.NoError(t, err)
	want := userRules + gitignoreMarker + "\n" + LocalIgnoreRule + "\n" + tail
	assert.Equal(t, want, readFile(t, filepath.Join(root, ".gitignore")))
	_, err = Enable(root)
	require.NoError(t, err)
	assert.Equal(t, want, readFile(t, filepath.Join(root, ".gitignore")))
}

func TestLegacyIgnoreRetirementDoesNotHideDefeatedCoverage(t *testing.T) {
	root := gitRepo(t)
	initial := LocalIgnoreRule + "\n!.punt-labs/local/\n" + legacyGitignoreMarker + "\n.punt-labs/**/local/**\n"
	path := filepath.Join(root, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte(initial), 0o644))
	var first string
	for attempt := range 2 {
		rep, err := Enable(root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), path)
		assert.Contains(t, err.Error(), "still stageable")
		assert.Contains(t, err.Error(), "repair conflicting ignore rules")
		var status string
		for _, step := range rep.Steps {
			if step.Step == "gitignore" {
				status = step.Status
				assert.Contains(t, step.Detail, "still stageable")
			}
		}
		assert.Equal(t, "attention", status)
		covered, err := LocalIgnored(root)
		require.NoError(t, err)
		assert.False(t, covered)
		assert.NoFileExists(t, filepath.Join(root, markerRel), "defeated protection must not create an enabled marker")
		body := readFile(t, path)
		assert.Equal(t, LocalIgnoreRule+"\n!.punt-labs/local/\n"+gitignoreMarker+"\n", body,
			"retire only the recognized managed rule, preserving the user rule and negation byte-for-byte")
		assert.Equal(t, 1, strings.Count("\n"+body, "\n"+LocalIgnoreRule+"\n"), "never append a duplicate canonical rule")
		if attempt == 0 {
			first = body
		} else {
			assert.Equal(t, first, body, "failed retry must not rewrite user policy or undo retirement")
		}
	}
}
