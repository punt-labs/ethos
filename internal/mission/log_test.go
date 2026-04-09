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

// TestLoadEvents_MissingFile covers class 1: a brand-new mission
// with no writer calls has no log file on disk. LoadEvents returns
// an empty slice, nil warnings, nil error — symmetric with
// LoadResults and LoadReflections conventions for missing sibling
// files.
func TestLoadEvents_MissingFile(t *testing.T) {
	s := testStore(t)
	// Do NOT call Create; no log file exists.
	events, warnings, err := s.LoadEvents("m-2026-04-07-999")
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.Empty(t, warnings)
}

// TestLoadEvents_EmptyFile covers class 2: a zero-byte log file
// returns the same empty state as a missing file. Symmetric with
// the reflections and results empty-file handling.
func TestLoadEvents_EmptyFile(t *testing.T) {
	s := testStore(t)
	// Create the missions directory and seed an empty log file.
	missionID := "m-2026-04-07-002"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	require.NoError(t, os.WriteFile(s.logPath(missionID), []byte{}, 0o600))

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
	missionID := "m-2026-04-07-012"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	require.NoError(t, os.WriteFile(s.logPath(missionID),
		[]byte(`{"ts":"2026-04-07T22:00:01Z","event":"create","actor":"claude"}`+"\n"),
		0o600))
	require.NoError(t, os.Chmod(s.logPath(missionID), 0o000))
	t.Cleanup(func() {
		// Restore permissions so t.TempDir cleanup can remove the file.
		_ = os.Chmod(s.logPath(missionID), 0o600)
	})

	events, _, err := s.LoadEvents(missionID)
	require.Error(t, err)
	assert.Nil(t, events)
	// Must not be confused with "missing file" — the error string
	// should carry the OS permission hint from the wrapped syscall.
	assert.Contains(t, strings.ToLower(err.Error()), "permission")
}

// TestLoadEvents_SymlinkPolicy covers class 13: the reader follows
// the same path-resolution discipline Store.Load uses for contracts.
// A symlink at the log path whose target is a readable JSONL file
// is followed; the mission ID is run through filepath.Base so a
// traversal-laced ID only ever points inside missionsDir. This test
// documents that matching behavior.
func TestLoadEvents_SymlinkPolicy(t *testing.T) {
	s := testStore(t)
	missionID := "m-2026-04-07-013"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	// Write a log body to an out-of-directory file, then symlink the
	// mission's log path to it. os.ReadFile follows the symlink, so
	// LoadEvents must succeed — matching Store.Load's implicit
	// symlink-follow policy for contracts.
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	require.NoError(t, os.WriteFile(outside,
		[]byte(`{"ts":"2026-04-07T22:00:01Z","event":"create","actor":"claude"}`+"\n"),
		0o600))
	require.NoError(t, os.Symlink(outside, s.logPath(missionID)))

	events, warnings, err := s.LoadEvents(missionID)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, events, 1)
	assert.Equal(t, "create", events[0].Event)
}

// TestLoadEvents_TraversalIDCannotEscape asserts the path-base
// defense: even a traversal-laced mission ID only ever resolves
// inside missionsDir. Complements class 13's symlink-follow — the
// trust anchor is logPath, which runs the ID through filepath.Base.
func TestLoadEvents_TraversalIDCannotEscape(t *testing.T) {
	s := testStore(t)
	// Seed a legitimate log file; then ask for it via a traversal
	// ID. logPath will strip the "../" prefix via filepath.Base, so
	// the request resolves to the legitimate file. The point is not
	// that the traversal succeeds — it is that the traversal is
	// collapsed by filepath.Base before any open, so the reader can
	// never be directed at a sibling file or outside the missions
	// directory.
	missionID := "m-2026-04-07-014"
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	require.NoError(t, os.WriteFile(s.logPath(missionID),
		[]byte(`{"ts":"2026-04-07T22:00:01Z","event":"create","actor":"claude"}`+"\n"),
		0o600))

	events, _, err := s.LoadEvents("../../etc/" + missionID)
	require.NoError(t, err)
	require.Len(t, events, 1)
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
	_, err := FilterEvents(nil, nil, "not-a-timestamp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "since")
}

func TestFilterEvents_EventWithInvalidTSSkippedUnderSince(t *testing.T) {
	// An event whose on-disk ts is not RFC3339 cannot be compared to
	// --since. The filter treats such events as "cannot satisfy
	// since" and drops them rather than surfacing a fatal error — the
	// policy is forward-compatibility with the writer, matching the
	// unknown-event-type policy above. The event is preserved when
	// --since is absent.
	events := []Event{
		{TS: "garbage", Event: "create", Actor: "a"},
		{TS: "2026-04-07T22:00:02Z", Event: "update", Actor: "a"},
	}
	got, err := FilterEvents(events, nil, "2026-04-07T00:00:00Z")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "update", got[0].Event)

	// Without --since, the bad-ts event is kept — filter does not
	// judge on-disk validity; it only answers "matches the filters".
	got, err = FilterEvents(events, nil, "")
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// seedLogLines writes the given lines to the mission's log file,
// one per line, with a trailing newline on the last line. Used by
// the decode-class tests to plant synthetic corruption at known line
// numbers.
func seedLogLines(t *testing.T, s *Store, missionID string, lines ...string) {
	t.Helper()
	body := strings.Join(lines, "\n") + "\n"
	seedLogRaw(t, s, missionID, body)
}

// seedLogRaw writes raw bytes to the mission's log file, bypassing
// any locking. The caller must not interleave this with a live
// writer on the same mission — tests that use it never do.
func seedLogRaw(t *testing.T, s *Store, missionID, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	require.NoError(t, os.WriteFile(s.logPath(missionID), []byte(body), 0o600))
}

// seededEventTime is a fixed reference timestamp for filter-class
// tests. Having a single constant means a test reading a filter
// cutoff does not drift as the clock marches on.
var seededEventTime = time.Date(2026, 4, 7, 22, 0, 0, 0, time.UTC)

var _ = seededEventTime // kept for future tests; silence unused in case all current tests use literal ts
