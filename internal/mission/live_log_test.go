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

// TestMissionLiveLog_SessionlessAppendRoutesToLiveZone is B4(b): a sessionless
// in-repo append (ad-hoc CLI, no resolvable session) must land in the reserved
// live log under the local zone, never the tracked log.jsonl, and must not hide
// a real session's already-written events from the union read.
func TestMissionLiveLog_SessionlessAppendRoutesToLiveZone(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	id := "m-2026-07-21-050"

	// A session-scoped store creates the mission: the create event lands in
	// sessA's live log.
	storeA := NewStoreWithRoots(repoRoot, globalRoot).WithSessionID("sessA")
	require.NoError(t, storeA.Create(newContract(id)))

	// A sessionless store updates it: the update event must route to the
	// reserved-session live log, not the tracked tree.
	storeB := NewStoreWithRoots(repoRoot, globalRoot)
	loaded, err := storeB.Load(id)
	require.NoError(t, err)
	loaded.Context = "touched with no session"
	require.NoError(t, storeB.Update(loaded))

	assert.FileExists(t, audit.LiveMissionLogPath(repoRoot, id, "sessA"))
	assert.FileExists(t, audit.LiveMissionLogPath(repoRoot, id, sessionlessID))
	tracked := filepath.Join(audit.SealedMissionDir(repoRoot, id), "log.jsonl")
	_, statErr := os.Stat(tracked)
	assert.True(t, os.IsNotExist(statErr),
		"sessionless append must not write the tracked log.jsonl: %v", statErr)

	// The union read returns both sessions' events — sessA is not stranded.
	events, _, err := storeB.LoadEvents(id)
	require.NoError(t, err)
	var haveCreate, haveUpdate bool
	for _, e := range events {
		switch e.Event {
		case "create":
			haveCreate = true
		case "update":
			haveUpdate = true
		}
	}
	assert.True(t, haveCreate, "sessA's create event must survive the sessionless append")
	assert.True(t, haveUpdate, "the sessionless update event must be read")
}

// TestMissionLiveLog_LazySessionResolverRoutesLater is R7-1: a store built
// before the session mapping exists (the MCP server starting first) must not
// freeze the empty resolution. The first non-empty resolver result routes and
// is then cached, so a session that appears after construction still attributes
// its events instead of stranding them in the reserved no-session log.
func TestMissionLiveLog_LazySessionResolverRoutesLater(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	id := "m-2026-07-21-007"

	// The session mapping does not exist yet: the resolver returns empty.
	var sid string
	s := NewStoreWithRoots(repoRoot, globalRoot).
		WithSessionResolver(func() string { return sid })

	require.NoError(t, s.Create(newContract(id)))
	assert.FileExists(t, audit.LiveMissionLogPath(repoRoot, id, sessionlessID),
		"a sessionless create lands in the reserved log")

	// The session mapping appears. The next append must route to the real
	// session's live log, not the reserved no-session log.
	sid = "sessLate"
	require.NoError(t, appendEventForTest(t, s, id, Event{
		TS: "2026-07-21T00:00:00Z", Event: "dispatch", Actor: "claude",
	}))
	assert.FileExists(t, audit.LiveMissionLogPath(repoRoot, id, "sessLate"),
		"an append after the mapping appears must route to the real session")

	// Once resolved non-empty, the id is cached: a later resolver change is
	// ignored, since a session does not change mid-process.
	sid = "sessChanged"
	require.NoError(t, appendEventForTest(t, s, id, Event{
		TS: "2026-07-21T00:00:01Z", Event: "close", Actor: "claude",
	}))
	_, statErr := os.Stat(audit.LiveMissionLogPath(repoRoot, id, "sessChanged"))
	assert.True(t, os.IsNotExist(statErr),
		"the resolved session id must be cached, not re-resolved per append")
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

func TestMissionLiveLog_ResiduePerLineFilter(t *testing.T) {
	s, repoRoot := twoTreeSessionStore(t, "reader")
	id := "m-2026-07-21-006"
	require.NoError(t, s.Create(newContract(id)))
	// Drop the reader session's own create event so the fixture is exactly the
	// two residue lines plus A's sealed chunk under test.
	require.NoError(t, os.Remove(audit.LiveMissionLogPath(repoRoot, id, "reader")))

	// Session A sealed a chunk up to ts=200: its residue lines at or below 200
	// were already copied into that chunk. Session B never sealed a chunk.
	sealedDir := audit.SealedMissionDir(repoRoot, id)
	require.NoError(t, os.MkdirAll(sealedDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(sealedDir, audit.MissionChunkFile("A", 200, 200)),
		[]byte(`{"ts":"`+audit.FormatLineTS(200)+`","event":"create","actor":"a"}`+"\n"), 0o600))

	// The superseded shared-live residue holds one already-sealed line for A
	// (ts 150 <= A's sealed 200 → drop) and one never-sealed line for the
	// lagging B (no chunk → keep). Each carries its own session tag.
	residue := audit.MissionResiduePath(repoRoot, id)
	require.NoError(t, os.MkdirAll(filepath.Dir(residue), 0o700))
	body := `{"ts":"` + audit.FormatLineTS(150) + `","session":"A","event":"dispatch","actor":"a"}` + "\n" +
		`{"ts":"` + audit.FormatLineTS(150) + `","session":"B","event":"dispatch","actor":"b"}` + "\n"
	require.NoError(t, os.WriteFile(residue, []byte(body), 0o600))

	events, warnings, err := s.LoadEvents(id)
	require.NoError(t, err)
	require.Empty(t, warnings)

	// A's residue line was already sealed → dropped, no duplicate. B's lagging
	// line survives. The chunk's create line is the only A event.
	require.Len(t, events, 2)
	var dispatchA, dispatchB, createA int
	for _, e := range events {
		switch {
		case e.Event == "dispatch" && e.Actor == "a":
			dispatchA++
		case e.Event == "dispatch" && e.Actor == "b":
			dispatchB++
		case e.Event == "create" && e.Actor == "a":
			createA++
		}
	}
	assert.Zero(t, dispatchA, "A's already-sealed residue line must be dropped, not duplicated")
	assert.Equal(t, 1, dispatchB, "B's never-sealed residue line must survive")
	assert.Equal(t, 1, createA, "A's sealed chunk line must remain")
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
