package mission

import (
	"errors"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// A crash after the corrections rename and before the contract rename leaves
// corrections without decisions. The next run completes the import and leaves
// the corrections bytes as the crash left them.
func TestUpgradeLegacyAuthority_ResumesAfterCrashBetweenRenames(t *testing.T) {
	s, c, root := legacyReflectionReader(t, true, 1)
	dir := filepath.Join(root, ".punt-labs", "ethos", "missions", c.MissionID)

	writes := 0
	s.persistenceCheck = func(phase string) error {
		if phase != "mission_write" {
			return nil
		}
		writes++
		if writes == 2 {
			panic("crash between the corrections rename and the contract rename")
		}
		return nil
	}
	require.Panics(t, func() { _, _, _ = s.UpgradeLegacyAuthority(c.MissionID) })
	s.persistenceCheck = nil

	corrections, err := os.ReadFile(filepath.Join(dir, "corrections.yaml"))
	require.NoError(t, err, "the crash happened after the corrections rename")
	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	require.Nil(t, loaded.RoundDecisions, "the crash happened before the contract rename")

	upgraded, _, err := s.UpgradeLegacyAuthority(c.MissionID)
	require.NoError(t, err)
	assert.True(t, upgraded)
	loaded, err = s.Load(c.MissionID)
	require.NoError(t, err)
	require.NotNil(t, loaded.RoundDecisions)
	after, err := os.ReadFile(filepath.Join(dir, "corrections.yaml"))
	require.NoError(t, err)
	assert.Equal(t, string(corrections), string(after), "corrections kept as the crash left them")

	upgraded, _, err = s.UpgradeLegacyAuthority(c.MissionID)
	require.NoError(t, err)
	assert.False(t, upgraded, "second run is a no-op")
}

// A directory-sync failure after the rename leaves the new bytes installed, so
// the file is rewritten: ReplaceRecordFile reports the durability failure apart
// from the error.
func TestReplaceRecordFile_DurabilityFailureIsNotAnError(t *testing.T) {
	root := t.TempDir()
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	dest := filepath.Join(root, ".punt-labs", "ethos", "missions", "m-2026-11-04-001", "contract.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o700))
	require.NoError(t, os.WriteFile(dest, []byte("mission_id: m-2026-11-04-001\n"), 0o600))

	real := syncDir
	t.Cleanup(func() { syncDir = real })
	syncDir = func(string) error { return errors.New("injected sync failure") }

	durability, err := ReplaceRecordFile(p, dest, []byte("mission_id: m-2026-11-04-001\ncontext: x\n"))
	require.NoError(t, err, "the rename installed the bytes")
	assert.ErrorContains(t, durability, "injected sync failure")
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Contains(t, string(got), "context: x")
}

// M2: the upgrade reports durability warnings in its outcome and prints nothing.
func TestUpgradeLegacyAuthority_WarningsGoToTheOutcomeNotStderr(t *testing.T) {
	s, c, _ := legacyReflectionReader(t, true, 1)
	real := syncDir
	t.Cleanup(func() { syncDir = real })
	syncDir = func(string) error { return errors.New("injected sync failure") }

	var upgraded bool
	var out MutationOutcome
	var err error
	stderr := captureStderr(t, func() { upgraded, out, err = s.UpgradeLegacyAuthority(c.MissionID) })
	require.NoError(t, err)
	assert.True(t, upgraded)
	assert.Empty(t, stderr)
	require.NotEmpty(t, out.Warnings)
	assert.Contains(t, out.Warnings[0], "injected sync failure")
}

// A fence names its kind, so a caller derives a reason without parsing text.
func TestUpgradeLegacyAuthority_FenceKinds(t *testing.T) {
	t.Run("incomplete history", func(t *testing.T) {
		s, c, root := legacyReflectionReader(t, true, 2) // round 1 crossed, no reflection retained
		_, _, err := s.UpgradeLegacyAuthority(c.MissionID)
		var h *LegacyHistoryError
		require.ErrorAs(t, err, &h)
		assert.Equal(t, LegacyIncomplete, h.Kind)
		assert.NoFileExists(t, filepath.Join(root, ".punt-labs", "ethos", "missions", c.MissionID, "corrections.yaml"))
	})
	t.Run("corrupt corrections", func(t *testing.T) {
		s, c, root := legacyReflectionReader(t, true, 1)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos", "missions", c.MissionID, "corrections.yaml"), []byte("version: 7\n"), 0o600))
		_, _, err := s.UpgradeLegacyAuthority(c.MissionID)
		var h *LegacyHistoryError
		require.ErrorAs(t, err, &h)
		assert.Equal(t, LegacyCorruptCorrections, h.Kind)
	})
	// A credential in a reflection's author would be copied into the shared
	// decision; redaction would change that frozen value, so the import refuses.
	t.Run("redaction refused", func(t *testing.T) {
		s, c, root := legacyReflectionReader(t, true, 2)
		b := "reflections:\n    - mission: " + c.MissionID + "\n      round: 1\n      created_at: \"2026-11-05T10:00:00Z\"\n" +
			"      author: ghp_abcdefghijklmnopqrstuvwxyz0123456789abcd\n      converging: true\n      signals:\n        - ok\n" +
			"      recommendation: continue\n      reason: r\n"
		require.NoError(t, os.WriteFile(mustReflectionsPath(t, s, c.MissionID), []byte(b), 0o600))
		_, _, err := s.UpgradeLegacyAuthority(c.MissionID)
		var h *LegacyHistoryError
		require.ErrorAs(t, err, &h)
		assert.Equal(t, LegacyRedactionRefused, h.Kind)
		assert.NoFileExists(t, filepath.Join(root, ".punt-labs", "ethos", "missions", c.MissionID, "corrections.yaml"))
	})
	t.Run("unloadable contract", func(t *testing.T) {
		s, c, root := legacyReflectionReader(t, true, 1)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos", "missions", c.MissionID, "contract.yaml"), []byte("broken: [yaml"), 0o600))
		_, _, err := s.UpgradeLegacyAuthority(c.MissionID)
		var h *LegacyHistoryError
		require.ErrorAs(t, err, &h)
		assert.Equal(t, LegacyUnloadable, h.Kind)
	})
}

// A fenced mission refuses lifecycle writes: Correct on a closed mission whose
// crossed-round history is incomplete installs nothing.
func TestCorrect_RefusesAFencedMission(t *testing.T) {
	s, c, root := legacyReflectionReader(t, true, 2)
	dir := filepath.Join(root, ".punt-labs", "ethos", "missions", c.MissionID)
	contract := filepath.Join(dir, "contract.yaml")
	data, err := os.ReadFile(contract)
	require.NoError(t, err)
	closed := strings.Replace(string(data), "status: open", "status: closed\nclosed_at: \"2026-11-05T10:00:00Z\"", 1)
	require.NotEqual(t, string(data), closed)
	require.NoError(t, os.WriteFile(contract, []byte(closed), 0o600))

	err = s.Correct(c.MissionID, Correction{Mission: c.MissionID, Kind: CorrectionDecision, Author: "claude", Corrected: "decided"})
	require.Error(t, err)
	assert.ErrorContains(t, err, "fenced from lifecycle writes")
	assert.NoFileExists(t, filepath.Join(dir, "corrections.yaml"))
	after, err := os.ReadFile(contract)
	require.NoError(t, err)
	assert.Equal(t, closed, string(after))
}

// A closed report mission has no write_set: the report archetype allows
// that. On a host whose repository and global trees never seeded the
// archetype, the contract still loads by the rules its type carried when
// it was created, so the storage upgrade does not fence it on every run.
func TestLoadReportContractWithoutWriteSetOnAHostWithoutTheArchetype(t *testing.T) {
	root := t.TempDir()
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	s := NewRepositoryStore(p, t.TempDir()) // no archetypes dir in either tree
	c := newContract("m-2026-09-06-009")
	c.Type = "report"
	c.WriteSet = nil
	c.Status = StatusClosed
	c.ClosedAt = "2026-09-06T12:00:00Z"
	data, err := yaml.Marshal(c)
	require.NoError(t, err)
	path := RepoStatePath(root, "missions", c.MissionID, "contract.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o600))

	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err, "a report contract with no write_set loads")
	assert.Equal(t, "report", loaded.Type)
	assert.Empty(t, loaded.WriteSet)
}
