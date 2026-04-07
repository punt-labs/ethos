//go:build !windows

package mission

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendEvent_RoundTrip(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))

	// Create already wrote one event; append four more.
	for i := 1; i <= 4; i++ {
		require.NoError(t, s.appendEvent("m-2026-04-07-001", Event{
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
		require.NoError(t, s.appendEvent("m-2026-04-07-001", Event{
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

	require.NoError(t, s.appendEvent("m-2026-04-07-001", Event{
		TS: "2026-04-07T22:00:01Z", Event: "update", Actor: "claude",
	}))
	require.NoError(t, s.appendEvent("m-2026-04-07-001", Event{
		TS: "2026-04-07T22:00:02Z", Event: "update", Actor: "claude",
	}))
	require.NoError(t, s.appendEvent("m-2026-04-07-001", Event{
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

	require.NoError(t, s.appendEvent("m-2026-04-07-001", Event{
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
	require.NoError(t, s.Close("m-2026-04-07-001", StatusClosed))

	events := readLog(t, s, "m-2026-04-07-001")
	require.Len(t, events, 2)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, "close", events[1].Event)
	assert.Equal(t, "closed", events[1].Details["status"])
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
