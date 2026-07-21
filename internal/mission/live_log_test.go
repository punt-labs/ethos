//go:build !windows

package mission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// firstLineTS returns the Unix-ns ts of the first JSONL line in data.
func firstLineTS(data []byte) (int64, error) {
	lines := audit.SplitLines(data)
	var e Event
	if err := json.Unmarshal(lines[0], &e); err != nil {
		return 0, err
	}
	return audit.ParseLineTS(e.TS)
}

// twoTreeSessionStore builds a DES-058 two-tree store with a session id, so
// mission event appends route to the per-(mission, session) live log.
func twoTreeSessionStore(t *testing.T, sessionID string) (*Store, string) {
	t.Helper()
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, globalRoot).WithSessionID(sessionID)
	return s, repoRoot
}

func TestMissionLiveLog_WriteLandsInLocalZone(t *testing.T) {
	s, repoRoot := twoTreeSessionStore(t, "sess1")
	id := "m-2026-07-21-001"
	require.NoError(t, s.Create(newContract(id)))

	// The create event must land in the machine-local live log, not the
	// tracked log.jsonl.
	live := audit.LiveMissionLogPath(repoRoot, id, "sess1")
	data, err := os.ReadFile(live)
	require.NoError(t, err, "live mission log should exist at %s", live)
	assert.Contains(t, string(data), `"event":"create"`)

	tracked := filepath.Join(audit.SealedMissionDir(repoRoot, id), "log.jsonl")
	_, statErr := os.Stat(tracked)
	assert.True(t, os.IsNotExist(statErr),
		"tracked log.jsonl must not be written in the live path: %v", statErr)
}

func TestMissionLiveLog_MonotonicTimestamps(t *testing.T) {
	s, repoRoot := twoTreeSessionStore(t, "sess1")
	id := "m-2026-07-21-002"
	require.NoError(t, s.Create(newContract(id)))
	for i := 0; i < 3; i++ {
		require.NoError(t, appendEventForTest(t, s, id, Event{
			TS: "2026-07-21T00:00:00Z", Event: "update", Actor: "claude",
		}))
	}
	live := audit.LiveMissionLogPath(repoRoot, id, "sess1")
	data, err := os.ReadFile(live)
	require.NoError(t, err)
	var last int64
	for _, raw := range audit.SplitLines(data) {
		var e Event
		require.NoError(t, json.Unmarshal(raw, &e))
		ts, perr := audit.ParseLineTS(e.TS)
		require.NoError(t, perr)
		assert.Greater(t, ts, last, "timestamps must be strictly increasing")
		last = ts
	}
}

func TestMissionLiveLog_UnionReadReturnsEvents(t *testing.T) {
	s, _ := twoTreeSessionStore(t, "sess1")
	id := "m-2026-07-21-003"
	require.NoError(t, s.Create(newContract(id)))
	require.NoError(t, appendEventForTest(t, s, id, Event{
		TS: "2026-07-21T00:00:00Z", Event: "dispatch", Actor: "claude",
	}))

	events, warnings, err := s.LoadEvents(id)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, events, 2)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, "dispatch", events[1].Event)
}

func TestMissionLiveLog_UnionReadUnionsSealedAndLive(t *testing.T) {
	s, repoRoot := twoTreeSessionStore(t, "sess1")
	id := "m-2026-07-21-004"
	require.NoError(t, s.Create(newContract(id)))

	// Simulate a prior seal: move the create line into a sealed chunk and
	// leave a later live event past its watermark.
	live := audit.LiveMissionLogPath(repoRoot, id, "sess1")
	data, err := os.ReadFile(live)
	require.NoError(t, err)
	createTS, err := firstLineTS(data)
	require.NoError(t, err)
	sealedDir := audit.SealedMissionDir(repoRoot, id)
	require.NoError(t, os.MkdirAll(sealedDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(sealedDir, audit.MissionChunkFile("sess1", createTS, createTS)),
		data, 0o600))

	require.NoError(t, appendEventForTest(t, s, id, Event{
		TS: "2026-07-21T00:00:00Z", Event: "close", Actor: "claude",
	}))

	events, _, err := s.LoadEvents(id)
	require.NoError(t, err)
	require.Len(t, events, 2, "union of sealed create + live close")
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, "close", events[1].Event)
}

func TestMissionLiveLog_DrainsLegacyResidue(t *testing.T) {
	s, repoRoot := twoTreeSessionStore(t, "sess1")
	id := "m-2026-07-21-005"
	require.NoError(t, s.Create(newContract(id)))
	// A superseded shared-live missions/<id>.jsonl residue holds a
	// pre-discipline event that must join the read as the oldest legacy pool
	// (carried refinement (b), docs §Migration).
	residue := audit.MissionResiduePath(repoRoot, id)
	require.NoError(t, os.MkdirAll(filepath.Dir(residue), 0o700))
	require.NoError(t, os.WriteFile(residue,
		[]byte(`{"ts":"2020-01-01T00:00:00Z","event":"dispatch","actor":"legacy"}`+"\n"), 0o600))

	events, _, err := s.LoadEvents(id)
	require.NoError(t, err)
	require.Len(t, events, 2, "residue dispatch + live create")
	// The residue line predates the discipline, so it sorts first.
	assert.Equal(t, "dispatch", events[0].Event)
	assert.Equal(t, "legacy", events[0].Actor)
	assert.Equal(t, "create", events[1].Event)
}
