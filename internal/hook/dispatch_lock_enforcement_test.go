//go:build unix

package hook

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

type lockDispatchResult struct {
	response PreToolUseResult
	err      error
}

func runLockDispatch(r io.Reader) lockDispatchResult {
	var out bytes.Buffer
	if err := HandlePreToolUse(r, &out); err != nil {
		return lockDispatchResult{err: err}
	}
	var result PreToolUseResult
	err := json.Unmarshal(out.Bytes(), &result)
	return lockDispatchResult{response: result, err: err}
}

// A separate open file description holds LOCK_SH on the canonical-local
// mission lock. Both actual dispatches must FINISH before that holder is
// released. Distinct sessions avoid serialization by the session lock.
// An exclusive-mission-lock mutant blocks here even if it still eventually
// produces distinct delegation directories after the holder is released.
func TestDispatchMissionSharedLockEnforcement(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := stageRepoRoot(t)
	t.Setenv("ETHOS_REPO_ROOT", "")
	const missionID = "m-2026-09-08-801"
	stageContract(t, os.Getenv("HOME"), missionID)
	t.Setenv("MISSION_ID", missionID)
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("PARENT_SESSION_ID", "")
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("ETHOS_QUIET_ADVICE", "1")

	policy, err := layout.NewRepository(repo, repo, layout.RecordBlame)
	require.NoError(t, err)
	path, err := policy.LockPath("mission-delegations", missionID)
	require.NoError(t, err)
	require.NoError(t, layout.EnsureLocalDir(policy, filepath.Dir(path)))
	held, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	// Use the OS primitive, not AcquireMissionLock: the independent holder
	// must stay shared even when mutation-testing that production function.
	var workers sync.WaitGroup
	defer func() {
		// Closing releases the independent holder before draining workers,
		// including on a mutant's timeout. No goroutine outlives env/cwd cleanup.
		assert.NoError(t, held.Close())
		waitLockDispatches(t, &workers)
	}()
	require.NoError(t, syscall.Flock(int(held.Fd()), syscall.LOCK_SH))
	results := make(chan lockDispatchResult, 2)
	for _, sessionID := range []string{"sess-shared-one", "sess-shared-two"} {
		payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`
		workers.Go(func() { results <- runLockDispatch(strings.NewReader(payload)) })
	}

	var dirs []string
	for range 2 {
		select {
		case result := <-results:
			require.NoError(t, result.err)
			assert.Empty(t, result.response.HookSpecificOutput.Decision())
			assert.Equal(t, missionID, result.response.HookSpecificOutput.AdditionalEnv["MISSION_ID"])
			dir := result.response.HookSpecificOutput.AdditionalEnv["MISSION_ARTIFACTS_DIR"]
			require.NotEmpty(t, dir)
			dirs = append(dirs, dir)
		case <-time.After(5 * time.Second):
			t.Fatal("actual dispatch did not finish while the independent shared mission lock remained held")
		}
	}
	assert.NotEqual(t, dirs[0], dirs[1])
}

// The first real hook call is paused after matching but before acquiring the
// mission lock or consuming its pending entry. A nonblocking OS probe proves
// that this first call still owns the production per-session lock. Only then
// does a second real hook invocation start reading its input in that window;
// it cannot mask an early release by transiently holding the lock itself.
// Removing the lock or releasing it before admission fails the probe regardless
// of scheduling. Final mission IDs also check consumption by both actual calls.
func TestDispatchPendingLockEnforcement(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)
	t.Setenv("ETHOS_REPO_ROOT", "")
	const missionA = "m-2026-09-08-802"
	const missionB = "m-2026-09-08-803"
	const sessionID = "sess-pending-lock"
	stageContract(t, home, missionA)
	stageContractCustomWriteSet(t, home, missionB, []string{"docs/"})
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionA, "bwk"))
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionB, "bwk"))
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("PARENT_SESSION_ID", "")
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("ETHOS_QUIET_ADVICE", "1")

	matched := make(chan struct{})
	resume := make(chan struct{})
	var first, release sync.Once
	old := dispatchTierBConfirmedOpen
	dispatchTierBConfirmedOpen = func() {
		first.Do(func() {
			close(matched)
			<-resume
		})
	}
	var workers sync.WaitGroup
	defer func() {
		release.Do(func() { close(resume) })
		waitLockDispatches(t, &workers)
		dispatchTierBConfirmedOpen = old
	}()

	payload := `{"tool_name":"Agent","tool_input":{"subagent_type":"bwk"},"session_id":"` + sessionID + `"}`
	results := make(chan lockDispatchResult, 2)
	workers.Go(func() { results <- runLockDispatch(strings.NewReader(payload)) })
	select {
	case <-matched:
	case <-time.After(5 * time.Second):
		t.Fatal("first actual dispatch did not reach the post-match admission boundary")
	}
	// Neither entry has been consumed at this synchronized boundary.
	pending, warnings, err := mission.ReadDispatchPending(globalRoot, sessionID)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, pending, 2)

	// Open the actual production lock independently. No test-owned lock
	// protects the match/consume path. LOCK_NB gives a positive runtime
	// observation of ownership, not a sleep-based inference of blocking.
	// Probe before starting the second call: otherwise that call could
	// transiently own the lock and mask the first call's early release.
	path := filepath.Join(globalRoot, "sessions", sessionID, "dispatch-pending", ".lock")
	probe, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if assert.NoError(t, err) {
		err = syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		assert.ErrorIs(t, err, syscall.EWOULDBLOCK,
			"production pending lock must remain held between match and consume")
		// Close also releases the probe's lock if a mutant let it acquire.
		assert.NoError(t, probe.Close())
	}

	reading := make(chan struct{})
	reader := &lockDispatchReader{Reader: strings.NewReader(payload), reading: reading}
	workers.Go(func() { results <- runLockDispatch(reader) })
	select {
	case <-reading:
	case <-time.After(5 * time.Second):
		t.Fatal("second actual hook call did not begin reading while the first was paused before consume")
	}
	release.Do(func() { close(resume) })

	var missions []string
	for range 2 {
		select {
		case result := <-results:
			require.NoError(t, result.err)
			assert.Empty(t, result.response.HookSpecificOutput.Decision())
			missions = append(missions, result.response.HookSpecificOutput.AdditionalEnv["MISSION_ID"])
		case <-time.After(5 * time.Second):
			t.Fatal("actual dispatches did not finish after releasing the admission barrier")
		}
	}
	assert.ElementsMatch(t, []string{missionA, missionB}, missions)
	pending, warnings, err = mission.ReadDispatchPending(globalRoot, sessionID)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Empty(t, pending)
}

// lockDispatchReader signals from inside HandlePreToolUse's input read, after
// its entry-time root snapshot. It does not call matching or acquire a lock.
type lockDispatchReader struct {
	*strings.Reader
	reading chan struct{}
	once    sync.Once
}

func (r *lockDispatchReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.reading) })
	return r.Reader.Read(p)
}

var _ io.Reader = (*lockDispatchReader)(nil)

func waitLockDispatches(t *testing.T, workers *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch workers did not drain before test cleanup")
	}
}
