package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

// missionWithLiveEvent creates a mission in a fresh repo and writes one older
// live event for another session whose details hold another user's home. It
// returns the repo, the mission store and the mission id.
func missionWithLiveEvent(t *testing.T, id, session string, ts int64) (string, *mission.Store) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	initGitRepo(t, repo)
	s := mission.NewStoreWithRoots(repo, t.TempDir())
	c := mission.Contract{
		MissionID: id, Status: mission.StatusOpen,
		CreatedAt: "2026-04-07T21:30:00Z", UpdatedAt: "2026-04-07T21:30:00Z",
		Leader: "claude", Worker: "bwk",
		Evaluator:       mission.Evaluator{Handle: "djb", PinnedAt: "2026-04-07T21:30:00Z"},
		Inputs:          mission.Inputs{Ticket: "ethos-07m.5", Files: []string{"internal/session/store.go"}},
		WriteSet:        []string{"internal/mission/"},
		Tools:           []string{"Read", "Write", "Edit"},
		SuccessCriteria: []string{"make check passes"},
		Budget:          mission.Budget{Rounds: 3, ReflectionAfterEach: true},
		CurrentRound:    1,
	}
	require.NoError(t, s.Create(&c))
	live := liveMissionLogPath(repo, id, session)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	line := `{"ts":"` + audit.FormatLineTS(ts) + `","event":"update","actor":"claude","details":{"note":"see /home/bob/x"}}` + "\n"
	require.NoError(t, os.WriteFile(live, []byte(line), 0o600))
	return repo, s
}

func requireUpdateEvent(t *testing.T, s *mission.Store, id string) {
	t.Helper()
	events, warnings, err := s.LoadEvents(id)
	require.NoError(t, err)
	assert.Empty(t, warnings, "a sealed event line must decode")
	var found bool
	for _, e := range events {
		if e.Event == "update" {
			found = true
			assert.NotContains(t, e.Details["note"], "bob", "the home is redacted")
		}
	}
	assert.True(t, found, "the sealed update event must load: %v", events)
}

// TestSealMission_SealedEventLoads: an older live mission event whose details
// name another user's home is rewritten when sealed, in a form the strict
// mission-event decoder accepts, so LoadEvents still returns it.
func TestSealMission_SealedEventLoads(t *testing.T) {
	const id = "m-2026-07-21-020"
	repo, s := missionWithLiveEvent(t, id, "sessX", 100)
	_, err := SealMission(repo, id, time.Now().UTC(), SealOptions{})
	require.NoError(t, err)
	requireUpdateEvent(t, s, id)
}

// TestQuarantineChunk_RecoveredMissionEventLoads: the same for the events a
// quarantine re-seals from the live file.
func TestQuarantineChunk_RecoveredMissionEventLoads(t *testing.T) {
	const id = "m-2026-07-21-021"
	repo, s := missionWithLiveEvent(t, id, "sessX", 100)
	sealed := sealedMissionDir(repo, id)
	require.NoError(t, os.MkdirAll(sealed, 0o700))
	chunk := audit.MissionChunkFile("sessX", 100, 300)
	require.NoError(t, os.WriteFile(filepath.Join(sealed, chunk), []byte("\x00garbage\n"), 0o600))
	_, err := QuarantineChunk(repo, filepath.Join(sealed, chunk), "bad")
	require.NoError(t, err)
	requireUpdateEvent(t, s, id)
}

// TestAudit_RelativeHomeSegmentIsNotAHome: a Read of the repo-relative
// app/home/page.tsx is stored as written, and the precondition that the file
// was read is then satisfied: a /home/ in the middle of a relative path begins
// no path.
func TestAudit_RelativeHomeSegmentIsNotAHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	const target = "app/home/page.tsx"
	e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": target}},
		"sess-h", dir, time.Now())
	assert.Equal(t, target, e.ToolInput["file_path"], "stored as written, not [redacted-path]")
	line, err := json.Marshal(e)
	require.NoError(t, err)
	_, rule := guardAuditLineWhy(line)
	assert.Empty(t, rule, "the sink guard keeps it too")

	writeSessionReadAudit(t, dir, "sess-h", target)
	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "read before write"}},
		mission.Inputs{}, nil)
	reason, deny, err := EvaluatePreconditions(c, "Write", map[string]any{"file_path": target}, "sess-h", dir, dir)
	require.NoError(t, err)
	assert.False(t, deny, "the Read satisfies the precondition: %q", reason)
}

func missionGuardLine(t *testing.T, line string) (map[string]any, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	out, rule, marker := missionLineGuard(t.TempDir())([]byte(line))
	require.False(t, marker, "an event stays an event")
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var e mission.Event
	require.NoError(t, dec.Decode(&e), "the stored line must decode strictly: %s", out)
	var m map[string]any
	d2 := json.NewDecoder(bytes.NewReader(out))
	d2.UseNumber()
	require.NoError(t, d2.Decode(&m))
	m["_raw"] = string(out)
	return m, rule
}

// TestMissionLineGuard_KeepsNumbersExactly: a number a float64 would round
// reaches the sealed line digit for digit.
func TestMissionLineGuard_KeepsNumbersExactly(t *testing.T) {
	ts := audit.FormatLineTS(100)
	m, _ := missionGuardLine(t, `{"ts":"`+ts+`","event":"update","actor":"a","details":{"a":12345678901234567890,"b":1152921504606846977}}`)
	assert.Contains(t, m["_raw"], "12345678901234567890")
	assert.Contains(t, m["_raw"], "1152921504606846977")
}

// TestMissionLineGuard_ReportsADuplicateKey: a duplicate key, at the top level
// or nested, is reported as a rule so a notice is printed; the last value is
// the one stored.
func TestMissionLineGuard_ReportsADuplicateKey(t *testing.T) {
	ts := audit.FormatLineTS(100)
	m, rule := missionGuardLine(t, `{"ts":"`+ts+`","event":"update","actor":"a","actor":"b"}`)
	assert.Contains(t, rule, `duplicate key "actor"`)
	assert.Equal(t, "b", m["actor"])
	m, rule = missionGuardLine(t, `{"ts":"`+ts+`","event":"update","actor":"a","details":{"k":1,"k":2}}`)
	assert.Contains(t, rule, `duplicate key "k"`)
	assert.Equal(t, json.Number("2"), m["details"].(map[string]any)["k"])
}

// TestMissionLineGuard_ScrubsOnlyTheLeakingLeaves: details the record pass
// refuses lose only the leaves that leak, which are marked, and keep the rest.
func TestMissionLineGuard_ScrubsOnlyTheLeakingLeaves(t *testing.T) {
	ts := audit.FormatLineTS(100)
	m, rule := missionGuardLine(t, `{"ts":"`+ts+`","event":"update","actor":"a","details":{"ok":"fine","bad":"a %2Fhome%2Fbob%2Fx"}}`)
	d := m["details"].(map[string]any)
	assert.Equal(t, "fine", d["ok"], "a non-leaking key survives")
	assert.Equal(t, "[redacted-path]", d["bad"])
	assert.Equal(t, true, d["redacted"])
	assert.NotEmpty(t, rule)
}

// TestMissionLineGuard_KeysThatDifferOnlyInCaseAreNotMerged: encoding/json
// folds actor onto Actor and details onto DETAILS, so the typed decode would
// merge them silently. Such a line goes to the audit guard, which replaces it
// with a marker and a notice, as at base.
func TestMissionLineGuard_KeysThatDifferOnlyInCaseAreNotMerged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ts := audit.FormatLineTS(100)
	for _, line := range []string{
		`{"ts":"` + ts + `","event":"update","actor":"a","Actor":"b"}`,
		`{"ts":"` + ts + `","event":"update","actor":"a","details":{"k":1},"DETAILS":{"k":2}}`,
	} {
		out, rule, marker := missionLineGuard(t.TempDir())([]byte(line))
		assert.True(t, marker, "%s -> %s", line, out)
		assert.Contains(t, rule, "differ only in case")
	}
}

// TestMissionLineGuard_KeepsACallersRedactedKey: when the guard marks details
// it does not overwrite a redacted key the caller already had; its marker takes
// the first free redacted#N, and the rule names the key it chose.
func TestMissionLineGuard_KeepsACallersRedactedKey(t *testing.T) {
	ts := audit.FormatLineTS(100)
	for name, orig := range map[string]string{"string": `"orig"`, "false": `false`} {
		m, rule := missionGuardLine(t, `{"ts":"`+ts+`","event":"update","actor":"a","details":{"redacted":`+orig+`,"ok":"fine","bad":"a %2Fhome%2Fbob%2Fx"}}`)
		d := m["details"].(map[string]any)
		want := any("orig")
		if name == "false" {
			want = false
		}
		assert.Equal(t, want, d["redacted"], name)
		assert.Equal(t, true, d["redacted#1"], name)
		assert.Equal(t, "fine", d["ok"], name)
		assert.Contains(t, rule, `; marked details under "redacted#1" (key "redacted" was taken)`, name)
		assert.NotContains(t, rule, "renamed key", name)
	}
}

// TestMissionLineGuard_FallbackRedactsCredentialsBeforeRenamingKeys: when the
// record pass fails on one leaf (an encoded home) and the guard scrubs the
// details leaf by leaf, a credential-named key that the content pass would
// rename (password@example.com) still has its value replaced: credentials are
// judged before any key is renamed.
func TestMissionLineGuard_FallbackRedactsCredentialsBeforeRenamingKeys(t *testing.T) {
	ts := audit.FormatLineTS(100)
	m, rule := missionGuardLine(t, `{"ts":"`+ts+`","event":"update","actor":"a","details":{"password@example.com":"hunter2","bad":"a %2Fhome%2Fbob%2Fx"}}`)
	assert.NotEmpty(t, rule)
	assert.NotContains(t, m["_raw"], "hunter2")
	assert.NotContains(t, m["_raw"], "bob")
}
