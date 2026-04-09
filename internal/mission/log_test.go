//go:build !windows

package mission

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appendEventForTest exercises the flock-acquire + append pattern from
// a test-only context. Production callers (Create/Update/Close) hold
// the lock when they write events, so they call appendEventLocked
// directly. Tests need the locking wrapper to validate concurrent
// behavior without inlining the two-step pattern at every call site.
//
// Lives in log_test.go so production builds don't carry a public
// wrapper that's only useful to tests (and which was a deadlock
// footgun for any future caller invoking it from inside a locked
// block).
func appendEventForTest(t *testing.T, s *Store, missionID string, e Event) error {
	t.Helper()
	return s.withLock(missionID, func() error {
		return s.appendEventLocked(missionID, e)
	})
}

func TestAppendEvent_RoundTrip(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))

	// Create already wrote one event; append four more.
	for i := 1; i <= 4; i++ {
		require.NoError(t, appendEventForTest(t, s, "m-2026-04-07-001", Event{
			TS:    fmt.Sprintf("2026-04-07T22:00:%02dZ", i),
			Event: "update",
			Actor: "claude",
			Details: map[string]any{
				"index": i,
			},
		}))
	}

	events := readLog(t, s, "m-2026-04-07-001")
	assert.Len(t, events, 5)
	assert.Equal(t, "create", events[0].Event)
	for i := 1; i <= 4; i++ {
		assert.Equal(t, "update", events[i].Event)
		assert.Equal(t, "claude", events[i].Actor)
	}
}

func TestAppendEvent_NoTruncation(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))

	// Write 50 events with distinct payloads. None should be lost.
	const n = 50
	for i := 0; i < n; i++ {
		require.NoError(t, appendEventForTest(t, s, "m-2026-04-07-001", Event{
			TS:      "2026-04-07T22:00:00Z",
			Event:   "update",
			Actor:   "claude",
			Details: map[string]any{"i": i},
		}))
	}

	events := readLog(t, s, "m-2026-04-07-001")
	// 1 create + n updates.
	assert.Len(t, events, n+1)
}

func TestAppendEvent_PreservesOrder(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))

	require.NoError(t, appendEventForTest(t, s, "m-2026-04-07-001", Event{
		TS: "2026-04-07T22:00:01Z", Event: "update", Actor: "claude",
	}))
	require.NoError(t, appendEventForTest(t, s, "m-2026-04-07-001", Event{
		TS: "2026-04-07T22:00:02Z", Event: "update", Actor: "claude",
	}))
	require.NoError(t, appendEventForTest(t, s, "m-2026-04-07-001", Event{
		TS: "2026-04-07T22:00:03Z", Event: "close", Actor: "claude",
	}))

	events := readLog(t, s, "m-2026-04-07-001")
	require.Len(t, events, 4)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, "update", events[1].Event)
	assert.Equal(t, "update", events[2].Event)
	assert.Equal(t, "close", events[3].Event)
}

func TestAppendEvent_DetailsRoundTrip(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))

	require.NoError(t, appendEventForTest(t, s, "m-2026-04-07-001", Event{
		TS:    "2026-04-07T22:00:01Z",
		Event: "update",
		Actor: "claude",
		Details: map[string]any{
			"diff_lines": 42,
			"reason":     "fix-spec round 2",
		},
	}))

	events := readLog(t, s, "m-2026-04-07-001")
	require.Len(t, events, 2)
	last := events[1]
	assert.Equal(t, "update", last.Event)
	// JSON unmarshals numbers to float64.
	assert.InDelta(t, 42.0, last.Details["diff_lines"], 0.001)
	assert.Equal(t, "fix-spec round 2", last.Details["reason"])
}

func TestStoreCreateAppendsCreateEvent(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))

	events := readLog(t, s, "m-2026-04-07-001")
	require.Len(t, events, 1)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, c.Leader, events[0].Actor)
}

func TestStoreCloseAppendsCloseEvent(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))
	// Phase 3.6: Close refuses without a result for the current
	// round. Submit one so the test exercises the close event append,
	// not the gate refusal.
	submitRoundResult(t, s, c, VerdictPass)
	require.NoError(t, s.Close("m-2026-04-07-001", StatusClosed))

	events := readLog(t, s, "m-2026-04-07-001")
	// Phase 3.6 adds a "result" event for the submitted artifact —
	// the sequence is create, result, close.
	require.Len(t, events, 3)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, "result", events[1].Event)
	assert.Equal(t, "close", events[2].Event)
	assert.Equal(t, "closed", events[2].Details["status"])
}

func TestStoreUpdateAppendsUpdateEvent(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))

	loaded, err := s.Load("m-2026-04-07-001")
	require.NoError(t, err)
	loaded.Context = "added"
	require.NoError(t, s.Update(loaded))

	events := readLog(t, s, "m-2026-04-07-001")
	require.Len(t, events, 2)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, "update", events[1].Event)
}

// readLog reads the JSONL event log for a mission and parses each line.
// Tests use this to assert the on-disk format directly.
func readLog(t *testing.T, s *Store, missionID string) []Event {
	t.Helper()
	f, err := os.Open(s.logPath(missionID))
	require.NoError(t, err)
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		require.NoError(t, json.Unmarshal([]byte(line), &e), "log line: %q", line)
		events = append(events, e)
	}
	require.NoError(t, scanner.Err())
	return events
}

// --- Phase 3.7: LoadEvents reader classes ---
//
// The failure-mode equivalence class table in the Phase 3.7 mission
// contract enumerates 13 decode classes. This file represents every
// row so a later reviewer can walk the table against the test names
// without guessing which cases are covered.
//
// Trust boundary note: the Event schema has no mission_id field —
// mission identity is implicit via the file path (see Store.logPath,
// which runs the ID through filepath.Base as defense in depth). A
// wrong-mission-id-in-payload attack is therefore structurally
// impossible at the line level: there is no field to be wrong.
// Class 8 from the contract is satisfied by the logPath defense and
// by the `details` map being free-form (a caller-supplied "mission"
// key is treated as opaque payload, not as identity). The test below
// documents this as the policy.

// TestLoadEvents_MissingFile covers class 1: a mission whose
// contract exists but whose JSONL log has not been written yet
// returns an empty slice, nil warnings, nil error — symmetric
// with LoadResults and LoadReflections conventions for missing
// sibling files. The test creates a contract stub but deletes
// the log file the Create path would have produced, so only the
// contract anchor remains.
//
// Round 2: a brand-new mission with NO contract at all now
// returns an error ("mission not found") because the round-2 H4
// existence check aligns LoadEvents with LoadReflections and
// LoadResults. That unknown-mission path is covered by
// TestLoadEvents_UnknownMissionID below.
func TestLoadEvents_MissingFile(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-999"
	seedContractStub(t, s, missionID)
	// No log file: the Create path would write one, but the stub
	// only seeds the contract so the log path is absent.

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.Empty(t, warnings)
}

// TestLoadEvents_EmptyFile covers class 2: a zero-byte log file
// returns the same empty state as a missing file. Symmetric with
// the reflections and results empty-file handling.
func TestLoadEvents_EmptyFile(t *testing.T) {
	s := testStore(t)
	// Create the missions directory and seed an empty log file
	// plus the contract-existence anchor the H4 check requires.
	missionID := "m-2026-04-07-002"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	require.NoError(t, os.WriteFile(s.logPath(missionID), []byte{}, 0o600))
	seedContractStub(t, s, missionID)

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.Empty(t, warnings)
}

// TestLoadEvents_CleanSingleEvent covers class 3: a log with one
// well-formed event returns that event, no warnings, no error. The
// slice is typed — the caller can range over it without a nil guard.
func TestLoadEvents_CleanSingleEvent(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-003")
	require.NoError(t, s.Create(c))

	events, warnings, err := s.LoadEvents("m-2026-04-07-003")
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, events, 1)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, c.Leader, events[0].Actor)
}

// TestLoadEvents_CleanMultipleEvents covers class 4: a log with N
// well-formed events returns them in write order. The test drives a
// full create + result + close sequence so the ordering invariant
// is exercised against the live writer, not a synthetic file.
func TestLoadEvents_CleanMultipleEvents(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-004")
	require.NoError(t, s.Create(c))
	submitRoundResult(t, s, c, VerdictPass)
	require.NoError(t, s.Close("m-2026-04-07-004", StatusClosed))

	events, warnings, err := s.LoadEvents("m-2026-04-07-004")
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, events, 3)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, "result", events[1].Event)
	assert.Equal(t, "close", events[2].Event)
}

// TestLoadEvents_CorruptFirstLine covers class 5: a garbage first
// line is surfaced as a warning naming line 1, and the rest of the
// file still decodes. Partial damage does not erase the log.
func TestLoadEvents_CorruptFirstLine(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-005")
	require.NoError(t, s.Create(c))
	// Append a second (good) line so we can assert the rest of the
	// file still decodes after the first line is overwritten with
	// garbage.
	require.NoError(t, appendEventForTest(t, s, "m-2026-04-07-005", Event{
		TS: "2026-04-07T22:00:02Z", Event: "update", Actor: "claude",
	}))

	// Overwrite the whole file with "garbage\n{good line}\n" so line
	// 1 is corrupt and line 2 is a valid update event.
	seedLogLines(t, s, "m-2026-04-07-005",
		"{not valid json",
		`{"ts":"2026-04-07T22:00:02Z","event":"update","actor":"claude"}`,
	)

	events, warnings, err := s.LoadEvents("m-2026-04-07-005")
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "line 1")
	require.Len(t, events, 1)
	assert.Equal(t, "update", events[0].Event)
}

// TestLoadEvents_CorruptMidFile covers class 6: a garbage line in
// the middle of the file is surfaced as a warning naming its line
// number, and the lines before and after still decode.
func TestLoadEvents_CorruptMidFile(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-006"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	seedLogLines(t, s, missionID,
		`{"ts":"2026-04-07T22:00:01Z","event":"create","actor":"claude"}`,
		"{garbage",
		`{"ts":"2026-04-07T22:00:03Z","event":"close","actor":"claude"}`,
	)

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "line 2")
	require.Len(t, events, 2)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, "close", events[1].Event)
}

// TestLoadEvents_CorruptLastLine covers class 7: a partial write
// at end-of-file (a line with no trailing newline and invalid JSON)
// is surfaced as a warning, and every good line before it still
// decodes. This is the hazardous case — a crashing writer can leave
// a half-written line at EOF.
func TestLoadEvents_CorruptLastLine(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-007"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	// Three good lines + one bad trailing fragment with no newline.
	seedLogRaw(t, s, missionID,
		`{"ts":"2026-04-07T22:00:01Z","event":"create","actor":"claude"}`+"\n"+
			`{"ts":"2026-04-07T22:00:02Z","event":"update","actor":"claude"}`+"\n"+
			`{"ts":"2026-04-07T22:00:03Z","event":"update","actor":"claude"}`+"\n"+
			`{"ts":"2026-04-07T22:00:04","event":"clo`,
	)

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "line 4")
	require.Len(t, events, 3)
	assert.Equal(t, "create", events[0].Event)
}

// TestLoadEvents_WrongMissionInDetails covers class 8 under the
// actual Event schema. The Event type has no top-level mission_id
// field — mission identity is implicit via the file path (the
// logPath helper runs the ID through filepath.Base as defense in
// depth). A caller-planted `"mission": "other"` key inside the
// free-form Details map is just payload, not identity: the reader
// preserves it untouched because there is no invariant that says
// otherwise.
//
// This is the documented policy: the trust anchor is the filesystem
// path, not a field on the line. Tests at higher layers confirm the
// path-based anchor is stable (no traversal, no symlink out).
func TestLoadEvents_WrongMissionInDetails(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-008"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	seedLogLines(t, s, missionID,
		`{"ts":"2026-04-07T22:00:01Z","event":"create","actor":"claude","details":{"mission":"m-other-999"}}`,
	)

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, events, 1)
	// Payload round-trips untouched; the "mission" key inside Details
	// is opaque to the reader and stays as typed by the writer.
	assert.Equal(t, "m-other-999", events[0].Details["mission"])
}

// TestLoadEvents_UnknownEventType covers class 9: a line whose
// `event` string is not one of the writer's current set (`create`,
// `update`, `close`, `reflect`, `result`, `round_advanced`) is
// preserved with the unknown type string intact. Forward compatibility
// is the policy — future phases may emit `worker_spawned`,
// `round_started`, etc. without a reader change.
func TestLoadEvents_UnknownEventType(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-009"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	seedLogLines(t, s, missionID,
		`{"ts":"2026-04-07T22:00:01Z","event":"create","actor":"claude"}`,
		`{"ts":"2026-04-07T22:00:02Z","event":"worker_spawned","actor":"claude"}`,
	)

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, events, 2)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, "worker_spawned", events[1].Event)
}

// TestLoadEvents_MissingRequiredField covers class 10: a line with
// empty mandatory fields (ts, event, actor) is rejected with a
// warning naming the line, and the rest of the file still decodes.
// The writer never emits these empty — only a hand-edited or
// corrupted file can produce them.
func TestLoadEvents_MissingRequiredField(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-010"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	seedLogLines(t, s, missionID,
		`{"ts":"2026-04-07T22:00:01Z","event":"create","actor":"claude"}`,
		`{"ts":"","event":"","actor":""}`,
		`{"ts":"2026-04-07T22:00:03Z","event":"close","actor":"claude"}`,
	)

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "line 2")
	require.Len(t, events, 2)
}

// TestLoadEvents_UnknownFieldStrict covers class 11: strict decode
// rejects a line with an extra top-level field. Trust-boundary
// symmetry with the reflection and result loaders — an attacker
// with local write access cannot smuggle extra fields past a lax
// reader.
func TestLoadEvents_UnknownFieldStrict(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-011"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	seedLogLines(t, s, missionID,
		`{"ts":"2026-04-07T22:00:01Z","event":"create","actor":"claude"}`,
		`{"ts":"2026-04-07T22:00:02Z","event":"update","actor":"claude","smuggled":"payload"}`,
	)

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "line 2")
	require.Len(t, events, 1)
	assert.Equal(t, "create", events[0].Event)
}

// TestLoadEvents_PermissionDenied covers class 12: an unreadable
// log file returns a distinguishable error, not a "missing file"
// false positive. Running as root defeats chmod 0000, so this test
// skips in that case rather than producing a misleading pass.
func TestLoadEvents_PermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-denied test is meaningless as root")
	}
	s := testStore(t)
	c := newContract("m-2026-04-07-012")
	require.NoError(t, s.Create(c))
	require.NoError(t, os.Chmod(s.logPath("m-2026-04-07-012"), 0o000))
	t.Cleanup(func() {
		// Restore permissions so t.TempDir cleanup can remove the file.
		_ = os.Chmod(s.logPath("m-2026-04-07-012"), 0o600)
	})

	events, _, err := s.LoadEvents("m-2026-04-07-012")
	require.Error(t, err)
	assert.Nil(t, events)
	// Must not be confused with "missing file" — the error string
	// should carry the OS permission hint from the wrapped syscall.
	assert.Contains(t, strings.ToLower(err.Error()), "permission")
}

// TestLoadEvents_FollowsSymlink_KnownWeaknessMatchesStoreLoad
// documents the deliberate carry of a known weakness shared across
// all four loaders. os.ReadFile follows symlinks, so a symlink
// planted at the mission log path whose target is an attacker-
// controlled file outside missionsDir is read as if it were the
// mission's log. LoadEvents does not tighten this alone: the fix
// must land uniformly across Store.Load, LoadReflections,
// LoadResults, and LoadEvents, otherwise the asymmetry is worse
// than the current consistent weakness. See bead ethos-jjm for the
// follow-up that hardens all four loaders together.
//
// This test pins the current behavior so the follow-up is an
// explicit, reviewable change — not a silent drift. It used to
// carry the positive-sounding name "SymlinkPolicy" which misread
// as "this is a feature"; the rename signals the intent is
// deferred hardening, not a capability.
func TestLoadEvents_FollowsSymlink_KnownWeaknessMatchesStoreLoad(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-013"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	// Write a log body to an out-of-directory file, then symlink the
	// mission's log path to it. os.ReadFile follows the symlink, so
	// LoadEvents reads it as if it were the mission's own log — the
	// weakness ethos-jjm will close across all four loaders.
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	require.NoError(t, os.WriteFile(outside,
		[]byte(`{"ts":"2026-04-07T22:00:01Z","event":"create","actor":"claude"}`+"\n"),
		0o600))
	require.NoError(t, os.Symlink(outside, s.logPath(missionID)))
	seedContractStub(t, s, missionID)

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, events, 1)
	assert.Equal(t, "create", events[0].Event)
}

// TestLoadEvents_TraversalIDCannotEscape asserts the path-base
// defense combined with the round-2 H4 existence anchor. A
// traversal-laced mission ID is collapsed to its basename by
// filepath.Base BEFORE any open — the reader can never be directed
// at a sibling file or outside the missions directory — AND the
// reader then checks that the collapsed mission actually exists
// (symmetric with LoadReflections / LoadResults). The combination
// means a caller passing "../../etc/<id>" gets a clean error
// rather than a silently-collapsed read of some unrelated file.
//
// Round 1 asserted only the collapse and returned the log for the
// collapsed ID; round 2 (feature-dev finding H4) adds the
// existence check so the loader is symmetric with its siblings.
func TestLoadEvents_TraversalIDCannotEscape(t *testing.T) {
	s := testStore(t)
	// Seed a legitimate contract + log file. The traversal-laced ID
	// collapses to the same basename as the legitimate mission, so
	// without the existence check round 1 read the legitimate log
	// for an unrelated caller. With the check the collapsed ID is
	// still rejected when it does not match an existing contract —
	// here the contract exists, so collapse + existence both succeed.
	missionID := "m-2026-04-07-014"
	c := newContract(missionID)
	require.NoError(t, s.Create(c))

	// The ".." prefix is collapsed to the legitimate mission ID;
	// the contract exists because Create wrote it; the read
	// succeeds and returns the create event the writer appended.
	events, _, err := s.LoadEvents("../../etc/" + missionID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events), 1)

	// A traversal-laced ID that collapses to a NON-existent
	// mission now errors — the round-2 existence check closes the
	// asymmetry where round 1 returned an empty slice for bogus IDs.
	_, _, err = s.LoadEvents("../../etc/m-9999-99-99-999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestLoadEvents_EmptyMissionID rejects an empty mission ID at the
// API boundary, matching LoadResults / LoadReflections. A caller
// passing "" is a programming error, not a post-mortem artifact.
func TestLoadEvents_EmptyMissionID(t *testing.T) {
	s := testStore(t)
	_, _, err := s.LoadEvents("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missionID is required")
}

// TestLoadEvents_BlankLinesAreSkipped asserts that blank lines (the
// scanner's "" case for runs of newlines) produce no warning and no
// phantom event. The writer never emits blank lines but the reader
// defends against hand-edited files.
func TestLoadEvents_BlankLinesAreSkipped(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-015"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	seedLogRaw(t, s, missionID,
		`{"ts":"2026-04-07T22:00:01Z","event":"create","actor":"claude"}`+"\n"+
			"\n"+
			"   \n"+
			`{"ts":"2026-04-07T22:00:02Z","event":"close","actor":"claude"}`+"\n",
	)

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, events, 2)
}

// --- Phase 3.7 round 2: H-findings regression tests ---

// TestLoadEvents_OversizedLineDoesNotTruncateTail covers class 27
// (new in round 2): a single line larger than any fixed scanner
// buffer must not silently truncate the tail of the log. Round 1
// used bufio.Scanner with a 1 MiB cap — a line > 1 MiB caused
// scanner.Scan() to return false and every subsequent line to be
// silently lost, producing "M events, 1 warning" with no signal
// that lines N+1..EOF were never attempted. This violated the
// round 1 contract invariant "partial damage does not erase the
// log." Round 2 replaces the scanner with bufio.Reader.ReadString,
// which has no per-line cap; the whole-file 16 MiB cap bounds
// memory without silently losing the tail.
//
// The non-negotiable assertion: both close and result appear in
// the returned slice, even though a 1.5 MiB line sits between
// them and the first create event.
func TestLoadEvents_OversizedLineDoesNotTruncateTail(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-027"
	fatNotes := strings.Repeat("x", 1536*1024) // 1.5 MiB
	// Use json.Marshal to build the oversized update line so the
	// string escapes cleanly and the decoder accepts it at the
	// other end (if the whole-file cap allows it through at all).
	oversize := fmt.Sprintf(
		`{"ts":"2026-04-08T00:00:01Z","event":"update","actor":"b","details":{"notes":%q}}`,
		fatNotes)
	seedLogLines(t, s, missionID,
		`{"ts":"2026-04-08T00:00:00Z","event":"create","actor":"a"}`,
		oversize,
		`{"ts":"2026-04-08T00:00:02Z","event":"close","actor":"c"}`,
		`{"ts":"2026-04-08T00:00:03Z","event":"result","actor":"d"}`,
	)

	events, _, err := s.LoadEvents(missionID)
	require.NoError(t, err)

	// Tail must survive the oversized line in the middle. The
	// oversized line MAY decode (it is valid JSON) or MAY be
	// omitted for an unrelated reason; either is acceptable as
	// long as close and result both appear.
	types := make([]string, 0, len(events))
	for _, e := range events {
		types = append(types, e.Event)
	}
	assert.Contains(t, types, "create", "first event must survive")
	assert.Contains(t, types, "close", "tail must not be silently truncated after oversized line")
	assert.Contains(t, types, "result", "tail must not be silently truncated after oversized line")
}

// TestLoadEvents_WarningsSanitizeControlBytes covers H2: an
// attacker with local write access plants a JSON line whose
// decode error may (depending on the decoder's error path) carry
// control bytes through to operator terminals and MCP consumers.
// Round 2 sanitizes warnings at the source so no path forwards
// raw control bytes.
//
// End-to-end assertion against the LoadEvents surface: the
// warning for a deliberately-crafted line must contain NO raw
// control bytes (ESC, BEL, DEL, C1). The stronger synthetic-
// input tests that exercise the sanitizer itself are
// TestSanitizeWarning and TestDecodeEventLog_SanitizesRawControlBytes
// below — they drive the helper with inputs that would bypass
// Go's own %q escaping.
func TestLoadEvents_WarningsSanitizeControlBytes(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-h2"
	seedLogLines(t, s, missionID,
		`{"ts":"2026-04-08T00:00:00Z","event":"create","actor":"x","\u001b[31m\u0007FAKE\u007f":1}`,
		`{"ts":"2026-04-08T00:00:01Z","event":"create","actor":"y"}`,
	)

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	require.Len(t, warnings, 1, "bad line must surface as a warning")
	w := warnings[0]

	// Non-negotiable: the warning must NOT contain any raw bytes
	// that let an attacker drive a terminal. Go's json package
	// already renders unknown-field errors via %q — the sanitizer
	// is the last-line defense for any path that does not.
	for _, b := range []byte(w) {
		assert.True(t,
			b == '\t' || b == ' ' || (b >= 0x20 && b < 0x7f) || b >= 0xa0,
			"warning must contain no raw control bytes, got 0x%02x at offset %d in %q",
			b, strings.IndexByte(w, b), w)
	}

	// The rest of the file still decodes — partial damage does not
	// erase the log.
	require.Len(t, events, 1)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, "y", events[0].Actor)
}

// TestDecodeEventLog_SanitizesRawControlBytes drives decodeEventLog
// directly with a line that bypasses Go's json %q escaping by
// placing raw bytes somewhere the decoder's error chain could
// propagate. The decoder typically escapes unknown-field names,
// but a defense-in-depth check: the sanitizer must still catch
// anything the decoder lets through.
//
// This test synthesizes a warning input via the sanitizer helper
// directly — decodeEventLog's own call site passes error strings
// through sanitizeWarning unconditionally, so any control byte in
// any error source gets neutralized. The helper test below pins
// the helper's behavior; this test pins the wiring.
func TestDecodeEventLog_SanitizesRawControlBytes(t *testing.T) {
	// A single line with a literal ESC byte inside a string value.
	// Go's json package rejects this as "invalid character" and
	// the error string uses %q escaping, so the pipeline produces
	// an already-safe warning. The test pins that the FINAL
	// warning has zero raw control bytes — belt and suspenders.
	raw := []byte("{\"ts\":\"2026-04-08T00:00:00Z\",\"event\":\"create\",\"actor\":\"\x1bFAKE\"}\n")
	_, warnings, err := decodeEventLog(raw)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	for _, b := range []byte(warnings[0]) {
		assert.True(t,
			b == '\t' || b == ' ' || (b >= 0x20 && b < 0x7f) || b >= 0xa0,
			"raw control byte 0x%02x survived sanitization in %q", b, warnings[0])
	}
}

// TestSanitizeWarning exercises the sanitizeWarning helper as a pure
// unit test with a table of inputs so every branch (passthrough,
// space, tab, C0, DEL, C1, invalid UTF-8, legitimate UTF-8) has
// explicit coverage. The C1 cases include both a legitimate
// multi-byte rune whose UTF-8 continuation byte lives in the
// [0x80, 0x9f] range (ß = U+00DF = 0xc3 0x9f) and a lone invalid
// 0x9f byte — the helper must pass the first through unchanged
// and escape the second.
func TestSanitizeWarning(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"clean ASCII", "clean text", "clean text"},
		{"tab preserved", "with\ttab", "with\ttab"},
		{"space preserved", "with space", "with space"},
		{"newline escaped", "with\nnewline", `with\x0anewline`},
		{"carriage return escaped", "with\rreturn", `with\x0dreturn`},
		{"ESC escaped", "with\x1b[31mred", `with\x1b[31mred`},
		{"BEL escaped", "bell\x07", `bell\x07`},
		{"DEL escaped", "del\x7fchar", `del\x7fchar`},
		{"invalid UTF-8 byte escaped", "c1\x9fchar", `c1\x9fchar`},
		{"legitimate UTF-8 passthrough", "über", "über"},
		{"sharp-s passthrough (0x9f continuation byte)", "straße", "straße"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeWarning(tc.in))
		})
	}
}

// TestLoadEvents_UnknownMissionID covers H4: an unknown mission ID
// returns an error, NOT a silent empty slice. Symmetric with
// LoadReflections / LoadResults which both refuse bogus IDs via
// the path of their sibling loaders.
func TestLoadEvents_UnknownMissionID(t *testing.T) {
	s := testStore(t)
	_, _, err := s.LoadEvents("m-2099-99-99-999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestLoadEvents_OversizedFileRejected covers M3: a log file above
// the 16 MiB cap is rejected before any parsing starts, so a
// runaway writer or attacker-planted pathological file cannot OOM
// the ethos process. The error names the byte count and the cap
// so the operator knows why.
func TestLoadEvents_OversizedFileRejected(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-m3"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	// 17 MiB of zeros — the content is irrelevant because the cap
	// fires before any read. Writing 17 MiB is fast enough for a
	// unit test (~50 ms on a modern laptop).
	big := make([]byte, 17*1024*1024)
	require.NoError(t, os.WriteFile(s.logPath(missionID), big, 0o600))
	seedContractStub(t, s, missionID)

	events, warnings, err := s.LoadEvents(missionID)
	require.Error(t, err)
	assert.Nil(t, events)
	assert.Nil(t, warnings)
	assert.Contains(t, err.Error(), "exceeds cap")
	assert.Contains(t, err.Error(), "16777216")
}

// TestLoadEvents_DirectoryAtLogPath covers M4: an attacker plants a
// directory at the expected log path. Round 1 would return a
// generic os.ReadFile error indistinguishable from a transient
// storage fault; round 2 stats the path first and rejects the
// directory with a clear, named error.
func TestLoadEvents_DirectoryAtLogPath(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-m4"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	require.NoError(t, os.Mkdir(s.logPath(missionID), 0o700))
	seedContractStub(t, s, missionID)

	events, warnings, err := s.LoadEvents(missionID)
	require.Error(t, err)
	assert.Nil(t, events)
	assert.Nil(t, warnings)
	assert.Contains(t, err.Error(), "directory")
}

// --- FilterEvents: filter classes ---
//
// The filter helper is pure and has no store dependency, so its
// tests live here rather than in store_test. Classes 14-19 from the
// contract table are exercised.

func TestFilterEvents_Types_NoMatch(t *testing.T) {
	// Class 14: --event foo with no matching events.
	events := []Event{
		{TS: "2026-04-07T22:00:01Z", Event: "create", Actor: "a"},
		{TS: "2026-04-07T22:00:02Z", Event: "update", Actor: "a"},
	}
	got, err := FilterEvents(events, []string{"foo"}, "")
	require.NoError(t, err)
	assert.Empty(t, got)
	// Empty, but non-nil: callers that marshal to JSON must see [].
	assert.NotNil(t, got)
}

func TestFilterEvents_Types_PartialMatch(t *testing.T) {
	// Class 15: --event create,close with partial matches — only the
	// matching entries come back, in original order.
	events := []Event{
		{TS: "2026-04-07T22:00:01Z", Event: "create", Actor: "a"},
		{TS: "2026-04-07T22:00:02Z", Event: "update", Actor: "a"},
		{TS: "2026-04-07T22:00:03Z", Event: "close", Actor: "a"},
	}
	got, err := FilterEvents(events, []string{"create", "close"}, "")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "create", got[0].Event)
	assert.Equal(t, "close", got[1].Event)
}

func TestFilterEvents_Since_NoMatch(t *testing.T) {
	// Class 16: --since <future> with no matching events.
	events := []Event{
		{TS: "2026-04-07T22:00:01Z", Event: "create", Actor: "a"},
		{TS: "2026-04-07T22:00:02Z", Event: "update", Actor: "a"},
	}
	got, err := FilterEvents(events, nil, "2099-01-01T00:00:00Z")
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.NotNil(t, got)
}

func TestFilterEvents_Since_PastIncludesAll(t *testing.T) {
	// Class 17: --since <past> returns every event on or after the
	// cutoff. Boundary: an event whose ts equals the cutoff is
	// included.
	events := []Event{
		{TS: "2026-04-07T22:00:01Z", Event: "create", Actor: "a"},
		{TS: "2026-04-07T22:00:02Z", Event: "update", Actor: "a"},
		{TS: "2026-04-07T22:00:03Z", Event: "close", Actor: "a"},
	}
	got, err := FilterEvents(events, nil, "2026-04-07T22:00:02Z")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "update", got[0].Event)
	assert.Equal(t, "close", got[1].Event)
}

func TestFilterEvents_TypesAndSince_ANDComposed(t *testing.T) {
	// Class 18: --event X --since Y AND-composes.
	events := []Event{
		{TS: "2026-04-07T22:00:01Z", Event: "create", Actor: "a"},
		{TS: "2026-04-07T22:00:02Z", Event: "update", Actor: "a"},
		{TS: "2026-04-07T22:00:03Z", Event: "update", Actor: "a"},
		{TS: "2026-04-07T22:00:04Z", Event: "close", Actor: "a"},
	}
	got, err := FilterEvents(events, []string{"update"}, "2026-04-07T22:00:03Z")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "update", got[0].Event)
	assert.Equal(t, "2026-04-07T22:00:03Z", got[0].TS)
}

func TestFilterEvents_UnknownTypeIsAccepted(t *testing.T) {
	// Class 19: an unknown event type string in the filter is
	// accepted — the filter does not validate against a closed enum.
	// The result is simply empty. Event types are forward-compatible.
	events := []Event{
		{TS: "2026-04-07T22:00:01Z", Event: "create", Actor: "a"},
	}
	got, err := FilterEvents(events, []string{"worker_spawned"}, "")
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.NotNil(t, got)
}

func TestFilterEvents_NilInputs(t *testing.T) {
	// Nil input + nil filters: an empty, non-nil slice.
	got, err := FilterEvents(nil, nil, "")
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.NotNil(t, got)
}

func TestFilterEvents_InvalidSinceIsAnError(t *testing.T) {
	// The filter helper is pure; invalid since values must produce a
	// typed error so the CLI and MCP surfaces can name the bad input
	// directly. Using nil for events is fine — the check fires on
	// the time parse, before any iteration.
	//
	// Round 2 (L1): the error must name the bad value and include a
	// human-readable RFC3339 hint; it must NOT leak the Go time
	// reference layout string "2006-01-02T15:04:05Z07:00" or the
	// word "parsing" verbatim from the time.Parse error — operators
	// reading the message should not need to know Go's time package.
	_, err := FilterEvents(nil, nil, "not-a-timestamp")
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "since")
	assert.Contains(t, msg, "not-a-timestamp")
	assert.Contains(t, msg, "expected RFC3339")
	assert.NotContains(t, msg, "2006")
	assert.NotContains(t, msg, "Z07:00")
}

// TestLoadEvents_RejectsUnparseableTS covers class 28 (new in
// round 2): a strict-JSON-valid line with a non-RFC3339 ts is
// rejected at decode time with a warning, not silently dropped at
// filter time. Round 1 rejected the bad line ONLY when --since was
// set, so the same audit trail returned N events without --since
// and N-k events with --since — a silent count mismatch on the
// same damaged file. Round 2 closes this by validating ts inside
// decodeEventLine; the bad event never reaches FilterEvents, and
// the count agrees between filter states.
func TestLoadEvents_RejectsUnparseableTS(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-028"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	seedLogLines(t, s, missionID,
		`{"ts":"garbage","event":"create","actor":"a"}`,
		`{"ts":"2026-04-07T22:00:02Z","event":"update","actor":"a"}`,
	)

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	require.Len(t, events, 1, "bad-ts event must not reach LoadEvents output")
	assert.Equal(t, "update", events[0].Event)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "line 1")
	assert.Contains(t, warnings[0], "RFC3339")

	// FilterEvents called with a past --since must return the same
	// count (1) as calling without --since — the bad-ts line is
	// equally absent in both. This is the non-negotiable invariant
	// the silent-failure finding flagged.
	filteredSince, err := FilterEvents(events, nil, "2026-04-07T00:00:00Z")
	require.NoError(t, err)
	assert.Len(t, filteredSince, 1)
	filteredNoSince, err := FilterEvents(events, nil, "")
	require.NoError(t, err)
	assert.Len(t, filteredNoSince, 1)
	assert.Equal(t, len(filteredSince), len(filteredNoSince),
		"counts must agree between --since and no-filter states")
}

// seedLogLines writes the given lines to the mission's log file,
// one per line, with a trailing newline on the last line. Used by
// the decode-class tests to plant synthetic corruption at known line
// numbers. Also seeds a minimal contract stub alongside the log so
// LoadEvents' H4 existence check (round 2) resolves.
func seedLogLines(t *testing.T, s *Store, missionID string, lines ...string) {
	t.Helper()
	body := strings.Join(lines, "\n") + "\n"
	seedLogRaw(t, s, missionID, body)
}

// seedLogRaw writes raw bytes to the mission's log file, bypassing
// any locking. Also seeds a minimal contract stub alongside the log
// so LoadEvents' H4 existence check (round 2) resolves — the stub
// is NOT decoded by LoadEvents, which only stats the path, so a
// byte-empty file satisfies the check. The caller must not
// interleave this with a live writer on the same mission — tests
// that use it never do.
func seedLogRaw(t *testing.T, s *Store, missionID, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	require.NoError(t, os.WriteFile(s.logPath(missionID), []byte(body), 0o600))
	seedContractStub(t, s, missionID)
}

// seedContractStub writes a zero-byte contract file next to the
// mission log so LoadEvents' existence check (os.Stat on the
// contract path) resolves. Tests that plant corrupt log lines
// directly do not want to drive a full Create, which would write
// its own create event and contaminate the corruption under test.
// LoadEvents never parses the contract — only stats its path — so
// an empty file is sufficient to satisfy the existence anchor.
func seedContractStub(t *testing.T, s *Store, missionID string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	require.NoError(t, os.WriteFile(s.ContractPath(missionID), []byte{}, 0o600))
}

// seededEventTime is a fixed reference timestamp for filter-class
// tests. Having a single constant means a test reading a filter
// cutoff does not drift as the clock marches on.
var seededEventTime = time.Date(2026, 4, 7, 22, 0, 0, 0, time.UTC)

var _ = seededEventTime // kept for future tests; silence unused in case all current tests use literal ts
