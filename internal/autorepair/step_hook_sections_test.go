package autorepair

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHookSectionsRefreshed(t *testing.T) {
	cases := []struct {
		dir       string
		wantItems int // 0 means the hooks are already current
		// foreign is true when the fixture's old section carries a tag ethos
		// never wrote (damage/stale-hook-section), so it is host content: the
		// refresh chains the current section beside it and does not delete it
		// (hook commit-trailers stays, so the old block still works). The
		// fixture has no pre-commit, so that hook is installed: 2 items.
		foreign bool
	}{
		{"corpus/v4.13.0", 2, false},
		{"corpus/v4.17.0", 2, false},
		{"corpus/v4.20.4", 2, false},
		{"corpus/v4.21.0", 2, false},
		// The restored v4.22.1 snapshot was enabled by a binary that still
		// wrote the pre-shim section, so both hooks are refreshed.
		{"corpus/v4.22.1", 2, false},
		{"damage/stale-hook-section", 2, false},
	}
	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			env := fixtureEnv(t, c.dir)
			out, err := runHookSections(context.Background(), env)
			require.NoError(t, err)
			assert.False(t, out.Deferred)
			assert.Empty(t, out.Residuals)
			assert.Equal(t, c.wantItems, out.Items)

			hooks := filepath.Join(env.Roots.Checkout, ".git", "hooks")
			for _, name := range []string{"pre-commit", "commit-msg"} {
				b, err := os.ReadFile(filepath.Join(hooks, name))
				require.NoError(t, err)
				s := string(b)
				assert.Contains(t, s, "hook "+name, "%s is the shim", name)
				assert.NotContains(t, s, "audit seal --commit-context")
				if !c.foreign {
					assert.NotContains(t, s, "hook commit-trailers")
					assert.Equal(t, 1, strings.Count(s, "# --- BEGIN "), "%s has one ethos section", name)
				}
			}

			again, err := runHookSections(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, again.Items, "second run changes nothing")
		})
	}
}

// buildEthos builds the binary under test into a scratch directory and returns
// that directory, for a test that must run git hooks against a real ethos.
func buildEthos(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "ethos"), "../../cmd/ethos")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building ethos: %s", out)
	return dir
}

// A refreshed section must work, not merely read right: a real git commit runs
// through the shim the refresh wrote, against the built binary, and lands with
// its trailer. The fixture's old section calls the retired hook commit-trailers.
func TestHookSectionsRefreshedSectionCommits(t *testing.T) {
	// Build first: fixtureEnv points HOME at a scratch directory, and a build
	// under it would write the module cache there.
	bin := buildEthos(t)
	env := fixtureEnv(t, "damage/stale-hook-section")
	repo := env.Roots.Checkout

	out, err := runHookSections(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, 2, out.Items)
	hook := readFixture(t, filepath.Join(repo, ".git", "hooks", "commit-msg"))
	require.NotContains(t, hook, "hook commit-trailers", "the retired subcommand is gone")

	gitEnv := append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x",
		"MISSION_ID=m-2026-11-04-043", "DELEGATION_ID=d-2026-11-04-043")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x\n"), 0o644))
	for _, args := range [][]string{{"add", "f.txt"}, {"-c", "commit.gpgsign=false", "commit", "-q", "-m", "refreshed"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repo, gitEnv
		b, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, b)
	}
	cmd := exec.Command("git", "log", "-1", "--format=%B")
	cmd.Dir, cmd.Env = repo, gitEnv
	b, err := cmd.CombinedOutput()
	require.NoError(t, err, string(b))
	assert.Contains(t, string(b), "Mission: m-2026-11-04-043")
	assert.Contains(t, string(b), "Delegation: d-2026-11-04-043")
}

func TestHookSectionsSharedHooksPathIsResidual(t *testing.T) {
	env := fixtureEnv(t, "corpus/v4.20.4")
	shared := filepath.Join(t.TempDir(), "shared-hooks")
	require.NoError(t, os.MkdirAll(shared, 0o755))
	for _, name := range []string{"pre-commit", "commit-msg"} {
		b, err := os.ReadFile(filepath.Join(env.Roots.Checkout, ".git", "hooks", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(shared, name), b, 0o755))
	}
	cfg := filepath.Join(env.Roots.Checkout, ".git", "config")
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("[core]\n\thooksPath = " + shared + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	out, err := runHookSections(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	assert.False(t, out.Deferred)
	require.Len(t, out.Residuals, 2)
	assert.Equal(t, stepHookSections, out.Residuals[0].Step)
}

// A truncated or duplicated section is a permanent condition of the file: the
// step records it once as a residual, returns no error (an error withholds the
// stamp and retries forever), and leaves the file byte-identical.
func TestHookSectionsMalformedIsResidualNotError(t *testing.T) {
	shapes := map[string]func(t *testing.T, s string) string{
		"truncated": func(t *testing.T, s string) string {
			i := strings.LastIndex(s, "# --- END ")
			require.GreaterOrEqual(t, i, 0)
			return s[:i]
		},
		"duplicated": func(t *testing.T, s string) string {
			i := strings.Index(s, "# --- BEGIN ")
			require.GreaterOrEqual(t, i, 0)
			return s + s[i:]
		},
	}
	for name, mutate := range shapes {
		t.Run(name, func(t *testing.T) {
			env := fixtureEnv(t, "corpus/v4.22.1")
			path := filepath.Join(env.Roots.Checkout, ".git", "hooks", "commit-msg")
			cur, err := runHookSections(context.Background(), env)
			require.NoError(t, err)
			require.Empty(t, cur.Residuals)
			b, err := os.ReadFile(path)
			require.NoError(t, err)
			bad := mutate(t, string(b))
			require.NoError(t, os.WriteFile(path, []byte(bad), 0o755))

			out, err := runHookSections(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, out.Items)
			require.Len(t, out.Residuals, 1)
			r := out.Residuals[0]
			assert.Equal(t, stepHookSections, r.Step)
			assert.Contains(t, r.ID, "commit-msg")
			assert.Contains(t, r.Reason, "cannot be refreshed")
			assert.NotContains(t, r.ID+r.Reason, env.Roots.Checkout, "residuals name no absolute path")
			assert.NotContains(t, r.ID+r.Reason, os.Getenv("HOME"))
			assert.NotContains(t, r.Reason, "next session", "the reason states the condition, not a future repair")

			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, bad, string(after), "the file is byte-identical")

			again, err := runHookSections(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, again.Items)
			after, err = os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, bad, string(after))
		})
	}
}
