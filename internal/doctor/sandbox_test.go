package doctor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/plugin/hooks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHookInvocationObserved pins the execution-based detector directly
// (ethos-kcbv), independent of checkHookPresence's PASS/FAIL wiring.
func TestHookInvocationObserved(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("real invocation is observed", func(t *testing.T) {
		observed, err := hookInvocationObserved(
			[]byte("#!/bin/sh\nethos audit seal || exit 2\n"),
			[]string{"audit", "seal"}, false)
		require.NoError(t, err)
		assert.True(t, observed)
	})

	t.Run("no invocation at all", func(t *testing.T) {
		observed, err := hookInvocationObserved(
			[]byte("#!/bin/sh\necho nothing to see here\n"),
			[]string{"audit", "seal"}, false)
		require.NoError(t, err)
		assert.False(t, observed)
	})

	t.Run("a different ethos subcommand is not the invocation being looked for", func(t *testing.T) {
		observed, err := hookInvocationObserved(
			[]byte("#!/bin/sh\nethos whoami\n"),
			[]string{"audit", "seal"}, false)
		require.NoError(t, err)
		assert.False(t, observed, "a call to a different subcommand must not read as the one being probed")
	})

	t.Run("eval resolves correctly — a lexical scanner's documented blind spot", func(t *testing.T) {
		// hasActiveSealCall's doc comment named eval as a case it FAILs
		// safe on because it cannot see through it. Execution has no such
		// limitation: the shell actually evaluates the string.
		observed, err := hookInvocationObserved(
			[]byte("#!/bin/sh\ncmd='ethos audit seal'\neval \"$cmd\"\n"),
			[]string{"audit", "seal"}, false)
		require.NoError(t, err)
		assert.True(t, observed)
	})

	t.Run("commit-msg style call needs the message-file argument", func(t *testing.T) {
		// The hook refuses to do anything without $1; needsMsgArg=false
		// would leave $1 empty and the call would never happen.
		body := []byte("#!/bin/sh\n[ -z \"$1\" ] && exit 0\nethos hook commit-trailers\n")
		observed, err := hookInvocationObserved(body, []string{"hook", "commit-trailers"}, true)
		require.NoError(t, err)
		assert.True(t, observed)

		observed, err = hookInvocationObserved(body, []string{"hook", "commit-trailers"}, false)
		require.NoError(t, err)
		assert.False(t, observed, "without $1 the hook must exit before ever calling ethos")
	})

	t.Run("the real DES-058 pre-commit hook is observed calling audit seal", func(t *testing.T) {
		observed, err := hookInvocationObserved(hooks.PreCommit, []string{"audit", "seal"}, false)
		require.NoError(t, err)
		assert.True(t, observed, "the production hook body must be seen invoking ethos audit seal")
	})

	t.Run("the real DES-054 commit-msg hook is observed calling hook commit-trailers", func(t *testing.T) {
		observed, err := hookInvocationObserved(hooks.CommitMsg, []string{"hook", "commit-trailers"}, true)
		require.NoError(t, err)
		assert.True(t, observed, "the production hook body must be seen invoking ethos hook commit-trailers")
	})

	t.Run("extra trailing argv words still count as the invocation (M2)", func(t *testing.T) {
		// A hook calling `ethos audit seal --quiet` logs "audit seal --quiet"
		// to the stub — checkHookPresence is only looking for the leading
		// "audit seal" subcommand, not an exact-argv match.
		observed, err := hookInvocationObserved(
			[]byte("#!/bin/sh\nethos audit seal --quiet || exit 2\n"),
			[]string{"audit", "seal"}, false)
		require.NoError(t, err)
		assert.True(t, observed, "a trailing flag after the matched subcommand must not defeat detection")
	})

	t.Run("a word that merely starts with the argv words is not a match", func(t *testing.T) {
		// "audit sealed" must not satisfy a search for "audit seal" — the
		// prefix match needs a boundary, not a bare strings.HasPrefix.
		observed, err := hookInvocationObserved(
			[]byte("#!/bin/sh\nethos audit sealed\n"),
			[]string{"audit", "seal"}, false)
		require.NoError(t, err)
		assert.False(t, observed, "a longer subcommand sharing the prefix must not read as a match")
	})

	t.Run("git unavailable is reported, not silently swallowed", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := hookInvocationObserved([]byte("#!/bin/sh\nethos audit seal\n"), []string{"audit", "seal"}, false)
		require.Error(t, err)
		assert.True(t, errors.Is(err, errSandboxGitUnavailable), "err = %v, want errSandboxGitUnavailable", err)
	})

	t.Run("shebang-less hook runs via git's ENOEXEC shell fallback (H2)", func(t *testing.T) {
		// No "#!" line at all. A bare execve of this file fails with
		// ENOEXEC; git's own run-command falls back to `sh -c '"$0" "$@"'`
		// in exactly this case, so this sandbox must too, or a real
		// shebang-less pre-commit that git runs fine reads as unexecutable.
		observed, err := hookInvocationObserved(
			[]byte("ethos audit seal || exit 2\n"),
			[]string{"audit", "seal"}, false)
		require.NoError(t, err)
		assert.True(t, observed, "a shebang-less hook must still be exercised via the shell fallback")
	})

	t.Run("a shebang naming a missing interpreter is reported as unexecutable, not a plain miss (H1)", func(t *testing.T) {
		observed, err := hookInvocationObserved(
			[]byte("#!/nonexistent/path/bash\nethos audit seal\n"),
			[]string{"audit", "seal"}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not be executed at all")
		assert.False(t, observed)
	})

	t.Run("a host section that exits before reaching ethos reports why, not a plain miss (H1)", func(t *testing.T) {
		observed, err := hookInvocationObserved(
			[]byte("#!/bin/sh\nexit 3\nethos audit seal\n"),
			[]string{"audit", "seal"}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exited 3 before reaching the ethos call")
		assert.False(t, observed)
	})

	t.Run("a hook that runs to completion and simply never calls ethos stays a plain miss", func(t *testing.T) {
		// runErr == nil must stay (false, nil) — this is the one case
		// doctor's existing "stale"/"not chained" messaging already covers,
		// and it must not gain a redundant classification error.
		observed, err := hookInvocationObserved(
			[]byte("#!/bin/sh\necho nothing to see here\nexit 0\n"),
			[]string{"audit", "seal"}, false)
		require.NoError(t, err)
		assert.False(t, observed)
	})

	t.Run("a CRLF-terminated shebang line reports unexecutable, not a plain silent miss (S3)", func(t *testing.T) {
		// textscan.IsShellHook strips \r for classification and reads
		// "#!/bin/sh\r\n" as shell — matching how such a hook is edited on
		// Windows or checked out with core.autocrlf. The kernel does not
		// strip it: the interpreter path becomes "/bin/sh\r", execve fails
		// (ENOENT — no such interpreter), and this must surface as
		// "could not be executed at all", the same bucket a missing
		// interpreter already gets, not a silent (false, nil) that reads
		// identically to a hook that ran fine and genuinely never calls
		// ethos.
		observed, err := hookInvocationObserved(
			[]byte("#!/bin/sh\r\nethos audit seal\r\n"),
			[]string{"audit", "seal"}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not be executed at all")
		assert.False(t, observed)
	})

	t.Run("a runaway hook is killed at the timeout, and reports a distinct timeout error (H1)", func(t *testing.T) {
		orig := sandboxTimeout
		sandboxTimeout = 200 * time.Millisecond
		t.Cleanup(func() { sandboxTimeout = orig })

		start := time.Now()
		observed, err := hookInvocationObserved([]byte("#!/bin/sh\nwhile :; do :; done\n"), []string{"audit", "seal"}, false)
		elapsed := time.Since(start)
		require.Error(t, err, "a timed-out hook must report why the stub was never reached, not read as a plain inactive hook")
		assert.Contains(t, err.Error(), "did not finish within")
		assert.Contains(t, err.Error(), "hanging")
		assert.False(t, observed)
		assert.Less(t, elapsed, 5*time.Second, "the infinite loop must be killed near the shortened timeout, not run to the test's own timeout")
	})
}

// realGitRepo git-inits dir and returns it, standing in for whatever real
// repo `ethos doctor` is actually checking — the sandbox must never resolve
// into it, no matter what the caller's environment points at.
func realGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init", "-q")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init: %s", out)
	return dir
}

// TestHookInvocationObserved_SandboxEscape pins S1: a caller-inherited git
// environment variable must never redirect the sandbox's own `git init` or
// the hook's own `git rev-parse --show-toplevel` at the real repository
// being checked. Proven pre-fix with GIT_DIR: `git init` inside the sandbox
// returned exit 0 while creating no repository at dir, and the hook's own
// rev-parse silently resolved to the real repo — the untrusted hook body
// then ran, and wrote, inside the real checkout, with
// hookInvocationObserved still reporting a clean (true, nil) result.
func TestHookInvocationObserved_SandboxEscape(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	cases := []struct {
		name   string
		envVar string
		value  func(realRepo string) string
	}{
		{"GIT_DIR points at the real repo", "GIT_DIR", func(r string) string { return filepath.Join(r, ".git") }},
		{"GIT_OBJECT_DIRECTORY points at the real repo", "GIT_OBJECT_DIRECTORY", func(r string) string { return filepath.Join(r, ".git", "objects") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			realRepo := realGitRepo(t)
			witness := filepath.Join(t.TempDir(), "witness")
			body := []byte("#!/bin/sh\ntop=$(git rev-parse --show-toplevel 2>&1)\n" +
				"printf '%s' \"$top\" > " + shQuote(witness) + "\n" +
				"ethos audit seal\n")

			t.Setenv(tc.envVar, tc.value(realRepo))
			observed, err := hookInvocationObserved(body, []string{"audit", "seal"}, false)
			// The sandbox must keep functioning under a hostile-looking
			// caller environment (it is not; this is a legitimate
			// inherited variable) — it must just not leak into it.
			require.NoError(t, err, "sandbox must still function under an inherited %s, not merely refuse", tc.envVar)
			assert.True(t, observed)

			data, rerr := os.ReadFile(witness)
			require.NoError(t, rerr)
			top := strings.TrimSpace(string(data))
			assert.NotEqual(t, realRepo, top,
				"the sandboxed hook's own `git rev-parse --show-toplevel` resolved to the REAL repo under an inherited %s — sandbox escape (S1)", tc.envVar)
			assert.False(t, strings.HasPrefix(realRepo, top) || strings.HasPrefix(top, realRepo),
				"toplevel %q must not be the real repo %q or contain/be contained by it", top, realRepo)
		})
	}
}

// TestHookInvocationObserved_UnsupportedPlatform pins M4: this sandbox
// cannot be exercised on Windows, and must say so honestly rather than
// silently degrading to a wrong FAIL. Outside the git-availability skip of
// TestHookInvocationObserved above — the Windows guard fires before git is
// ever consulted, so this must run unconditionally.
func TestHookInvocationObserved_UnsupportedPlatform(t *testing.T) {
	orig := sandboxGOOS
	sandboxGOOS = "windows"
	t.Cleanup(func() { sandboxGOOS = orig })

	observed, err := hookInvocationObserved([]byte("ethos audit seal\n"), []string{"audit", "seal"}, false)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errSandboxUnsupportedPlatform), "err = %v, want errSandboxUnsupportedPlatform", err)
	assert.False(t, observed)
}

// TestHookInvocationObserved_ProcessGroupCleanup pins S4: a hook that
// backgrounds a child must not leave it running (and writing into a
// directory that is about to be removed) after hookInvocationObserved
// returns. cmd.Cancel alone cannot catch this — the direct child (the
// shell) exits quickly and normally after backgrounding, so the sandbox
// context never times out and Cancel never fires; the fix has to reap the
// process group unconditionally after Run, not only on cancellation.
func TestHookInvocationObserved_ProcessGroupCleanup(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	cases := []struct {
		name string
		body func(witness string) []byte
	}{
		{
			name: "backgrounded subshell",
			body: func(w string) []byte {
				return []byte("#!/bin/sh\n(sleep 2; touch " + shQuote(w) + ") &\nexit 0\n")
			},
		},
		{
			name: "nohup'd daemon",
			body: func(w string) []byte {
				return []byte("#!/bin/sh\nnohup sh -c 'sleep 2; touch " + shQuote(w) + "' >/dev/null 2>&1 &\nexit 0\n")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			witnessDir := t.TempDir()
			witness := filepath.Join(witnessDir, "survived")

			observed, err := hookInvocationObserved(tc.body(witness), []string{"audit", "seal"}, false)
			require.NoError(t, err)
			assert.False(t, observed, "the direct child never calls ethos in this fixture")

			time.Sleep(2500 * time.Millisecond)
			_, statErr := os.Stat(witness)
			assert.True(t, os.IsNotExist(statErr),
				"a backgrounded child survived hookInvocationObserved returning — process-group cleanup failed (S4)")
		})
	}
}
