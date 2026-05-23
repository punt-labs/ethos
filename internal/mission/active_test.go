//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActiveMissionPath(t *testing.T) {
	tests := []struct {
		name    string
		root    string
		session string
		want    string
	}{
		{"empty root", "", "sess-1", ""},
		{"empty session", "/tmp/ethos", "", ""},
		{"both set", "/tmp/ethos", "sess-1", "/tmp/ethos/sessions/sess-1/active-mission"},
		{
			"basepath sanitization",
			"/tmp/ethos",
			"../escape",
			"/tmp/ethos/sessions/escape/active-mission",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ActiveMissionPath(tt.root, tt.session))
		})
	}
}

func TestReadActiveMission_MissingReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	got, err := ReadActiveMission(root, "sess-absent")
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestReadActiveMission_MissingGlobalRootReturnsEmpty(t *testing.T) {
	got, err := ReadActiveMission("", "sess-x")
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestWriteActiveMission_RoundTrip(t *testing.T) {
	root := t.TempDir()
	sess := "sess-roundtrip"
	mid := "m-2026-05-23-001"

	require.NoError(t, WriteActiveMission(root, sess, mid))

	got, err := ReadActiveMission(root, sess)
	require.NoError(t, err)
	assert.Equal(t, mid, got)
}

func TestWriteActiveMission_FileMode0o600(t *testing.T) {
	root := t.TempDir()
	sess := "sess-mode"
	require.NoError(t, WriteActiveMission(root, sess, "m-x"))

	info, err := os.Stat(ActiveMissionPath(root, sess))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"sidecar file mode must be 0o600")
}

func TestWriteActiveMission_DirMode0o700(t *testing.T) {
	root := t.TempDir()
	sess := "sess-dirmode"
	require.NoError(t, WriteActiveMission(root, sess, "m-x"))

	info, err := os.Stat(filepath.Join(root, "sessions", sess))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
		"per-session directory mode must be 0o700")
}

func TestWriteActiveMission_OverwriteIsAtomic(t *testing.T) {
	root := t.TempDir()
	sess := "sess-overwrite"

	require.NoError(t, WriteActiveMission(root, sess, "m-old"))
	require.NoError(t, WriteActiveMission(root, sess, "m-new"))

	got, err := ReadActiveMission(root, sess)
	require.NoError(t, err)
	assert.Equal(t, "m-new", got, "second write must replace the first")

	// No leftover .tmp files from the rename pattern.
	dir := filepath.Join(root, "sessions", sess)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp",
			"atomic write must clean up its temp file: %s", e.Name())
	}
}

func TestWriteActiveMission_RejectsEmptySessionID(t *testing.T) {
	root := t.TempDir()
	err := WriteActiveMission(root, "", "m-x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "globalRoot and sessionID are required")
}

func TestWriteActiveMission_RejectsEmptyMissionID(t *testing.T) {
	root := t.TempDir()
	err := WriteActiveMission(root, "sess-x", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missionID is required")
}

func TestWriteActiveMission_RejectsEmptyGlobalRoot(t *testing.T) {
	err := WriteActiveMission("", "sess-x", "m-x")
	require.Error(t, err)
}

func TestWriteActiveMission_RenameFailureLeavesNoPartial(t *testing.T) {
	// A pre-existing directory at the destination path causes os.Rename
	// to fail on every platform. The sidecar must not leave the temp
	// file behind after the failure — the cleanup path covers it.
	root := t.TempDir()
	sess := "sess-rename-fail"
	dest := ActiveMissionPath(root, sess)
	require.NoError(t, os.MkdirAll(dest, 0o700))

	err := WriteActiveMission(root, sess, "m-blocked")
	require.Error(t, err, "rename onto a directory must fail")

	// Original dir is still there; no .tmp leftovers.
	dir := filepath.Dir(dest)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp",
			"failed write must clean up its temp file: %s", e.Name())
	}
}

func TestReadActiveMission_MalformedReturnsRaw(t *testing.T) {
	// The helper trims surrounding whitespace but does not validate the
	// missionID shape — that is the caller's job. A stray newline or a
	// garbled value comes back as-is so the caller can produce its own
	// "malformed MISSION_ID" diagnostic.
	root := t.TempDir()
	sess := "sess-malformed"

	path := ActiveMissionPath(root, sess)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("  not-a-real-id  \n"), 0o600))

	got, err := ReadActiveMission(root, sess)
	require.NoError(t, err)
	assert.Equal(t, "not-a-real-id", got,
		"reader returns the trimmed raw content for the operator to validate")
}

func TestClearActiveMission_RemovesFile(t *testing.T) {
	root := t.TempDir()
	sess := "sess-clear"
	require.NoError(t, WriteActiveMission(root, sess, "m-x"))

	require.NoError(t, ClearActiveMission(root, sess))

	got, err := ReadActiveMission(root, sess)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestClearActiveMission_MissingIsNotAnError(t *testing.T) {
	root := t.TempDir()
	// Never wrote anything — clear must be a no-op.
	require.NoError(t, ClearActiveMission(root, "sess-never-existed"))
}

func TestClearActiveMission_EmptyArgsAreNoOp(t *testing.T) {
	require.NoError(t, ClearActiveMission("", "sess-x"))
	require.NoError(t, ClearActiveMission("/tmp/ethos", ""))
}
