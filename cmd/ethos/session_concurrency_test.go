//go:build linux || darwin

package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spawnCLI runs the ethos binary and returns stdout, stderr, and exit
// code without any t.Fatal — safe to call from goroutines (runCLI's
// require/t.Fatalf are not). A non-ExitError failure returns code -1 and
// the error for the caller to assert on the test goroutine.
func spawnCLI(se *cliSubprocessEnv, args ...string) (stdout, stderr string, code int, err error) {
	cmd := exec.Command(ethosBinary, args...)
	cmd.Dir = se.repo
	cmd.Env = se.env
	var o, e bytes.Buffer
	cmd.Stdout = &o
	cmd.Stderr = &e
	runErr := cmd.Run()
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			return o.String(), e.String(), ee.ExitCode(), nil
		}
		return o.String(), e.String(), -1, runErr
	}
	return o.String(), e.String(), 0, nil
}

// TestCLI_ConcurrentSessionStart_DistinctIDs races several `session start`
// invocations in the same env. Each mints its own crypto/rand id, so all
// ids must be distinct and every roster must be intact.
func TestCLI_ConcurrentSessionStart_DistinctIDs(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := seededSessionEnv(t)

	const n = 4
	outs := make([]string, n)
	codes := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], _, codes[i], errs[i] = spawnCLI(se, "session", "start")
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool)
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		require.Equal(t, 0, codes[i], "session start %d should exit 0", i)
		m := sessionExportRe.FindStringSubmatch(outs[i])
		require.NotNil(t, m, "start %d must emit a 32-hex id; got %q", i, outs[i])
		id := m[1]
		assert.False(t, seen[id], "session ids must be distinct across concurrent starts")
		seen[id] = true
		// The roster must be intact and loadable.
		_, stderr, code := runCLI(t, se, "session", "show", id)
		assert.Equal(t, 0, code, "roster %s should load; stderr=%s", id, stderr)
	}
	assert.Len(t, seen, n, "every concurrent start minted a distinct session")
}

// TestCLI_ConcurrentIam_FlockNoLoss races several `iam` joins against one
// session, each keyed on a distinct ETHOS_AGENT_ID. The store's flock must
// serialize the read-modify-write so no join is lost — all participants
// survive.
func TestCLI_ConcurrentIam_FlockNoLoss(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := seededSessionEnv(t)

	out, _, code := runCLI(t, se, "session", "start")
	require.Equal(t, 0, code)
	id := sessionExportRe.FindStringSubmatch(out)[1]

	const n = 5
	codes := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct agent id per join, so each is a separate participant.
			sh := withSession(se, id, "agent-"+string(rune('a'+i)))
			_, _, codes[i], errs[i] = spawnCLI(sh, "iam", "persona-"+string(rune('a'+i)))
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		require.Equal(t, 0, codes[i], "concurrent iam %d should exit 0", i)
	}

	// Every join must be present in the final roster — no lost update.
	stdout, _, code := runCLI(t, withSession(se, id, ""), "session", "show", id)
	require.Equal(t, 0, code)
	for i := 0; i < n; i++ {
		persona := "persona-" + string(rune('a'+i))
		assert.Contains(t, stdout, persona, "flock must preserve every concurrent join: %s", persona)
	}
}

// TestCLI_DoubleSessionEnd_NoOp pins that ending an already-ended session
// (env still set, roster gone) is a clean no-op with a stderr note — end is
// teardown, so "already gone" is success (the rm -f norm), preserving the
// trap-handler/rc cleanup pattern. H2's hard verification is scoped to
// state-writers (claim/release/iam), not teardown.
func TestCLI_DoubleSessionEnd_NoOp(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := seededSessionEnv(t)
	out, _, code := runCLI(t, se, "session", "start")
	require.Equal(t, 0, code)
	id := sessionExportRe.FindStringSubmatch(out)[1]
	sh := withSession(se, id, "")

	_, _, code = runCLI(t, sh, "session", "end")
	require.Equal(t, 0, code, "first end should succeed")

	_, stderr, code := runCLI(t, sh, "session", "end")
	assert.Equal(t, 0, code, "a second end with the env still set is a clean no-op; stderr=%s", stderr)
	assert.Contains(t, stderr, "nothing to end", "the no-op must note the already-gone session")
}

// TestCLI_IamAfterEnd_NoResurrection pins that iam after the session ended
// does not recreate the roster. With the env still set it fails at roster
// load (not a resurrection); with the env unset it returns the step-4
// no-active-session error.
func TestCLI_IamAfterEnd_NoResurrection(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := seededSessionEnv(t)
	out, _, code := runCLI(t, se, "session", "start")
	require.Equal(t, 0, code)
	id := sessionExportRe.FindStringSubmatch(out)[1]
	sh := withSession(se, id, "claude")

	_, _, code = runCLI(t, sh, "session", "end")
	require.Equal(t, 0, code)

	rosterPath := filepath.Join(se.home, ".punt-labs", "ethos", "sessions", id+".yaml")

	// (a) env still set → fails at load, roster not resurrected.
	_, stderr, code := runCLI(t, sh, "iam", "claude")
	assert.NotEqual(t, 0, code, "iam after end must fail")
	assert.Contains(t, stderr, "not found")
	assert.NoFileExists(t, rosterPath, "iam must not recreate the ended roster")

	// (b) env unset → step-4 no-active-session error (walk finds nothing).
	_, stderr, code = runCLI(t, se, "iam", "claude")
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "no active session")
}
