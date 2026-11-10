package autorepair

import (
	"context"
	"os"
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
		{"corpus/v4.22.1", 0, false},
		{"damage/stale-hook-section", 2, true},
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
