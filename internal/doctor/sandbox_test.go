package doctor

import (
	"errors"
	"os/exec"
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

	t.Run("a runaway hook is killed at the timeout, not left to hang", func(t *testing.T) {
		orig := sandboxTimeout
		sandboxTimeout = 200 * time.Millisecond
		t.Cleanup(func() { sandboxTimeout = orig })

		start := time.Now()
		observed, err := hookInvocationObserved([]byte("#!/bin/sh\nwhile :; do :; done\n"), []string{"audit", "seal"}, false)
		elapsed := time.Since(start)
		require.NoError(t, err)
		assert.False(t, observed)
		assert.Less(t, elapsed, 5*time.Second, "the infinite loop must be killed near the shortened timeout, not run to the test's own timeout")
	})
}
