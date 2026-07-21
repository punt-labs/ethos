package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFixture builds an identity.Store and matching session.Store at a
// fresh temp root. The identities directory is created so CheckIdentityDir
// passes by default; individual tests remove it when they need a failure.
func newFixture(t *testing.T) (*identity.Store, *session.Store, string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "identities"), 0o700))
	return identity.NewStore(root), session.NewStore(root), root
}

// outsideRepoTempDir creates a tempdir at /tmp so none of its ancestors
// contain a .git directory. Required for tests that must exercise the
// "not in a git repo" branch of FindRepoRoot — t.TempDir() honors
// $TMPDIR, which is set to .tmp inside the ethos repo.
func outsideRepoTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ethos-doctor-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// writeIdentity marshals a minimal identity YAML to the store. It writes
// through the filesystem directly rather than Store.Save so tests can
// seed malformed or duplicate data that Save would reject.
func writeIdentity(t *testing.T, root, handle, body string) {
	t.Helper()
	p := filepath.Join(root, "identities", handle+".yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
}

func TestResultPassed(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"PASS", true},
		{"FAIL", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			r := Result{Status: tc.status}
			assert.Equal(t, tc.want, r.Passed())
		})
	}
}

func TestCheckIdentityDir(t *testing.T) {
	t.Run("exists", func(t *testing.T) {
		s, ss, _ := newFixture(t)
		detail, ok := CheckIdentityDir(s, ss)
		assert.True(t, ok)
		assert.Equal(t, s.IdentitiesDir(), detail)
	})

	t.Run("missing", func(t *testing.T) {
		s, ss, root := newFixture(t)
		require.NoError(t, os.RemoveAll(filepath.Join(root, "identities")))
		detail, ok := CheckIdentityDir(s, ss)
		assert.False(t, ok)
		assert.Contains(t, detail, "not found")
	})
}

func TestCheckHumanIdentity(t *testing.T) {
	// Pin USER so resolve.Resolve never falls back to the developer's
	// real shell identity. Each sub-test overrides as needed.
	t.Setenv("USER", "nobody-doctor-test")
	// Set HOME so git config picks up nothing surprising.
	t.Setenv("HOME", t.TempDir())

	t.Run("happy path", func(t *testing.T) {
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "mal",
			"name: Mal\nhandle: mal\nkind: human\n")
		t.Setenv("USER", "mal")
		detail, ok := CheckHumanIdentity(s, ss)
		assert.True(t, ok, "detail: %s", detail)
		assert.Contains(t, detail, "mal")
	})

	t.Run("no match", func(t *testing.T) {
		s, ss, _ := newFixture(t)
		t.Setenv("USER", "ghost")
		detail, ok := CheckHumanIdentity(s, ss)
		assert.False(t, ok)
		assert.Contains(t, detail, "no match")
	})

	t.Run("malformed file", func(t *testing.T) {
		s, ss, root := newFixture(t)
		// Write a file that matches $USER by name, but malformed YAML is
		// skipped during resolution, so lookup fails with no match before
		// any direct load is attempted.
		writeIdentity(t, root, "bad", "not: [valid: yaml")
		t.Setenv("USER", "bad")
		detail, ok := CheckHumanIdentity(s, ss)
		assert.False(t, ok)
		assert.Contains(t, detail, "no match")
	})
}

func TestCheckDefaultAgent(t *testing.T) {
	s, ss, _ := newFixture(t)

	t.Run("not in a repo", func(t *testing.T) {
		// t.TempDir honors $TMPDIR which is set to .tmp inside the
		// ethos repo by .envrc. Walking up from that path would find
		// the real repo's .git and its .punt-labs/ethos.yaml. Use
		// /tmp directly so FindRepoRoot walks up to / without
		// finding any .git.
		dir := outsideRepoTempDir(t)
		t.Chdir(dir)
		detail, ok := CheckDefaultAgent(s, ss)
		assert.True(t, ok)
		assert.Equal(t, "not in a git repo", detail)
	})

	t.Run("repo without config", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o700))
		t.Chdir(dir)
		detail, ok := CheckDefaultAgent(s, ss)
		assert.True(t, ok)
		assert.Equal(t, "not configured", detail)
	})

	t.Run("repo with agent configured", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o700))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".punt-labs"), 0o700))
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, ".punt-labs", "ethos.yaml"),
			[]byte("agent: claude\n"), 0o600))
		t.Chdir(dir)
		detail, ok := CheckDefaultAgent(s, ss)
		assert.True(t, ok)
		assert.Equal(t, "claude", detail)
	})

	t.Run("malformed config", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o700))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".punt-labs"), 0o700))
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, ".punt-labs", "ethos.yaml"),
			[]byte("agent: [not a string"), 0o600))
		t.Chdir(dir)
		detail, ok := CheckDefaultAgent(s, ss)
		assert.False(t, ok)
		assert.NotEmpty(t, detail)
	})
}

func TestCheckDuplicateFields(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "mal",
			"name: Mal\nhandle: mal\nkind: human\nemail: mal@example.com\ngithub: mal\n")
		writeIdentity(t, root, "zoe",
			"name: Zoe\nhandle: zoe\nkind: human\nemail: zoe@example.com\ngithub: zoe\n")
		detail, ok := CheckDuplicateFields(s, ss)
		assert.True(t, ok, "detail: %s", detail)
		assert.Equal(t, "no duplicates", detail)
	})

	t.Run("duplicate github", func(t *testing.T) {
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "mal",
			"name: Mal\nhandle: mal\nkind: human\ngithub: shared\n")
		writeIdentity(t, root, "zoe",
			"name: Zoe\nhandle: zoe\nkind: human\ngithub: shared\n")
		detail, ok := CheckDuplicateFields(s, ss)
		assert.False(t, ok)
		assert.Contains(t, detail, "github")
		assert.Contains(t, detail, "shared")
	})

	t.Run("duplicate email", func(t *testing.T) {
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "mal",
			"name: Mal\nhandle: mal\nkind: human\nemail: same@example.com\n")
		writeIdentity(t, root, "zoe",
			"name: Zoe\nhandle: zoe\nkind: human\nemail: same@example.com\n")
		detail, ok := CheckDuplicateFields(s, ss)
		assert.False(t, ok)
		assert.Contains(t, detail, "email")
		assert.Contains(t, detail, "same@example.com")
	})

	t.Run("ignores empty fields", func(t *testing.T) {
		// Two identities with no email/github must not count as duplicates.
		s, ss, root := newFixture(t)
		writeIdentity(t, root, "mal", "name: Mal\nhandle: mal\nkind: human\n")
		writeIdentity(t, root, "zoe", "name: Zoe\nhandle: zoe\nkind: human\n")
		detail, ok := CheckDuplicateFields(s, ss)
		assert.True(t, ok, "detail: %s", detail)
		assert.Equal(t, "no duplicates", detail)
	})
}

func TestCheckSealHook(t *testing.T) {
	// writeHook creates a repo with .git/hooks/pre-commit holding body.
	writeHook := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(body), 0o755))
		return dir
	}

	t.Run("not in a repo", func(t *testing.T) {
		r := CheckSealHook("")
		assert.True(t, r.Passed())
		assert.Equal(t, "not in a repo", r.Detail)
	})

	t.Run("no pre-commit hook", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "missing")
		assert.Contains(t, r.Detail, "install.sh")
	})

	t.Run("standalone seal hook", func(t *testing.T) {
		dir := writeHook(t, "#!/bin/sh\n# DES-058\nethos audit seal || exit 2\n")
		r := CheckSealHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "standalone")
	})

	t.Run("chained seal section", func(t *testing.T) {
		body := "#!/bin/sh\nbd hooks run pre-commit || exit 1\n" +
			"# --- BEGIN ETHOS DES-058 SEAL ---\nethos audit seal || exit 2\n" +
			"# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeHook(t, body)
		r := CheckSealHook(dir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "chained")
	})

	t.Run("stale section without seal call", func(t *testing.T) {
		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-058 SEAL ---\n" +
			"echo placeholder\n# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "stale")
	})

	t.Run("commented-out seal call is not active", func(t *testing.T) {
		// A disabled invocation must not read as active — that is exactly
		// the silent-absence recurrence the check exists to catch.
		body := "#!/bin/sh\n# --- BEGIN ETHOS DES-058 SEAL ---\n" +
			"# if ! ethos audit seal; then exit 2; fi\n" +
			"# --- END ETHOS DES-058 SEAL ---\n"
		dir := writeHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "stale")
	})

	t.Run("mention in a foreign comment is not active", func(t *testing.T) {
		body := "#!/bin/sh\n# TODO: wire up ethos audit seal here\nrun_lint\n"
		dir := writeHook(t, body)
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "missing")
	})

	t.Run("non-executable hook fails with chmod remedy", func(t *testing.T) {
		dir := t.TempDir()
		hooks := filepath.Join(dir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"), 0o644))
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "not executable")
		assert.Contains(t, r.Detail, "chmod +x")
	})

	t.Run("foreign hook without seal", func(t *testing.T) {
		dir := writeHook(t, "#!/bin/sh\nbd hooks run pre-commit || exit 1\n")
		r := CheckSealHook(dir)
		assert.False(t, r.Passed())
		assert.Contains(t, r.Detail, "missing")
	})

	t.Run("gitdir file redirects hooks path", func(t *testing.T) {
		// A submodule .git file points directly at a dir with hooks/ and
		// no commondir — the hooks dir is target/hooks.
		real := t.TempDir()
		hooks := filepath.Join(real, "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"), 0o755))
		wt := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"),
			[]byte("gitdir: "+real+"\n"), 0o644))
		r := CheckSealHook(wt)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
	})

	t.Run("worktree resolves hooks through commondir", func(t *testing.T) {
		// Real worktree layout: the .git file points at
		// <main>/.git/worktrees/<name>, which has a commondir file (../..)
		// back to the main .git — where the hooks actually live. The seal
		// hook at the per-worktree admin dir must be ignored; only the
		// common hooks dir counts.
		mainGit := t.TempDir() // stands in for <main>/.git
		commonHooks := filepath.Join(mainGit, "hooks")
		require.NoError(t, os.MkdirAll(commonHooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(commonHooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"), 0o755))

		wtAdmin := filepath.Join(mainGit, "worktrees", "wt")
		require.NoError(t, os.MkdirAll(filepath.Join(wtAdmin, "hooks"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(wtAdmin, "commondir"),
			[]byte("../..\n"), 0o644))
		// A decoy hook at the dead path git never runs — must be ignored.
		require.NoError(t, os.WriteFile(filepath.Join(wtAdmin, "hooks", "pre-commit"),
			[]byte("#!/bin/sh\necho decoy\n"), 0o755))

		wt := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"),
			[]byte("gitdir: "+wtAdmin+"\n"), 0o644))

		r := CheckSealHook(wt)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "standalone")
	})

	t.Run("worktree via real git worktree add", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		mainDir := t.TempDir()
		gitRun := func(dir string, args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"HOME="+t.TempDir(),
				"GIT_CONFIG_GLOBAL=/dev/null",
				"GIT_CONFIG_SYSTEM=/dev/null",
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
				"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "git %v: %s", args, out)
		}
		gitRun(mainDir, "init", "-q")
		gitRun(mainDir, "commit", "--allow-empty", "-q", "-m", "init")

		// The hook git actually runs lives in the main repo's hooks dir.
		hooks := filepath.Join(mainDir, ".git", "hooks")
		require.NoError(t, os.MkdirAll(hooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(hooks, "pre-commit"),
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"), 0o755))

		wtDir := filepath.Join(t.TempDir(), "wt")
		gitRun(mainDir, "worktree", "add", "-q", wtDir, "-b", "b")

		r := CheckSealHook(wtDir)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
	})

	t.Run("honors core.hooksPath like the installer", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		repo := t.TempDir()
		cmd := exec.Command("git", "-C", repo, "init", "-q")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git init: %s", out)
		cmd = exec.Command("git", "-C", repo, "config", "core.hooksPath", ".husky")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err = cmd.CombinedOutput()
		require.NoError(t, err, "git config: %s", out)

		// The seal lives where git runs hooks — the tracked .husky dir, the
		// same path the installer resolves via `git rev-parse --git-path hooks`.
		husky := filepath.Join(repo, ".husky")
		require.NoError(t, os.MkdirAll(husky, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(husky, "pre-commit"),
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"), 0o755))
		// A seal in .git/hooks must NOT satisfy the check — git never runs it.
		defaultHooks := filepath.Join(repo, ".git", "hooks")
		require.NoError(t, os.MkdirAll(defaultHooks, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(defaultHooks, "pre-commit"),
			[]byte("#!/bin/sh\necho decoy\n"), 0o755))

		r := CheckSealHook(repo)
		assert.True(t, r.Passed(), "detail: %s", r.Detail)
	})
}

func TestRunAllAndHelpers(t *testing.T) {
	// A fixture that passes all four checks initially, including
	// human-identity via USER=mal matching the mal identity.
	s, ss, root := newFixture(t)
	writeIdentity(t, root, "mal",
		"name: Mal\nhandle: mal\nkind: human\n")

	t.Setenv("USER", "mal")
	t.Setenv("HOME", t.TempDir())

	// CheckDefaultAgent walks up from CWD looking for .git. Put us in a
	// fresh temp dir that is definitely not inside a git repo — a repo
	// ancestor would cause the walk to find .git several directories up
	// and trigger a non-deterministic result.
	t.Chdir(outsideRepoTempDir(t))

	// Pass empty repoRoot and nil teams — the orphaned-agent check
	// degrades to PASS ("not in a repo") in this configuration.
	results := RunAll(s, ss, "", nil)
	require.Len(t, results, 6)

	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Name
	}
	assert.Equal(t, []string{
		"Identity directory",
		"Human identity",
		"Default agent",
		"Duplicate fields",
		"Orphaned agent files",
		"Audit seal hook",
	}, names)

	assert.True(t, AllPassed(results), "results: %+v", results)
	assert.Equal(t, 6, PassedCount(results))

	// Now inject a failure: remove the identities directory. RunAll
	// should report at least one failure and AllPassed should flip.
	require.NoError(t, os.RemoveAll(filepath.Join(root, "identities")))
	results = RunAll(s, ss, "", nil)
	assert.False(t, AllPassed(results))
	assert.Less(t, PassedCount(results), 6)

	// At least one result should name the identity directory failure.
	var found bool
	for _, r := range results {
		if r.Name == "Identity directory" && !r.Passed() {
			found = true
			assert.True(t, strings.Contains(r.Detail, "not found") ||
				strings.Contains(r.Detail, "error"))
		}
	}
	assert.True(t, found)
}
