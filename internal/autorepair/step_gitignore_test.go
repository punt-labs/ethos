package autorepair

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
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

// A user rule that ignores the sessions record directory is deleted by the
// step itself: the line is exact, the fix is known, nothing else changes.
func TestGitignoreDeletesSessionsIgnoreLine(t *testing.T) {
	env := fixtureEnv(t, "damage/gitignore-ignores-sessions")
	path := filepath.Join(env.Roots.Checkout, ".gitignore")
	before := readFixture(t, path)
	require.Contains(t, before, ".punt-labs/ethos/sessions/")

	log := &recLog{}
	env.Log = log
	out, err := runGitignore(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, out.Residuals, "a condition ethos fixed is not a residual")
	assert.GreaterOrEqual(t, out.Items, 1)
	assert.Contains(t, log.steps, "gitignore:delete-sessions-ignore")
	after := readFixture(t, path)
	assert.NotContains(t, after, ".punt-labs/ethos/sessions")
	for _, l := range strings.Split(before, "\n") {
		if strings.TrimRight(l, "\r") != ".punt-labs/ethos/sessions/" && l != "" && !strings.HasPrefix(l, "# ethos") {
			assert.Contains(t, after, l, "every other line survives")
		}
	}

	again, err := runGitignore(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, again.Items)
	assert.Equal(t, after, readFixture(t, path))
}

// Only the exact lines go, and every other byte stays, CRLF endings and a
// missing final newline included.
func TestGitignoreDeleteKeepsOtherBytes(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"three spellings":  {"a/\n.punt-labs/ethos/sessions\nb\n.punt-labs/ethos/sessions/**\n.punt-labs/ethos/sessions/\nc\n", "a/\nb\nc\n"},
		"crlf":             {"a\r\n.punt-labs/ethos/sessions/\r\nb\r\n", "a\r\nb\r\n"},
		"no final newline": {"a\n.punt-labs/ethos/sessions/", "a\n"},
		"near misses stay": {"!.punt-labs/ethos/sessions/\n.punt-labs/ethos/sessions/x\n#.punt-labs/ethos/sessions/\n .punt-labs/ethos/sessions/\n", "!.punt-labs/ethos/sessions/\n.punt-labs/ethos/sessions/x\n#.punt-labs/ethos/sessions/\n .punt-labs/ethos/sessions/\n"},
	} {
		t.Run(name, func(t *testing.T) {
			env := fixtureEnv(t, "corpus/v4.20.4")
			path := filepath.Join(env.Roots.Checkout, ".gitignore")
			require.NoError(t, os.WriteFile(path, []byte(singleRuleBlock+c.in), 0o640))
			require.NoError(t, os.Chmod(path, 0o640)) // WriteFile keeps an existing file's mode
			out, err := runGitignore(context.Background(), env)
			require.NoError(t, err)
			assert.Empty(t, out.Residuals)
			assert.Equal(t, singleRuleBlock+c.want, readFixture(t, path))
			fi, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o640), fi.Mode().Perm(), "the mode is kept")
		})
	}
}

// When ethos cannot write the file the condition stays, with its cause.
func TestGitignoreDeleteUnwritableStaysActionable(t *testing.T) {
	env := fixtureEnv(t, "corpus/v4.20.4")
	dir := env.Roots.Checkout
	path := filepath.Join(dir, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte(singleRuleBlock+".punt-labs/ethos/sessions/\n"), 0o644))
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if f, err := os.CreateTemp(dir, "probe"); err == nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		t.Skip("directory writable despite mode 555 (running as root)")
	}
	out, err := runGitignore(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, ClassActionable, out.Residuals[0].Class)
	assert.Contains(t, out.Residuals[0].Action, "clears when")
	assert.Contains(t, readFixture(t, path), ".punt-labs/ethos/sessions/", "the line is untouched")
}

// Another tool's local file must stay ignored: retiring the old patterns
// while vox.local.md and biff/local/state.json depend on them would make a
// file that can hold secrets stageable.
func TestGitignoreKeepsPatternsOtherToolsFilesDependOn(t *testing.T) {
	env := fixtureEnv(t, "corpus/v4.20.4")
	repo := env.Roots.Checkout
	vox := filepath.Join(repo, ".punt-labs", "vox", "vox.local.md")
	biff := filepath.Join(repo, ".punt-labs", "biff", "local", "state.json")
	for _, p := range []string{vox, biff} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("secret\n"), 0o600))
	}
	ignored := func() bool {
		for _, rel := range []string{".punt-labs/vox/vox.local.md", ".punt-labs/biff/local/state.json"} {
			if _, err := gitenv.Output(repo, "git", "check-ignore", "-q", rel); err != nil {
				return false
			}
		}
		return true
	}
	require.True(t, ignored(), "both files are ignored before the step")

	out, err := runGitignore(context.Background(), env)
	require.NoError(t, err)
	assert.True(t, ignored(), "both files are still ignored after the step")
	require.Len(t, out.Residuals, 2)
	ids := out.Residuals[0].ID + "\n" + out.Residuals[1].ID
	assert.Contains(t, ids, ".punt-labs/**/*.local.*")
	assert.Contains(t, ids, "vox.local.md")
	assert.Contains(t, ids, ".punt-labs/**/local/**")
	assert.Contains(t, ids, filepath.Join("biff", "local", "state.json"))

	again, err := runGitignore(context.Background(), env)
	require.NoError(t, err)
	assert.True(t, ignored(), "a second run keeps them ignored")
	assert.Equal(t, 0, again.Items)
}

func TestGitignoreSessionsLineIsDeleted(t *testing.T) {
	block := "# ethos runtime (never track) — DES-058 live zone + mission locks\n" +
		".punt-labs/**/local/**\n" +
		".punt-labs/ethos/sessions/\n" +
		".punt-labs/**/*.ethos-tmp*\n"
	t.Run("inside the block", func(t *testing.T) {
		env := fixtureEnv(t, "corpus/v4.20.4")
		repo := env.Roots.Checkout
		path := filepath.Join(repo, ".gitignore")
		require.NoError(t, os.WriteFile(path, []byte(block), 0o644))
		audit := filepath.Join(repo, ".punt-labs", "ethos", "sessions", "s1", "audit.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(audit), 0o755))
		require.NoError(t, os.WriteFile(audit, []byte("{}\n"), 0o600))
		ignored := func() bool {
			_, err := gitenv.Output(repo, "git", "check-ignore", "-q", ".punt-labs/ethos/sessions/s1/audit.jsonl")
			return err == nil
		}
		require.True(t, ignored(), "ignored before the step")

		out, err := runGitignore(context.Background(), env)
		require.NoError(t, err)
		assert.False(t, ignored(), "the sessions records are trackable after the step")
		assert.Empty(t, out.Residuals)
		assert.NotContains(t, readFixture(t, path), ".punt-labs/ethos/sessions")
	})
	t.Run("outside the block", func(t *testing.T) {
		env := fixtureEnv(t, "corpus/v4.20.4")
		path := filepath.Join(env.Roots.Checkout, ".gitignore")
		user := "\n.punt-labs/ethos/sessions/\n"
		require.NoError(t, os.WriteFile(path, []byte(block[:strings.Index(block, ".punt-labs/ethos/sessions/")]+user), 0o644))
		out, err := runGitignore(context.Background(), env)
		require.NoError(t, err)
		assert.Empty(t, out.Residuals)
		assert.NotContains(t, readFixture(t, path), ".punt-labs/ethos/sessions")
	})
}

// Step 10 refreshes hooks first; the hooks it cannot rewrite are its residuals
// too, not dropped.
func TestGitignoreCarriesHookRefreshResiduals(t *testing.T) {
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

	out, err := runGitignore(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, out.Residuals, 2)
	for _, r := range out.Residuals {
		assert.Equal(t, stepGitignore, r.Step)
	}
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

// A residual names a file relative to its repository: the machine's directory
// layout goes to the repair log, never into a stamp.
func TestResidualsNameNoAbsolutePath(t *testing.T) {
	env := fixtureEnv(t, "corpus/v4.20.4")
	repo := env.Roots.Checkout
	for _, p := range []string{
		filepath.Join(repo, ".punt-labs", "vox", "vox.local.md"),
		filepath.Join(repo, ".punt-labs", "biff", "local", "state.json"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("secret\n"), 0o600))
	}
	shared := filepath.Join(t.TempDir(), "shared-hooks")
	require.NoError(t, os.MkdirAll(shared, 0o755))
	for _, name := range []string{"pre-commit", "commit-msg"} {
		b, err := os.ReadFile(filepath.Join(repo, ".git", "hooks", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(shared, name), b, 0o755))
	}
	f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("[core]\n\thooksPath = " + shared + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"),
		[]byte(readFixture(t, filepath.Join(repo, ".gitignore"))+".punt-labs/ethos/sessions/\n"), 0o644))

	out, err := runGitignore(context.Background(), env)
	require.NoError(t, err)
	require.NotEmpty(t, out.Residuals)
	home := os.Getenv("HOME")
	for _, r := range out.Residuals {
		for _, s := range []string{r.ID, r.Reason} {
			assert.NotContains(t, s, repo, "residual %q", r.ID)
			assert.NotContains(t, s, shared, "residual %q", r.ID)
			assert.NotContains(t, s, home, "residual %q", r.ID)
		}
	}
}
