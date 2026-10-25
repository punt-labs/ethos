package resolve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitRun runs git with args in the test's isolated environment and fails the
// test on error.
func gitRun(t *testing.T, args ...string) {
	t.Helper()
	cmd := testhelpers.GitCommand(args...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// worktreeLayout builds a repository layout with real git and returns the
// linked worktree to run from and the store expected there ("" for a
// refusal, whose error must contain refusal).
type worktreeLayout struct {
	build   func(t *testing.T, base string) (wt, store string)
	refusal string
}

var worktreeLayouts = map[string]worktreeLayout{
	// A bare common dir has no work tree of its own: the store is the
	// worktree's, as before linked worktrees shared one.
	"bare clone proj.git": {build: func(t *testing.T, base string) (string, string) {
		src := filepath.Join(base, "src")
		initRepo(t, src)
		bare := filepath.Join(base, "proj.git")
		gitRun(t, "clone", "-q", "--bare", src, bare)
		wt := filepath.Join(base, "wt")
		gitRun(t, "-C", bare, "worktree", "add", "-q", wt)
		return wt, wt
	}},
	"bare clone p/.git": {build: func(t *testing.T, base string) (string, string) {
		src := filepath.Join(base, "src")
		initRepo(t, src)
		bare := filepath.Join(base, "p", ".git")
		gitRun(t, "clone", "-q", "--bare", src, bare)
		wt := filepath.Join(base, "wt")
		gitRun(t, "-C", bare, "worktree", "add", "-q", wt)
		return wt, wt
	}},
	// A non-bare common dir that is the .git of its parent: the main
	// checkout holds the store.
	"main checkout": {build: func(t *testing.T, base string) (string, string) {
		main := filepath.Join(base, "main")
		initRepo(t, main)
		wt := filepath.Join(base, "wt")
		gitRun(t, "-C", main, "worktree", "add", "-q", wt)
		return wt, main
	}},
	// A non-bare common dir that is not its parent's .git: refused.
	"separate git dir": {build: func(t *testing.T, base string) (string, string) {
		main := filepath.Join(base, "main")
		require.NoError(t, os.MkdirAll(filepath.Join(base, "store"), 0o755))
		gitRun(t, "init", "-q", "--separate-git-dir="+filepath.Join(base, "store", "repo.git"), main)
		gitRun(t, "-C", main, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false",
			"commit", "-q", "--allow-empty", "-m", "init")
		wt := filepath.Join(base, "wt")
		gitRun(t, "-C", main, "worktree", "add", "-q", wt)
		return wt, ""
	}, refusal: "repo.git"},
	"submodule worktree": {build: func(t *testing.T, base string) (string, string) {
		src := filepath.Join(base, "src")
		initRepo(t, src)
		super := filepath.Join(base, "super")
		initRepo(t, super)
		gitRun(t, "-C", super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", src, "sub")
		wt := filepath.Join(base, "subwt")
		gitRun(t, "-C", filepath.Join(super, "sub"), "worktree", "add", "-q", wt)
		return wt, ""
	}, refusal: filepath.Join("modules", "sub")},
}

// The store of a linked worktree follows one rule, with git and without: a
// bare common dir keeps the store in the worktree; a non-bare common dir that
// is the .git of its parent work tree puts it in that main checkout; any
// other layout is refused with a reason naming it.
func TestLinkedWorktreeStoreByLayout(t *testing.T) {
	for _, withGit := range []bool{true, false} {
		for name, l := range worktreeLayouts {
			t.Run(map[bool]string{true: "git ", false: "no git "}[withGit]+name, func(t *testing.T) {
				requireGit(t)
				wt, want := l.build(t, t.TempDir())
				t.Chdir(wt)
				if !withGit {
					hideGit(t)
				}
				var store string
				var err error
				captureStderr(t, func() { store, err = LocateStoreRoot() })
				if want == "" {
					require.Error(t, err)
					assert.NotErrorIs(t, err, ErrNotInRepo)
					assert.Empty(t, store)
					assert.Contains(t, err.Error(), "linked worktree")
					assert.Contains(t, err.Error(), l.refusal)
				} else {
					require.NoError(t, err)
					assert.Equal(t, realpath(t, want), realpath(t, store))
				}
				root, err := LocateRepoRoot()
				require.NoError(t, err)
				assert.Equal(t, realpath(t, wt), realpath(t, root))
			})
		}
	}
}

// core.bare is read as git reads a boolean.
func TestConfigBare(t *testing.T) {
	cases := map[string]struct {
		config string
		bare   bool
		err    bool
	}{
		"absent":             {config: "", bare: false},
		"true":               {config: "[core]\n\tbare = true\n", bare: true},
		"false":              {config: "[core]\n\tbare = false\n", bare: false},
		"yes mixed case":     {config: "[CORE]\n\tBare = Yes\n", bare: true},
		"bare key alone":     {config: "[core]\n\tbare\n", bare: true},
		"last one wins":      {config: "[core]\nbare = true\n[core]\nbare = 0\n", bare: false},
		"other section":      {config: "[other]\n\tbare = true\n", bare: false},
		"subsection":         {config: "[core \"x\"]\n\tbare = true\n", bare: false},
		"comment":            {config: "[core]\n\tbare = true ; set by clone\n", bare: true},
		"header on one line": {config: "[core] bare = true\n", bare: true},
		"not a boolean":      {config: "[core]\n\tbare = maybe\n", err: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if c.config != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "config"), []byte(c.config), 0o644))
			}
			bare, err := handConfigBare(dir)
			if c.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.bare, bare)
		})
	}
}
