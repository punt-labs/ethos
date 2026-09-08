//go:build !windows

package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

// TestStore_IDPathSanitization pins the ID→path defense at the exact line
// it lives: rosterPath and lockPath run the session ID through
// filepath.Base, so a traversal-shaped ID (from an attacker-controlled
// ETHOS_SESSION) can never derive a path outside the sessions dir. The
// store sanitizes rather than rejects — this table asserts sanitization.
// If filepath.Base is ever dropped, this fails loudly here rather than
// only in the end-to-end subprocess tests.
func TestStore_IDPathSanitization(t *testing.T) {
	s := testStore(t)
	sessionsDir := s.sessionsDir()

	cases := []struct {
		name string
		id   string
	}{
		{"clean", "abc123"},
		{"dotdot", "../escape"},
		{"dotdot-deep", "../../../../etc/passwd"},
		{"absolute", "/etc/passwd"},
		{"embedded-slash", "a/b/c"},
		{"trailing-slash", "evil/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range []string{s.rosterPath(tc.id), s.lockPath(tc.id)} {
				assert.Equal(t, sessionsDir, filepath.Dir(p),
					"derived path %q must sit directly in the sessions dir", p)
				rel, err := filepath.Rel(sessionsDir, p)
				require.NoError(t, err)
				assert.False(t, strings.HasPrefix(rel, ".."),
					"derived path %q must not escape the sessions dir (rel %q)", p, rel)
			}
		})
	}
}

func TestStore_CreateAndLoad(t *testing.T) {
	s := testStore(t)

	root := Participant{AgentID: "mal", Persona: "mal"}
	primary := Participant{AgentID: "12345", Persona: "archie", Parent: "mal"}
	require.NoError(t, s.Create("session-1", root, primary, "", ""))

	roster, err := s.Load("session-1")
	require.NoError(t, err)
	assert.Equal(t, "session-1", roster.Session)
	assert.NotEmpty(t, roster.Started)
	assert.Len(t, roster.Participants, 2)
	assert.Equal(t, "mal", roster.Participants[0].AgentID)
	assert.Equal(t, "12345", roster.Participants[1].AgentID)
	assert.Equal(t, "mal", roster.Participants[1].Parent)
}

func TestStore_LoadNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.Load("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_Join(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-join", root, primary, "", ""))

	sub := Participant{
		AgentID:   "sub-1",
		Persona:   "code-reviewer",
		AgentType: "code-reviewer",
		Parent:    "99999",
	}
	require.NoError(t, s.Join("sess-join", sub))

	roster, err := s.Load("sess-join")
	require.NoError(t, err)
	assert.Len(t, roster.Participants, 3)
	assert.Equal(t, "sub-1", roster.Participants[2].AgentID)
	assert.Equal(t, "code-reviewer", roster.Participants[2].Persona)
}

func TestStore_JoinIdempotent(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-idem", root, primary, "", ""))

	sub := Participant{AgentID: "sub-1", Persona: "reviewer", Parent: "99999"}
	require.NoError(t, s.Join("sess-idem", sub))

	// Re-join with updated persona.
	sub2 := Participant{AgentID: "sub-1", Persona: "updated-reviewer", Parent: "99999"}
	require.NoError(t, s.Join("sess-idem", sub2))

	roster, err := s.Load("sess-idem")
	require.NoError(t, err)
	assert.Len(t, roster.Participants, 3)
	assert.Equal(t, "updated-reviewer", roster.FindParticipant("sub-1").Persona)
}

// TestStore_JoinSelf_UpdatesLegacyKeyedParticipant pins the round 2
// finding: a session created before DES-074 keys its primary participant
// on the walk-derived PID (legacyID). A caller that has since resolved a
// DIFFERENT preferred key (CLAUDE_PID) must update that existing record,
// not file a second, duplicate participant for the same physical process.
func TestStore_JoinSelf_UpdatesLegacyKeyedParticipant(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "518779", Persona: "claude", Parent: "user1"}
	require.NoError(t, s.Create("sess-legacy", root, primary, "", ""))

	update := Participant{AgentID: "1710156", Persona: "claude-updated"}
	require.NoError(t, s.JoinSelf("sess-legacy", "1710156", "518779", update))

	roster, err := s.Load("sess-legacy")
	require.NoError(t, err)
	require.Len(t, roster.Participants, 2, "must update the existing legacy-keyed record, not append a duplicate")
	assert.Equal(t, "518779", roster.Participants[1].AgentID, "the on-disk key is left as-is; only the fields update")
	assert.Equal(t, "claude-updated", roster.Participants[1].Persona)
}

// TestStore_JoinSelf_PrefersNewKeyWhenBothAbsent pins the ordinary case: a
// session created after DES-074 (or one with no self participant yet at
// all) has no legacy-keyed record to tolerate, so JoinSelf files the new
// participant under preferredID exactly like Join would.
func TestStore_JoinSelf_PrefersNewKeyWhenBothAbsent(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "user1", Persona: "user1"}
	require.NoError(t, s.Create("sess-fresh", root, primary, "", ""))

	p := Participant{Persona: "claude"}
	require.NoError(t, s.JoinSelf("sess-fresh", "1710156", "518779", p))

	roster, err := s.Load("sess-fresh")
	require.NoError(t, err)
	found := roster.FindParticipant("1710156")
	require.NotNil(t, found)
	assert.Equal(t, "claude", found.Persona)
	assert.Nil(t, roster.FindParticipant("518779"))
}

// TestStore_JoinSelf_PrefersNewKeyWhenBothPresent pins the case DES-074's
// corroboration is designed to make impossible in steady state (a caller
// resolving the SAME live process would never get two different answers
// from FindClaudePID across two calls) but which JoinSelf still resolves
// deterministically if it ever occurs: preferredID wins.
func TestStore_JoinSelf_PrefersNewKeyWhenBothPresent(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "1710156", Persona: "already-new"}
	require.NoError(t, s.Create("sess-both", root, primary, "", ""))
	require.NoError(t, s.Join("sess-both", Participant{AgentID: "518779", Persona: "also-legacy"}))

	update := Participant{AgentID: "1710156", Persona: "updated"}
	require.NoError(t, s.JoinSelf("sess-both", "1710156", "518779", update))

	roster, err := s.Load("sess-both")
	require.NoError(t, err)
	assert.Equal(t, "updated", roster.FindParticipant("1710156").Persona)
	assert.Equal(t, "also-legacy", roster.FindParticipant("518779").Persona, "the unrelated legacy record is untouched")
}

func TestStore_Leave(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-leave", root, primary, "", ""))

	sub := Participant{AgentID: "sub-1", Persona: "reviewer", Parent: "99999"}
	require.NoError(t, s.Join("sess-leave", sub))
	require.NoError(t, s.Leave("sess-leave", "sub-1"))

	roster, err := s.Load("sess-leave")
	require.NoError(t, err)
	assert.Len(t, roster.Participants, 2)
}

func TestStore_LeaveIdempotent(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-leave2", root, primary, "", ""))

	// Leaving a session you were never in is a no-op, not an error.
	err := s.Leave("sess-leave2", "nonexistent")
	require.NoError(t, err)
}

func TestStore_Delete(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-del", root, primary, "", ""))

	require.NoError(t, s.Delete("sess-del"))

	_, err := s.Load("sess-del")
	require.Error(t, err)
}

func TestStore_DeleteNonexistent(t *testing.T) {
	s := testStore(t)
	require.NoError(t, s.Delete("nonexistent"))
}

// TestStore_Delete_ClearsMissionSidecars pins review finding K3
// (full-branch review, m-2026-09-08-004 round 3): Delete is the shared
// low-level primitive Purge and PurgeTombstoned also funnel through
// (via deleteFiles), so clearing mission sidecars here closes the gap
// for every deletion path, not only the clean HandleSessionEnd one.
func TestStore_Delete_ClearsMissionSidecars(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-del-sidecars", root, primary, "", ""))

	require.NoError(t, mission.WriteActiveMission(s.root, "sess-del-sidecars", "m-2026-09-08-720"))
	require.NoError(t, mission.WriteDispatchPending(s.root, "sess-del-sidecars", "m-2026-09-08-721", "bwk"))
	require.NoError(t, mission.WriteDelegationBinding(s.root, "sess-del-sidecars", mission.DelegationBinding{
		MissionID:    "m-2026-09-08-721",
		DelegationID: "d-2026-09-08-001",
	}))

	require.NoError(t, s.Delete("sess-del-sidecars"))

	claimed, err := mission.ReadActiveMission(s.root, "sess-del-sidecars")
	require.NoError(t, err)
	assert.Empty(t, claimed)

	pending, _, err := mission.ReadDispatchPending(s.root, "sess-del-sidecars")
	require.NoError(t, err)
	assert.Empty(t, pending)

	_, err = os.Stat(mission.DelegationBindingPath(s.root, "sess-del-sidecars"))
	assert.True(t, os.IsNotExist(err))
}

func TestStore_List(t *testing.T) {
	s := testStore(t)

	// Empty list.
	ids, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, ids)

	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-a", root, primary, "", ""))
	require.NoError(t, s.Create("sess-b", root, primary, "", ""))

	ids, err = s.List()
	require.NoError(t, err)
	assert.Len(t, ids, 2)
	assert.Contains(t, ids, "sess-a")
	assert.Contains(t, ids, "sess-b")
}

func TestStore_ListNoDirectory(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nonexistent"))
	ids, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestStore_Purge(t *testing.T) {
	s := testStore(t)

	// Create a roster with a dead PID as primary agent.
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "9999999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-stale", root, primary, "", ""))

	purged, _, err := s.Purge()
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-stale")

	ids, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// TestStore_Delete_SidecarClearFailureKeepsRoster pins review finding
// J3 (full-branch review, m-2026-09-08-004 round 3), correcting K3: a
// sidecar-clear failure used to be advisory only -- reported to stderr,
// with the roster removed regardless. That reopened the exact gap K3
// closed: once the roster is gone the session is absent from List(), so
// Purge/PurgeTombstoned never revisit it, permanently orphaning the
// sidecar with no GC path. deleteFiles must now clear sidecars BEFORE
// removing the roster and propagate a clear failure instead of
// swallowing it, so the roster survives as the retry token a later
// purge needs.
func TestStore_Delete_SidecarClearFailureKeepsRoster(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	sessionID := "sess-sidecar-clear-fails"
	require.NoError(t, s.Create(sessionID, root, primary, "", ""))
	require.NoError(t, mission.WriteActiveMission(s.root, sessionID, "m-2026-09-08-724"))

	// Lock the mission sidecar's own directory (a sibling of the roster
	// file, not an ancestor of it) so removing the sidecar fails while
	// removing the roster itself would otherwise still succeed.
	sidecarDir := filepath.Dir(mission.ActiveMissionPath(s.root, sessionID))
	require.NoError(t, os.Chmod(sidecarDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(sidecarDir, 0o700) })

	err := s.Delete(sessionID)
	require.Error(t, err, "a sidecar-clear failure must surface, not be swallowed")

	_, loadErr := s.Load(sessionID)
	require.NoError(t, loadErr, "the roster must survive a sidecar-clear failure -- it is the retry token a later purge needs")
}

// TestStore_Delete_HoldsDispatchPendingLockThroughRosterRemoval pins the
// leader's PR #509 tail-round finding: before this fix, deleteFiles
// acquired mission's per-session dispatch-pending lock only around the
// dispatch-pending CLEAR substep (via clearMissionSidecars's own call to
// mission.WithDispatchPendingLock), releasing it before removing the
// roster file -- leaving a gap in which a resumed session's own claim or
// dispatch-pending WRITE (which also takes that lock) could land,
// orphaning the fresh sidecar the instant the roster disappeared
// (List()/Purge() only ever discover sessions via their roster file).
//
// deleteFilesLockStillHeld fires from inside deleteFiles' own critical
// section, after clearing every sidecar but BEFORE removing the roster,
// while the lock deleteFiles acquired at the top is still held. This
// test overrides the hook to attempt a sibling AcquireDispatchPendingLock
// for the SAME session from a separate goroutine and asserts it blocks
// (proving the lock spans the whole clear-through-roster-removal
// window, not only the clear substep) and only succeeds once Delete has
// returned.
func TestStore_Delete_HoldsDispatchPendingLockThroughRosterRemoval(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	sessionID := "sess-lock-spans-removal"
	require.NoError(t, s.Create(sessionID, root, primary, "", ""))

	proceed := make(chan struct{})
	siblingAcquired := make(chan struct{})
	t.Cleanup(func() { deleteFilesLockStillHeld = func() {} })
	deleteFilesLockStillHeld = func() {
		go func() {
			release, err := mission.AcquireDispatchPendingLock(s.root, sessionID)
			if err != nil {
				close(siblingAcquired) // surfaced via the select below as a spurious close
				return
			}
			defer release()
			close(siblingAcquired)
		}()
		// Give the sibling goroutine time to enter Flock and block --
		// mirrors TestAcquireDelegationLock_BlocksUntilRelease's own
		// discipline elsewhere in this codebase.
		time.Sleep(50 * time.Millisecond)
		select {
		case <-siblingAcquired:
			t.Error("sibling acquired the dispatch-pending lock while deleteFiles still holds it, " +
				"before the roster was removed")
		default:
			// Expected: still blocked.
		}
		close(proceed)
	}

	require.NoError(t, s.Delete(sessionID))

	select {
	case <-proceed:
	default:
		t.Fatal("deleteFilesLockStillHeld hook never fired -- test did not exercise the intended path")
	}
	select {
	case <-siblingAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("sibling never acquired the lock after Delete returned")
	}

	_, err := s.Load(sessionID)
	require.Error(t, err, "the roster must be gone once Delete returns")
}

// TestStore_Purge_ClearsMissionSidecars pins review finding K3's actual
// scenario: a session that ended abnormally (SIGKILL, closed terminal,
// crash -- simulated here by a dead PID, exactly what isStale detects)
// left its mission sidecars behind indefinitely before this fix, with
// no GC path. `claude --resume` reusing the same session ID would then
// have its first matching spawn captured by a stale claim or pending
// dispatch from before the death.
func TestStore_Purge_ClearsMissionSidecars(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "9999999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-stale-sidecars", root, primary, "", ""))

	require.NoError(t, mission.WriteActiveMission(s.root, "sess-stale-sidecars", "m-2026-09-08-722"))
	require.NoError(t, mission.WriteDispatchPending(s.root, "sess-stale-sidecars", "m-2026-09-08-723", "bwk"))

	purged, _, err := s.Purge()
	require.NoError(t, err)
	require.Contains(t, purged, "sess-stale-sidecars")

	claimed, err := mission.ReadActiveMission(s.root, "sess-stale-sidecars")
	require.NoError(t, err)
	assert.Empty(t, claimed, "a dead session's claim must not survive to capture a resumed session's spawn")

	pending, _, err := mission.ReadDispatchPending(s.root, "sess-stale-sidecars")
	require.NoError(t, err)
	assert.Empty(t, pending, "a dead session's pending dispatch must not survive to capture a resumed session's spawn")
}

// TestStore_Purge_SidecarClearFailureIsReportedAndRefused pins review
// finding B (full-branch review, m-2026-09-08-004 round 3), a
// regression J3 introduced: `if s.deleteFiles(id) == nil { didPurge =
// true }` treated a sidecar-clear failure (now possible since J3 made
// deleteFiles propagate one) identically to "not stale" -- the session
// was neither purged nor reported anywhere, with nothing reaching
// stderr. That is strictly worse than pre-J3, which at least logged a
// line per failed sidecar clear. Purge must log the failure (matching
// its sibling PurgeTombstoned) and report the session in a refused
// slice, not silently do nothing.
func TestStore_Purge_SidecarClearFailureIsReportedAndRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "9999999", Persona: "agent", Parent: "user1"}
	sessionID := "sess-purge-sidecar-fails"
	require.NoError(t, s.Create(sessionID, root, primary, "", ""))
	require.NoError(t, mission.WriteActiveMission(s.root, sessionID, "m-2026-09-08-725"))

	sidecarDir := filepath.Dir(mission.ActiveMissionPath(s.root, sessionID))
	require.NoError(t, os.Chmod(sidecarDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(sidecarDir, 0o700) })

	oldStderr := os.Stderr
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = pw

	purged, refused, purgeErr := s.Purge()

	require.NoError(t, pw.Close())
	os.Stderr = oldStderr
	stderrBytes, err := io.ReadAll(pr)
	require.NoError(t, err)
	stderrText := string(stderrBytes)

	require.NoError(t, purgeErr)
	assert.NotContains(t, purged, sessionID, "a session whose sidecar clear failed must not be reported as purged")
	assert.Contains(t, refused, sessionID, "a session whose sidecar clear failed must be reported as refused")
	assert.Contains(t, stderrText, sessionID, "the failure must reach stderr, matching PurgeTombstoned's own discipline")

	_, loadErr := s.Load(sessionID)
	require.NoError(t, loadErr, "the roster must survive so a later purge can retry")
}

func TestStore_PurgeKeepsLive(t *testing.T) {
	s := testStore(t)

	// Create a roster with our own PID as primary (definitely alive).
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{
		AgentID: fmt.Sprintf("%d", os.Getpid()),
		Persona: "agent",
		Parent:  "user1",
	}
	require.NoError(t, s.Create("sess-live", root, primary, "", ""))

	purged, _, err := s.Purge()
	require.NoError(t, err)
	assert.Empty(t, purged)

	ids, err := s.List()
	require.NoError(t, err)
	assert.Contains(t, ids, "sess-live")
}

func TestStore_CurrentSession(t *testing.T) {
	s := testStore(t)

	require.NoError(t, s.WriteCurrentSession("12345", "sess-abc"))

	id, err := s.ReadCurrentSession("12345")
	require.NoError(t, err)
	assert.Equal(t, "sess-abc", id)

	require.NoError(t, s.DeleteCurrentSession("12345"))

	_, err = s.ReadCurrentSession("12345")
	require.Error(t, err)
}

// TestStore_ReadCurrentSession_BlankFileIsError pins round 2, R1: a
// zero-byte (or whitespace-only) pointer file must read as an ERROR, not
// a successful empty session id. Before this fix, ("", nil) was
// indistinguishable from "no session" and took DES-074's silent branch
// even under Claude Code, reintroducing the exact wrong-answer-with-
// exit-0 shape this decision exists to close — through a narrower door
// (a crash or a non-atomic writer leaving a truncated file, rather than
// a PID collision).
func TestStore_ReadCurrentSession_BlankFileIsError(t *testing.T) {
	s := testStore(t)
	require.NoError(t, os.MkdirAll(filepath.Join(s.root, "sessions", "current"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(s.root, "sessions", "current", "99999"), []byte(""), 0o600))

	_, err := s.ReadCurrentSession("99999")
	require.Error(t, err, "a blank pointer file must not read as a successful empty session id")
}

// TestStore_WriteCurrentSession_AtomicNoStrayTempFiles pins round 2, R1b:
// WriteCurrentSession must write via temp+rename (matching writeRoster's
// existing pattern for the roster file), not a direct truncate-in-place
// os.WriteFile, which leaves a window where a concurrent
// ReadCurrentSession can observe a zero-byte or partially written file.
// A successful write leaves no stray "current-*.tmp" file behind.
func TestStore_WriteCurrentSession_AtomicNoStrayTempFiles(t *testing.T) {
	s := testStore(t)
	require.NoError(t, s.WriteCurrentSession("12345", "sess-atomic"))

	id, err := s.ReadCurrentSession("12345")
	require.NoError(t, err)
	assert.Equal(t, "sess-atomic", id)

	entries, err := os.ReadDir(filepath.Join(s.root, "sessions", "current"))
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp", "no stray temp file should remain after a successful write")
	}
}

// TestStore_WriteCurrentSession_RejectsBlankArgs pins mission 005 finding
// E: neither argument was validated. A blank sessionID produced a
// permanently blank pointer file at exit 0 -- ReadCurrentSession already
// treats blank as an error, but nothing stopped WriteCurrentSession from
// creating that state. A blank claudePID is worse: filepath.Base("")
// returns ".", so the rename destination would resolve to the
// current-session directory itself rather than a file inside it.
func TestStore_WriteCurrentSession_RejectsBlankArgs(t *testing.T) {
	s := testStore(t)

	err := s.WriteCurrentSession("4242", "")
	require.Error(t, err, "a blank sessionID must be refused, not silently written")

	err = s.WriteCurrentSession("", "some-session")
	require.Error(t, err, "a blank claudePID must be refused, not silently written")

	err = s.WriteCurrentSession("4242", "   ")
	require.Error(t, err, "a whitespace-only sessionID is not a session id either")

	// No pointer file exists for either rejected write.
	_, err = s.ReadCurrentSession("4242")
	require.Error(t, err, "a rejected write must leave no pointer file behind")
}

// Round 2 finding: a TestStore_CurrentSession_DistinctPIDsDoNotCollide
// used to live here, asserting that two literal string keys
// ("11111"/"22222") resolve to two distinct sessions. That is true of
// any key-value store and was true before this fix too — ethos-vqwn was
// never "the store collides on distinct keys," it was "FindClaudePID
// returns the SAME key for different sessions." Removed as redundant
// with TestStore_CurrentSession (a literal-key roundtrip already covers
// the store's own correctness) in favor of
// resolve.TestSessionID_ConcurrentSessionsDoNotCollide, which drives the
// keys through the actual mechanism (process.FindClaudePID under two
// simulated sessions) rather than asserting a property of maps.

func TestStore_PurgeCurrentFiles(t *testing.T) {
	s := testStore(t)

	// Write PID files: one for our own PID (alive), one for a dead PID.
	alivePID := fmt.Sprintf("%d", os.Getpid())
	deadPID := "99999999"

	require.NoError(t, s.WriteCurrentSession(alivePID, "sess-alive"))
	require.NoError(t, s.WriteCurrentSession(deadPID, "sess-dead"))

	purged, err := s.PurgeCurrent()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{deadPID}, purged)

	// Alive PID file still exists.
	sid, err := s.ReadCurrentSession(alivePID)
	require.NoError(t, err)
	assert.Equal(t, "sess-alive", sid)

	// Dead PID file is gone.
	_, err = s.ReadCurrentSession(deadPID)
	require.Error(t, err)
}

func TestStore_PurgeCurrentNoDirectory(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nonexistent"))
	purged, err := s.PurgeCurrent()
	require.NoError(t, err)
	assert.Empty(t, purged)
}

func TestStore_PurgeCleansBothRostersAndPIDFiles(t *testing.T) {
	s := testStore(t)

	deadPID := "99999999"

	// Create a roster with a dead PID as primary.
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: deadPID, Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-both", root, primary, "", ""))

	// Write a PID file for the same dead PID.
	require.NoError(t, s.WriteCurrentSession(deadPID, "sess-both"))

	purged, _, err := s.Purge()
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-both")

	// Roster is gone.
	ids, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, ids)

	// PurgeCurrent is now called separately (CLI orchestrates both).
	pidPurged, pidErr := s.PurgeCurrent()
	require.NoError(t, pidErr)
	assert.ElementsMatch(t, []string{deadPID}, pidPurged)

	// PID file is also gone.
	_, err = s.ReadCurrentSession(deadPID)
	require.Error(t, err)
}

func TestStore_ReadCurrentSessionNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.ReadCurrentSession("nonexistent")
	require.Error(t, err)
}

func TestStore_FilePermissions(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-perms", root, primary, "", ""))

	info, err := os.Stat(filepath.Join(s.sessionsDir(), "sess-perms.yaml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestIsStale pins that staleness keys on AGE, not participant count: a
// solo session (one participant, from Store.Create's root==primary collapse)
// ages out by the TTL rather than being immediately reclaimed. Only a
// genuinely empty roster is stale on sight.
func TestIsStale(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)

	t.Run("fresh solo session is not stale", func(t *testing.T) {
		r := &Roster{Started: now, Participants: []Participant{{AgentID: "jim", Persona: "jim"}}}
		assert.False(t, isStale(r))
	})
	t.Run("solo session past TTL is stale", func(t *testing.T) {
		r := &Roster{Started: old, Participants: []Participant{{AgentID: "jim", Persona: "jim"}}}
		assert.True(t, isStale(r))
	})
	t.Run("empty roster is stale", func(t *testing.T) {
		r := &Roster{Started: now, Participants: nil}
		assert.True(t, isStale(r))
	})
	t.Run("fresh persona primary is not stale", func(t *testing.T) {
		r := &Roster{Started: now, Participants: []Participant{{AgentID: "jim"}, {AgentID: "bwk", Parent: "jim"}}}
		assert.False(t, isStale(r))
	})
	t.Run("persona primary past TTL is stale", func(t *testing.T) {
		r := &Roster{Started: old, Participants: []Participant{{AgentID: "jim"}, {AgentID: "bwk", Parent: "jim"}}}
		assert.True(t, isStale(r))
	})
	t.Run("live numeric primary is not stale even past TTL", func(t *testing.T) {
		pid := strconv.Itoa(os.Getpid())
		r := &Roster{Started: old, Participants: []Participant{{AgentID: "jim"}, {AgentID: pid, Parent: "jim"}}}
		assert.False(t, isStale(r), "a live PID wins over the age fallback")
	})
}

func TestRoster_FindParticipant(t *testing.T) {
	r := &Roster{
		Participants: []Participant{
			{AgentID: "a1", Persona: "alice"},
			{AgentID: "b2", Persona: "bob"},
		},
	}
	assert.NotNil(t, r.FindParticipant("a1"))
	assert.Equal(t, "alice", r.FindParticipant("a1").Persona)
	assert.Nil(t, r.FindParticipant("nonexistent"))
}

func TestRoster_RemoveParticipant(t *testing.T) {
	r := &Roster{
		Participants: []Participant{
			{AgentID: "a1", Persona: "alice"},
			{AgentID: "b2", Persona: "bob"},
		},
	}
	assert.True(t, r.RemoveParticipant("a1"))
	assert.Len(t, r.Participants, 1)
	assert.False(t, r.RemoveParticipant("a1"))
}

func TestStore_JoinWithExt(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-ext", root, primary, "", ""))

	sub := Participant{
		AgentID: "sub-1",
		Persona: "reviewer",
		Parent:  "99999",
		Ext:     map[string]any{"biff": map[string]any{"tty": "s004"}},
	}
	require.NoError(t, s.Join("sess-ext", sub))

	roster, err := s.Load("sess-ext")
	require.NoError(t, err)
	p := roster.FindParticipant("sub-1")
	require.NotNil(t, p)
	assert.NotNil(t, p.Ext)
	biff, ok := p.Ext["biff"]
	require.True(t, ok)
	biffMap, ok := biff.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "s004", biffMap["tty"])
}

func TestStore_CreateWithRepoAndHost(t *testing.T) {
	s := testStore(t)

	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-repo", root, primary, "punt-labs/ethos", "m2-mb-air"))

	roster, err := s.Load("sess-repo")
	require.NoError(t, err)
	assert.Equal(t, "punt-labs/ethos", roster.Repo)
	assert.Equal(t, "m2-mb-air", roster.Host)
	assert.NotEmpty(t, roster.Participants[0].Joined)
	assert.NotEmpty(t, roster.Participants[1].Joined)
}

func TestStore_CreateSetsJoinedOnParticipants(t *testing.T) {
	s := testStore(t)

	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-joined", root, primary, "", ""))

	roster, err := s.Load("sess-joined")
	require.NoError(t, err)
	assert.NotEmpty(t, roster.Participants[0].Joined)
	assert.NotEmpty(t, roster.Participants[1].Joined)
}

func TestStore_JoinSetsJoined(t *testing.T) {
	s := testStore(t)

	root := Participant{AgentID: "user1", Persona: "user1"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-join-ts", root, primary, "", ""))

	sub := Participant{AgentID: "sub-1", Persona: "reviewer", Parent: "99999"}
	require.NoError(t, s.Join("sess-join-ts", sub))

	roster, err := s.Load("sess-join-ts")
	require.NoError(t, err)
	p := roster.FindParticipant("sub-1")
	require.NotNil(t, p)
	assert.NotEmpty(t, p.Joined)
}

func TestStore_BackwardCompatibility_NoRepoHostJoined(t *testing.T) {
	s := testStore(t)

	// Write a roster in the old format (no repo, host, or joined fields).
	require.NoError(t, os.MkdirAll(s.sessionsDir(), 0o700))
	oldYAML := `session: sess-old
started: "2025-01-01T00:00:00Z"
participants:
  - agent_id: user1
    persona: user1
  - agent_id: "12345"
    persona: agent
    parent: user1
`
	require.NoError(t, os.WriteFile(
		filepath.Join(s.sessionsDir(), "sess-old.yaml"),
		[]byte(oldYAML), 0o600,
	))

	roster, err := s.Load("sess-old")
	require.NoError(t, err)
	assert.Equal(t, "sess-old", roster.Session)
	assert.Equal(t, "", roster.Repo)
	assert.Equal(t, "", roster.Host)
	assert.Len(t, roster.Participants, 2)
	assert.Equal(t, "", roster.Participants[0].Joined)
	assert.Equal(t, "", roster.Participants[1].Joined)
}

func TestStore_CreatePreservesExplicitJoined(t *testing.T) {
	s := testStore(t)

	// If Joined is already set, Create should not overwrite it.
	root := Participant{AgentID: "user1", Persona: "user1", Joined: "2025-01-01T00:00:00Z"}
	primary := Participant{AgentID: "99999", Persona: "agent", Parent: "user1"}
	require.NoError(t, s.Create("sess-preserve", root, primary, "", ""))

	roster, err := s.Load("sess-preserve")
	require.NoError(t, err)
	assert.Equal(t, "2025-01-01T00:00:00Z", roster.Participants[0].Joined)
	assert.NotEmpty(t, roster.Participants[1].Joined)
	assert.NotEqual(t, "2025-01-01T00:00:00Z", roster.Participants[1].Joined)
}

// TestStore_CreateInCheckoutRecordsCheckout pins the DES-058 binding the
// vacuum cross-check reads: the roster records the checkout whose live audit
// zone the session writes to, distinct from the git-remote identity. Create
// records none, which reads as "writer unknown".
func TestStore_CreateInCheckoutRecordsCheckout(t *testing.T) {
	s := testStore(t)
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "99999", Parent: "user1"}

	require.NoError(t, s.CreateInCheckout("sess-co", root, primary,
		"punt-labs/ethos", "/checkouts/ethos", "host1"))
	roster, err := s.Load("sess-co")
	require.NoError(t, err)
	assert.Equal(t, "punt-labs/ethos", roster.Repo)
	assert.Equal(t, "/checkouts/ethos", roster.Checkout)

	require.NoError(t, s.Create("sess-noco", root, primary, "punt-labs/ethos", "host1"))
	roster, err = s.Load("sess-noco")
	require.NoError(t, err)
	assert.Equal(t, "", roster.Checkout)
}

// TestPurgeTombstoned_SealedMissionInNonWriterCheckoutDoesNotFlagTombstone is
// Fix 4 of ethos-q6e2. The mission-namespace guard used to read an absent live
// log as loss, so a purge run anywhere but the writing checkout minted a
// PERMANENT flagged tombstone — a loss record that was never earned and warns
// at every commit until acked.
//
// The suppression is narrower than "a chunk exists", and this test's setup is
// what makes it apply: the roster records NO checkout, so the purge falls back
// to whichever one it runs in, and that checkout holds no live mission log of
// this session's. Nothing ever said the session's files belong here, so their
// absence says nothing. A checkout the roster DID record still flags —
// TestPurgeTombstoned_DeletedLiveLogInWriterCheckoutFlagsTombstone covers it.
//
// The session's own live audit file is written and fully sealed here, so the
// session-namespace probe cannot supply the flag: only the mission branch is
// under test.
func TestPurgeTombstoned_SealedMissionInNonWriterCheckoutDoesNotFlagTombstone(t *testing.T) {
	s := testStore(t)
	repoRoot := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	// A roster predating the checkout field: the probe falls back to repoRoot.
	require.NoError(t, s.Create("sess-sealed", root, primary, testRepoID, ""))
	writeSealedLive(t, repoRoot, "sess-sealed")
	// A mission whose chunk carries this session, with no live log in this
	// checkout — the steady state of every checkout that did not write them.
	sealMissionChunkFor(t, repoRoot, "m-2026-07-21-009", "sess-sealed")

	purged, refused, err := s.PurgeTombstoned(repoRoot, testRepoID, false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-sealed")
	assert.Empty(t, refused)

	_, err = audit.ReadTombstone(filepath.Join(s.sessionsDir(), "sess-sealed.purged"))
	assert.Error(t, err, "a session whose mission lines are all sealed must leave no loss tombstone")
}

// writeSealedLive writes a live audit line for sessionID under repoRoot and
// seals it into a tracked chunk, so the session-namespace probe reports the
// file present with nothing unsealed.
func writeSealedLive(t *testing.T, repoRoot, sessionID string) {
	t.Helper()
	live := audit.LiveAuditPath(repoRoot, sessionID)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	body := `{"ts":"` + audit.FormatLineTS(100) + `","session":"` + sessionID + `","tool":"Read"}` + "\n"
	require.NoError(t, os.WriteFile(live, []byte(body), 0o600))

	dir := filepath.Join(audit.SealedSessionsBase(repoRoot), "2026-07-21-"+sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, audit.SessionChunkFile(100, 100)), []byte(body), 0o600))
}

// TestPurgeTombstoned_OutOfRepoUsesRecordedCheckout covers a purge run with no
// checkout in scope for a session whose roster records one. The recorded
// checkout is then the only checkout that can be named, and it holds both the
// live zone and its own tracked chunks. Reading the tracked side from an empty
// repoRoot instead would resolve ".punt-labs/..." against the working
// directory — nothing, or some unrelated repo's chunks — and flag a loss that
// did not happen.
func TestPurgeTombstoned_OutOfRepoUsesRecordedCheckout(t *testing.T) {
	s := testStore(t)
	checkout := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	// Repo "" is a checkout with no parseable origin — the only kind that
	// reaches the probes when the purge runs outside any repo.
	require.NoError(t, s.CreateInCheckout("sess-oor", root, primary, "", checkout, ""))
	writeSealedLive(t, checkout, "sess-oor")
	sealMissionChunkFor(t, checkout, "m-2026-07-21-009", "sess-oor")
	// The recorded checkout really holds the mission's live log, so there is
	// nothing missing there to report.
	writeLiveMissionLogFor(t, checkout, "m-2026-07-21-009", "sess-oor")

	purged, refused, err := s.PurgeTombstoned("", "", false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-oor")
	assert.Empty(t, refused)

	_, err = audit.ReadTombstone(filepath.Join(s.sessionsDir(), "sess-oor.purged"))
	assert.Error(t, err, "state provable at the recorded checkout must not flag a loss")
}

// TestPurgeTombstoned_DeletedLiveLogInWriterCheckoutFlagsTombstone is the other
// side of the suppression, on the purge path. Here the checkout DID write
// mission live logs — a sibling mission's file stands — so this mission's
// missing file is a deletion, and the tail written after the chunk's watermark
// went with it. Purge must record that loss.
//
// Without the WriterZone distinction a sealed chunk suppressed unconditionally
// and the whole deletion class purged silently (rsc, PR #413 M1).
func TestPurgeTombstoned_DeletedLiveLogInWriterCheckoutFlagsTombstone(t *testing.T) {
	s := testStore(t)
	repoRoot := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	require.NoError(t, s.CreateInCheckout("sess-del", root, primary, testRepoID, repoRoot, ""))
	writeSealedLive(t, repoRoot, "sess-del")
	sealMissionChunkFor(t, repoRoot, "m-2026-07-21-009", "sess-del")
	// The live-missions zone exists here — this checkout wrote mission logs —
	// so m-2026-07-21-009's absent file is a deletion, not another checkout's
	// ordinary absence.
	writeLiveMissionLogFor(t, repoRoot, "m-2026-07-21-010", "sess-del")

	purged, refused, err := s.PurgeTombstoned(repoRoot, testRepoID, false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-del")
	assert.Empty(t, refused)

	tb, err := audit.ReadTombstone(filepath.Join(s.sessionsDir(), "sess-del.purged"))
	require.NoError(t, err, "a deletion in the writer's own checkout must leave a tombstone")
	assert.True(t, tb.LiveFileGone, "the deleted mission live log must set the flag")
}

// writeLiveMissionLogFor writes one sealed-and-clean live mission log, enough
// to establish that the checkout has a live-missions zone.
func writeLiveMissionLogFor(t *testing.T, repoRoot, missionID, sessionID string) {
	t.Helper()
	path := audit.LiveMissionLogPath(repoRoot, missionID, sessionID)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	line := `{"ts":"` + audit.FormatLineTS(100) + `","event":"create"}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(line), 0o600))
	// Seal it, so this sibling contributes no unsealed lines of its own.
	dir := audit.SealedMissionDir(repoRoot, missionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, audit.MissionChunkFile(sessionID, 100, 100)), []byte(line), 0o600))
}

// TestPurgeTombstoned_RecordedWriterHoldsNoLiveLogFlagsTombstone pins the
// WIRING of the purge recorded-writer path, which no other test reaches.
//
// TestPurgeTombstoned_DeletedLiveLogInWriterCheckoutFlagsTombstone plants a
// sibling live log, so WriterZone carries its verdict and the recorded binding
// is never load-bearing. Here the recorded checkout exists and holds no live
// mission log at all — the whole live-missions zone removed — so only the
// binding separates it from a fallback probe. Downgrading RecordedWriter to
// AssumedWriter on this path restores the whole-zone-deleted hole, and this is
// the test that catches it.
func TestPurgeTombstoned_RecordedWriterHoldsNoLiveLogFlagsTombstone(t *testing.T) {
	s := testStore(t)
	repoRoot := t.TempDir()
	root := Participant{AgentID: "user1"}
	primary := Participant{AgentID: "9999999", Parent: "user1"} // dead PID → stale
	require.NoError(t, s.CreateInCheckout("sess-nozone", root, primary, testRepoID, repoRoot, ""))
	// The session's own audit file is present and sealed, so the
	// session-namespace probe reports clean and only the mission branch can
	// supply the flag.
	writeSealedLive(t, repoRoot, "sess-nozone")
	// A sealed mission, and NO live-missions zone anywhere in this checkout.
	sealMissionChunkFor(t, repoRoot, "m-2026-07-21-009", "sess-nozone")

	purged, refused, err := s.PurgeTombstoned(repoRoot, testRepoID, false)
	require.NoError(t, err)
	assert.Contains(t, purged, "sess-nozone")
	assert.Empty(t, refused)

	tb, err := audit.ReadTombstone(filepath.Join(s.sessionsDir(), "sess-nozone.purged"))
	require.NoError(t, err,
		"a recorded writer that cannot produce the live log must leave a loss tombstone")
	assert.True(t, tb.LiveFileGone)
}
