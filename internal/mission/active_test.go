//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"strings"
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

// TestActiveMissionBinding_Origin covers the bind origin the
// ethos-7vo3 ruling introduced: a claim binding produces commit
// trailers, a dispatch binding files delegations but must not. Both
// forms round-trip, and ReadActiveMission still answers with the
// mission alone.
func TestActiveMissionBinding_Origin(t *testing.T) {
	tests := []struct {
		name   string
		write  func(root, sess, mid string) error
		origin string
	}{
		{
			name:   "claim is the default form",
			write:  WriteActiveMission,
			origin: BindOriginClaim,
		},
		{
			name: "dispatch is explicit",
			write: func(root, sess, mid string) error {
				return WriteActiveMissionOrigin(root, sess, mid, BindOriginDispatch)
			},
			origin: BindOriginDispatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			sess := "sess-origin"
			mid := "m-2026-07-31-004"
			require.NoError(t, tt.write(root, sess, mid))

			b, err := ReadActiveMissionBinding(root, sess)
			require.NoError(t, err)
			assert.Equal(t, mid, b.MissionID)
			assert.Equal(t, tt.origin, b.Origin)

			got, err := ReadActiveMission(root, sess)
			require.NoError(t, err)
			assert.Equal(t, mid, got, "the mission-only read must not see the origin line")
		})
	}
}

// TestReadActiveMissionBinding_LegacySidecarReadsAsClaim pins the
// compatibility rule for sidecars written before the origin line
// existed. Every one of them came from `ethos mission claim`, so a
// bare mission ID reads as a claim — an installed session mid-upgrade
// keeps its commit trailers.
func TestReadActiveMissionBinding_LegacySidecarReadsAsClaim(t *testing.T) {
	root := t.TempDir()
	sess := "sess-legacy"
	path := ActiveMissionPath(root, sess)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("m-2026-07-31-004\n"), 0o600))

	b, err := ReadActiveMissionBinding(root, sess)
	require.NoError(t, err)
	assert.Equal(t, "m-2026-07-31-004", b.MissionID)
	assert.Equal(t, BindOriginClaim, b.Origin)
}

// TestReadActiveMissionBinding_UnknownOriginIsPreserved asserts an
// origin this build does not know is returned as written rather than
// silently rewritten to claim. Only BindOriginClaim turns trailers on,
// so an unknown value withholds them — the safe direction.
func TestReadActiveMissionBinding_UnknownOriginIsPreserved(t *testing.T) {
	root := t.TempDir()
	sess := "sess-unknown-origin"
	mid := "m-2026-07-31-004"
	require.NoError(t, WriteActiveMission(root, sess, mid))
	writeOriginFile(t, root, sess, "future-origin", mid)

	b, err := ReadActiveMissionBinding(root, sess)
	require.NoError(t, err)
	assert.Equal(t, "future-origin", b.Origin)
	assert.NotEqual(t, BindOriginClaim, b.Origin)
}

// TestActiveMissionSidecar_StaysOneLine is the regression rsc found on
// PR #415: the origin must NOT go into active-mission. Every reader
// that predates it does TrimSpace over the whole file, so a second
// line turns the mission ID into "m-...\nclaim" and the older binary
// denies every Agent spawn. Our own workflow triggers it — agents run
// a CLI built to .tmp while Claude Code hooks invoke the installed
// binary — so one claim through a new build would poison the session
// for the old one.
func TestActiveMissionSidecar_StaysOneLine(t *testing.T) {
	tests := []struct {
		name  string
		write func(root, sess, mid string) error
	}{
		{name: "claim", write: WriteActiveMission},
		{
			name: "dispatch",
			write: func(root, sess, mid string) error {
				return WriteActiveMissionOrigin(root, sess, mid, BindOriginDispatch)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			sess := "sess-one-line"
			mid := "m-2026-07-31-004"
			require.NoError(t, tt.write(root, sess, mid))

			data, err := os.ReadFile(ActiveMissionPath(root, sess))
			require.NoError(t, err)
			assert.Equal(t, mid+"\n", string(data),
				"active-mission must hold the mission ID and nothing else")

			// What an older reader does with the file: TrimSpace over
			// the whole thing. It must yield a usable mission ID.
			assert.Equal(t, mid, strings.TrimSpace(string(data)),
				"an older binary must read a clean mission ID")
		})
	}
}

// TestReadActiveMissionBinding_StaleOriginIgnored asserts the two files
// are self-checking: an origin sidecar naming a different mission is
// left over from an earlier binding and must not label the current one.
func TestReadActiveMissionBinding_StaleOriginIgnored(t *testing.T) {
	root := t.TempDir()
	sess := "sess-stale-origin"
	require.NoError(t, WriteActiveMission(root, sess, "m-2026-07-31-004"))
	writeOriginFile(t, root, sess, BindOriginDispatch, "m-2026-07-31-001")

	b, err := ReadActiveMissionBinding(root, sess)
	require.NoError(t, err)
	assert.Equal(t, "m-2026-07-31-004", b.MissionID)
	assert.Equal(t, BindOriginClaim, b.Origin,
		"an origin naming another mission must be ignored")
}

// TestWriteActiveMission_ClaimClearsDispatchOrigin asserts a claim over
// a dispatch binding removes the origin file, so the operator's
// commits carry trailers again.
func TestWriteActiveMission_ClaimClearsDispatchOrigin(t *testing.T) {
	root := t.TempDir()
	sess := "sess-claim-over-dispatch"
	mid := "m-2026-07-31-004"
	require.NoError(t, WriteActiveMissionOrigin(root, sess, mid, BindOriginDispatch))
	require.NoError(t, WriteActiveMission(root, sess, mid))

	_, statErr := os.Stat(ActiveMissionOriginPath(root, sess))
	assert.True(t, os.IsNotExist(statErr), "a claim must leave no origin file: %v", statErr)

	b, err := ReadActiveMissionBinding(root, sess)
	require.NoError(t, err)
	assert.Equal(t, BindOriginClaim, b.Origin)
}

// TestClearActiveMission_RemovesOriginToo asserts the pair is cleared
// together — an origin file outliving its binding would label whatever
// the session binds next.
func TestClearActiveMission_RemovesOriginToo(t *testing.T) {
	root := t.TempDir()
	sess := "sess-clear-both"
	require.NoError(t, WriteActiveMissionOrigin(root, sess, "m-2026-07-31-004", BindOriginDispatch))
	require.NoError(t, ClearActiveMission(root, sess))

	_, statErr := os.Stat(ActiveMissionPath(root, sess))
	assert.True(t, os.IsNotExist(statErr), "active-mission must be gone: %v", statErr)
	_, statErr = os.Stat(ActiveMissionOriginPath(root, sess))
	assert.True(t, os.IsNotExist(statErr), "active-mission-origin must be gone: %v", statErr)
}

// writeOriginFile stages the origin sidecar directly, for the cases
// that need a shape the writers do not produce.
func writeOriginFile(t *testing.T, root, sess, origin, missionID string) {
	t.Helper()
	path := ActiveMissionOriginPath(root, sess)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(origin+"\n"+missionID+"\n"), 0o600))
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

func TestClearDelegationBinding_RemovesFile(t *testing.T) {
	root := t.TempDir()
	sess := "sess-deleg"
	require.NoError(t, WriteDelegationBinding(root, sess, DelegationBinding{
		DelegationID:  "d-1",
		MissionID:     "m-2026-07-29-001",
		ParentSession: sess,
	}))

	require.NoError(t, ClearDelegationBinding(root, sess))

	b, err := ReadDelegationBinding(root, sess)
	require.NoError(t, err)
	assert.Equal(t, DelegationBinding{}, b)
}

func TestClearDelegationBinding_MissingIsNotAnError(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, ClearDelegationBinding(root, "sess-never-existed"))
}

func TestClearDelegationBinding_EmptyArgsAreNoOp(t *testing.T) {
	require.NoError(t, ClearDelegationBinding("", "sess-x"))
	require.NoError(t, ClearDelegationBinding("/tmp/ethos", ""))
}

// ClearMissionBindings is the one clear both close surfaces call — the
// CLI's `mission close` and the MCP close method. The tests below pin
// what it clears, what it leaves alone, and what it reports.

const clearTestMission = "m-2026-07-29-001"

func TestClearMissionBindings_ClearsBothSidecars(t *testing.T) {
	root := t.TempDir()
	sess := "sess-both"
	require.NoError(t, WriteActiveMission(root, sess, clearTestMission))
	require.NoError(t, WriteDelegationBinding(root, sess, DelegationBinding{
		DelegationID:  "d-1",
		MissionID:     clearTestMission,
		ParentSession: sess,
	}))

	require.NoError(t, ClearMissionBindings(root, sess, clearTestMission))

	active, err := ReadActiveMission(root, sess)
	require.NoError(t, err)
	assert.Equal(t, "", active)
	b, err := ReadDelegationBinding(root, sess)
	require.NoError(t, err)
	assert.Equal(t, DelegationBinding{}, b)
}

// TestClearMissionBindings_ScopedToMission is the guard that keeps a
// closing mission from releasing another mission's claim.
func TestClearMissionBindings_ScopedToMission(t *testing.T) {
	root := t.TempDir()
	sess := "sess-scoped"
	const other = "m-2026-07-29-002"
	require.NoError(t, WriteActiveMission(root, sess, other))
	require.NoError(t, WriteDelegationBinding(root, sess, DelegationBinding{
		DelegationID:  "d-1",
		MissionID:     other,
		ParentSession: sess,
	}))

	require.NoError(t, ClearMissionBindings(root, sess, clearTestMission))

	active, err := ReadActiveMission(root, sess)
	require.NoError(t, err)
	assert.Equal(t, other, active, "another mission's claim must survive")
	b, err := ReadDelegationBinding(root, sess)
	require.NoError(t, err)
	assert.Equal(t, other, b.MissionID, "another mission's binding must survive")
}

func TestClearMissionBindings_MissingSidecarsAreSilent(t *testing.T) {
	root := t.TempDir()
	assert.NoError(t, ClearMissionBindings(root, "sess-empty", clearTestMission),
		"nothing to clear is the ordinary state, not a failure")
}

func TestClearMissionBindings_EmptyArgsAreNoOp(t *testing.T) {
	root := t.TempDir()
	assert.NoError(t, ClearMissionBindings("", "sess-x", clearTestMission))
	assert.NoError(t, ClearMissionBindings(root, "", clearTestMission))
	assert.NoError(t, ClearMissionBindings(root, "sess-x", ""))
}

// TestReadActiveMissionBinding_UnreadableOriginIsAnError asserts the
// one shape that does NOT fall back to claim: an origin file that
// exists but will not read. Absence is a state this design assigns a
// meaning to; an I/O failure on a file that is there means the binding
// is unknown, and answering "claim" would invent one.
func TestReadActiveMissionBinding_UnreadableOriginIsAnError(t *testing.T) {
	root := t.TempDir()
	sess := "sess-unreadable-origin"
	require.NoError(t, WriteActiveMission(root, sess, "m-2026-07-31-004"))
	// A directory where the sidecar belongs makes os.ReadFile fail with
	// EISDIR — a real error, portably distinct from "no sidecar".
	require.NoError(t, os.MkdirAll(ActiveMissionOriginPath(root, sess), 0o700))

	_, err := ReadActiveMissionBinding(root, sess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "active-mission-origin")
}

// TestClearMissionBindings_ReportsReadFailures asserts that a real read
// failure is reported rather than swallowed, and that a failure on one
// sidecar does not stop work on the other — they are independent, and a
// sidecar left in place keeps the commit-msg trailer gate open on a
// closed mission (ethos-jawp).
func TestClearMissionBindings_ReportsReadFailures(t *testing.T) {
	root := t.TempDir()
	sess := "sess-unreadable"

	// A directory where each sidecar belongs makes os.ReadFile fail with
	// EISDIR — a real error, portably distinct from "no sidecar".
	require.NoError(t, os.MkdirAll(ActiveMissionPath(root, sess), 0o700))
	require.NoError(t, os.MkdirAll(DelegationBindingPath(root, sess), 0o700))

	err := ClearMissionBindings(root, sess, clearTestMission)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading active mission")
	assert.Contains(t, err.Error(), "reading delegation binding",
		"a failure on one sidecar must not skip the other")
}

// TestClearMissionBindings_ReportsClearFailure asserts a failed removal
// is reported too. A read-only parent directory blocks the unlink while
// leaving the file itself readable.
func TestClearMissionBindings_ReportsClearFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	root := t.TempDir()
	sess := "sess-locked"
	require.NoError(t, WriteActiveMission(root, sess, clearTestMission))

	dir := filepath.Dir(ActiveMissionPath(root, sess))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := ClearMissionBindings(root, sess, clearTestMission)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clearing active mission")
}
