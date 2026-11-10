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

const singleRuleBlock = "# ethos: shared machine-local zone (never track)\n.punt-labs/local/\n"

func TestGitignoreReplacesLegacyBlock(t *testing.T) {
	for _, v := range []string{"v3.11.0", "v3.12.0", "v4.0.0", "v4.8.0", "v4.13.0", "v4.17.0", "v4.20.4", "v4.21.0", "v4.22.1"} {
		t.Run(v, func(t *testing.T) {
			env := fixtureEnv(t, "corpus/"+v)
			path := filepath.Join(env.Roots.Checkout, ".gitignore")
			before, err := os.ReadFile(path)
			wasPresent := err == nil
			legacy := strings.Contains(string(before), "# ethos runtime (never track)")

			out, err := runGitignore(context.Background(), env)
			require.NoError(t, err)
			assert.False(t, out.Deferred)
			assert.Empty(t, out.Residuals)

			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, singleRuleBlock, string(after), "the block is the single rule")
			if legacy || !wasPresent {
				assert.Equal(t, 1, out.Items)
			} else {
				assert.Equal(t, 0, out.Items, "a current .gitignore is left alone")
			}

			again, err := runGitignore(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, again.Items, "second run changes nothing")
			final, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, string(after), string(final))
		})
	}
}

func TestGitignorePreservesUserLines(t *testing.T) {
	env := fixtureEnv(t, "corpus/v4.20.4")
	path := filepath.Join(env.Roots.Checkout, ".gitignore")
	legacy, err := os.ReadFile(path)
	require.NoError(t, err)
	user := "node_modules/\n*.log\n\n"
	tail := "\n# build output\ndist/\n.punt-labs/**/local/**\n"
	require.NoError(t, os.WriteFile(path, []byte(user+string(legacy)+tail), 0o644))

	_, err = runGitignore(context.Background(), env)
	require.NoError(t, err)
	after := readFixture(t, path)
	assert.True(t, strings.HasPrefix(after, user), "lines before the block are byte-for-byte")
	assert.True(t, strings.HasSuffix(after, tail), "lines after the block are byte-for-byte, an identical rule outside it included")
	assert.Equal(t, 1, strings.Count(after, "# ethos: shared machine-local zone (never track)"))
	assert.NotContains(t, after, "# ethos runtime (never track)")
	assert.NotContains(t, after, "*.ethos-tmp*")
}

func TestGitignoreSessionsIgnoreIsResidual(t *testing.T) {
	env := fixtureEnv(t, "damage/gitignore-ignores-sessions")
	path := filepath.Join(env.Roots.Checkout, ".gitignore")
	before := readFixture(t, path)

	out, err := runGitignore(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, stepGitignore, out.Residuals[0].Step)
	assert.Contains(t, out.Residuals[0].ID, ".punt-labs/ethos/sessions/")
	assert.Equal(t, before, readFixture(t, path), "a user line outside the former block is never touched")

	again, err := runGitignore(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, again.Items)
}

func TestGitignoreRefreshesHooksFirst(t *testing.T) {
	env := fixtureEnv(t, "corpus/v4.20.4")
	_, err := runGitignore(context.Background(), env)
	require.NoError(t, err)
	b := readFixture(t, filepath.Join(env.Roots.Checkout, ".git", "hooks", "pre-commit"))
	assert.Contains(t, b, "hook pre-commit", "step 10 refreshed the hook (ethos-fdmx)")
	assert.NotContains(t, b, "audit seal --commit-context")
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
