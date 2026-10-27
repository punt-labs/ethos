package main

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/adr"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := testhelpers.GitCommand(append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestCmdWritersPathsAreClassified runs the writers that live in package main
// against a temporary repository: the repo config writers (mergeRepoConfig,
// setConfigKey and writeConfigFile), `team add-bundle`, which runs
// `git submodule add`, and the ADR store at the location the CLI resolves. Every
// file they leave inside the working tree must have a class.
func TestCmdWritersPathsAreClassified(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	runGit(t, root, "init", "-q")

	require.NoError(t, mergeRepoConfig(root))
	require.NoError(t, setConfigKey(root, "team", "engineering"))

	// A local bundle repository to add as a submodule; git refuses file
	// transport for submodules unless told otherwise.
	src := t.TempDir()
	runGit(t, src, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(src, "bundle.yaml"), []byte("name: b\n"), 0o644))
	runGit(t, src, "add", "bundle.yaml")
	runGit(t, src, "commit", "-q", "-m", "bundle")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")
	t.Chdir(root)

	oldName, oldGlobal, oldApply := addBundleName, addBundleGlobal, addBundleApply
	t.Cleanup(func() { addBundleName, addBundleGlobal, addBundleApply = oldName, oldGlobal, oldApply })
	addBundleName, addBundleGlobal, addBundleApply = "b", false, true
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, runTeamAddBundle(cmd, src))

	store, err := adrStore()
	require.NoError(t, err)
	a := &adr.ADR{
		Title: "t", Status: adr.StatusProposed, Author: "bwk",
		CreatedAt: "2026-10-24T00:00:00Z", UpdatedAt: "2026-10-24T00:00:00Z",
		Context: "c", Decision: "d",
	}
	require.NoError(t, store.Create(a))

	var paths []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		switch {
		case rel == ".git" && d.IsDir():
			return filepath.SkipDir
		case d.IsDir():
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	require.NoError(t, err)

	// The CLI's ADR store is global today (ethos-yjh0 moves it into the
	// repository). Where it sits inside the tree its files need a class;
	// where it sits outside, none of them may appear in the walk.
	adrRel, err := filepath.Rel(root, store.Dir())
	require.NoError(t, err)
	if strings.HasPrefix(adrRel, "..") {
		require.FileExists(t, filepath.Join(store.Dir(), a.ID+".yaml"))
		for _, p := range paths {
			require.NotContains(t, p, a.ID)
		}
	} else {
		require.Contains(t, paths, filepath.ToSlash(filepath.Join(adrRel, a.ID+".yaml")))
	}

	for _, want := range []string{".gitmodules", ".punt-labs/ethos.yaml", ".punt-labs/ethos-bundles/b/bundle.yaml"} {
		require.Contains(t, paths, want)
	}
	for _, p := range paths {
		if c, e, ok := layout.Classify(p); !ok {
			t.Errorf("writer created %s, which no entry of layout.Table matches", p)
		} else if c != layout.Source {
			t.Errorf("%s classified %v by %q, want source", p, c, e.Pattern)
		}
	}
}
