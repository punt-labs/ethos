//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetPhase3Flags zeroes package-level flag vars used by session
// commands so tests do not leak state.
func resetPhase3Flags(t *testing.T) {
	t.Helper()
	sessionCreateSession = ""
	sessionCreateRootID = ""
	sessionCreateRootPersona = ""
	sessionCreatePrimaryID = ""
	sessionCreatePrimaryPersona = ""
	sessionDeleteSession = ""
	sessionJoinAgentID = ""
	sessionJoinPersona = ""
	sessionJoinParent = ""
	sessionJoinAgentType = ""
	sessionJoinSession = ""
	sessionLeaveAgentID = ""
	sessionLeaveSession = ""
	sessionWriteCurrentPID = ""
	sessionWriteCurrentSession = ""
	sessionDeleteCurrentPID = ""
	sessionIamSession = ""
	t.Cleanup(func() {
		sessionCreateSession = ""
		sessionCreateRootID = ""
		sessionCreateRootPersona = ""
		sessionCreatePrimaryID = ""
		sessionCreatePrimaryPersona = ""
		sessionDeleteSession = ""
		sessionJoinAgentID = ""
		sessionJoinPersona = ""
		sessionJoinParent = ""
		sessionJoinAgentType = ""
		sessionJoinSession = ""
		sessionLeaveAgentID = ""
		sessionLeaveSession = ""
		sessionWriteCurrentPID = ""
		sessionWriteCurrentSession = ""
		sessionDeleteCurrentPID = ""
		sessionIamSession = ""
	})
}

// setupPhase3Env creates the fixture dirs needed by attribute and
// session tests on top of the standard CLI subprocess env.
func setupPhase3Env(t *testing.T) *cliSubprocessEnv {
	t.Helper()
	se := setupCLISubprocessEnv(t)

	globalEthos := filepath.Join(se.home, ".punt-labs", "ethos")
	for _, d := range []string{"talents", "personalities", "writing-styles"} {
		require.NoError(t, os.MkdirAll(filepath.Join(globalEthos, d), 0o755))
	}

	setInProcessEnv(t, se)
	return se
}

// ---------------------------------------------------------------
// Attribute tests
// ---------------------------------------------------------------

func TestRunAttributeCreate_File(t *testing.T) {
	setupPhase3Env(t)

	content := "# Go\nSystems programming.\n"
	f := filepath.Join(t.TempDir(), "go.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	stdout, _, err := execHandler(t, "talent", "create", "go", "-f", f)
	require.NoError(t, err)
	assert.Contains(t, stdout, `"go"`)
	assert.Contains(t, stdout, "Created")
}

func TestRunAttributeList(t *testing.T) {
	se := setupPhase3Env(t)
	seedAttr(t, se, "talents", "go", "# Go\nSystems.\n")

	stdout, _, err := execHandler(t, "talent", "list")
	require.NoError(t, err)
	assert.Contains(t, stdout, "go")
}

func TestRunAttributeList_JSON(t *testing.T) {
	se := setupPhase3Env(t)
	seedAttr(t, se, "talents", "go", "# Go\nSystems.\n")

	stdout, _, err := execHandler(t, "talent", "list", "--json")
	require.NoError(t, err)
	var attrs []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &attrs))
	require.NotEmpty(t, attrs)
	assert.Equal(t, "go", attrs[0]["slug"])
}

func TestRunAttributeList_Empty(t *testing.T) {
	setupPhase3Env(t)

	stdout, _, err := execHandler(t, "talent", "list")
	require.NoError(t, err)
	assert.Contains(t, stdout, "No talents found")
}

func TestRunAttributeShow(t *testing.T) {
	se := setupPhase3Env(t)
	seedAttr(t, se, "talents", "go", "# Go\nSystems programming.\n")

	stdout, _, err := execHandler(t, "talent", "show", "go")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Systems programming")
}

func TestRunAttributeShow_JSON(t *testing.T) {
	se := setupPhase3Env(t)
	seedAttr(t, se, "talents", "go", "# Go\nSystems.\n")

	stdout, _, err := execHandler(t, "talent", "show", "go", "--json")
	require.NoError(t, err)
	var a map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &a))
	assert.Equal(t, "go", a["slug"])
}

func TestRunAttributeDelete(t *testing.T) {
	se := setupPhase3Env(t)
	seedAttr(t, se, "talents", "go", "# Go\n")

	stdout, _, err := execHandler(t, "talent", "delete", "go")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Deleted")

	_, _, err = execHandler(t, "talent", "show", "go")
	require.Error(t, err)
}

func TestRunAttributeDelete_JSON(t *testing.T) {
	se := setupPhase3Env(t)
	seedAttr(t, se, "talents", "go", "# Go\n")

	stdout, _, err := execHandler(t, "talent", "delete", "go", "--json")
	require.NoError(t, err)
	var d map[string]string
	require.NoError(t, json.Unmarshal([]byte(stdout), &d))
	assert.Equal(t, "go", d["deleted"])
}

func TestRunAttributeAdd(t *testing.T) {
	se := setupPhase3Env(t)
	seedAttr(t, se, "talents", "go", "# Go\n")

	stdout, _, err := execHandler(t, "talent", "add", "test-agent", "go")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Added talent")
	assert.Contains(t, stdout, "test-agent")
}

func TestRunAttributeRemove(t *testing.T) {
	se := setupPhase3Env(t)
	seedAttr(t, se, "talents", "go", "# Go\n")

	// Add first, then remove.
	_, _, err := execHandler(t, "talent", "add", "test-agent", "go")
	require.NoError(t, err)

	stdout, _, err := execHandler(t, "talent", "remove", "test-agent", "go")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Removed talent")
}

func TestRunAttributeSet(t *testing.T) {
	se := setupPhase3Env(t)
	seedAttr(t, se, "personalities", "analytical", "# Analytical\nData-driven.\n")

	stdout, _, err := execHandler(t, "personality", "set", "test-agent", "analytical")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Set personality")
	assert.Contains(t, stdout, "analytical")
}

func TestRunAttributeSet_NotFound(t *testing.T) {
	setupPhase3Env(t)

	_, _, err := execHandler(t, "personality", "set", "test-agent", "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRunAttributeSet_WritingStyle(t *testing.T) {
	se := setupPhase3Env(t)
	seedAttr(t, se, "writing-styles", "terse", "# Terse\nDirect.\n")

	stdout, _, err := execHandler(t, "writing-style", "set", "test-agent", "terse")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Set writing style")
	assert.Contains(t, stdout, "terse")
}

func TestRunPersonalityList(t *testing.T) {
	se := setupPhase3Env(t)
	seedAttr(t, se, "personalities", "analytical", "# Analytical\n")

	stdout, _, err := execHandler(t, "personality", "list")
	require.NoError(t, err)
	assert.Contains(t, stdout, "analytical")
}

func TestRunPersonalityCreate_File(t *testing.T) {
	setupPhase3Env(t)

	content := "# Calm\nMeasured responses.\n"
	f := filepath.Join(t.TempDir(), "calm.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	stdout, _, err := execHandler(t, "personality", "create", "calm", "-f", f)
	require.NoError(t, err)
	assert.Contains(t, stdout, "Created")
	assert.Contains(t, stdout, `"calm"`)
}

// ---------------------------------------------------------------
// Session tests
// ---------------------------------------------------------------

func TestRunSessionCreate(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	_, _, err := execHandler(t, "session", "create",
		"--session", "sess-001",
		"--root-id", "root-agent",
		"--primary-id", "primary-agent")
	require.NoError(t, err)
}

func TestRunSessionList(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	_, _, err := execHandler(t, "session", "create",
		"--session", "sess-002",
		"--root-id", "r", "--primary-id", "p")
	require.NoError(t, err)

	stdout, _, err := execHandler(t, "session", "list")
	require.NoError(t, err)
	assert.Contains(t, stdout, "sess-002")
}

func TestRunSessionList_JSON(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	_, _, err := execHandler(t, "session", "create",
		"--session", "sess-003",
		"--root-id", "r", "--primary-id", "p")
	require.NoError(t, err)

	stdout, _, err := execHandler(t, "session", "list", "--json")
	require.NoError(t, err)
	var entries []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &entries))
	require.NotEmpty(t, entries)
}

func TestRunSessionList_Empty(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	stdout, _, err := execHandler(t, "session", "list")
	require.NoError(t, err)
	assert.Contains(t, stdout, "No sessions found")
}

func TestRunSessionShowByID(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	_, _, err := execHandler(t, "session", "create",
		"--session", "sess-show-001",
		"--root-id", "root", "--primary-id", "primary")
	require.NoError(t, err)

	stdout, _, err := execHandler(t, "session", "show", "sess-show-001")
	require.NoError(t, err)
	assert.Contains(t, stdout, "sess-show-001")
	assert.Contains(t, stdout, "root")
}

func TestRunSessionShowByID_JSON(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	_, _, err := execHandler(t, "session", "create",
		"--session", "sess-json-001",
		"--root-id", "root", "--primary-id", "primary")
	require.NoError(t, err)

	stdout, _, err := execHandler(t, "session", "show", "sess-json-001", "--json")
	require.NoError(t, err)
	var roster map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &roster))
	assert.Equal(t, "sess-json-001", roster["session"])
}

func TestRunSessionShow_NoSession(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)
	t.Setenv("ETHOS_SESSION", "")

	stdout, _, err := execHandler(t, "session")
	require.NoError(t, err)
	assert.Contains(t, stdout, "No active session")
}

func TestRunSessionJoin(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	_, _, err := execHandler(t, "session", "create",
		"--session", "sess-join-001",
		"--root-id", "root", "--primary-id", "primary")
	require.NoError(t, err)

	_, _, err = execHandler(t, "session", "join",
		"--session", "sess-join-001",
		"--agent-id", "worker-1",
		"--parent", "root")
	require.NoError(t, err)

	// Verify the participant is in the roster.
	stdout, _, err := execHandler(t, "session", "show", "sess-join-001")
	require.NoError(t, err)
	assert.Contains(t, stdout, "worker-1")
}

func TestRunSessionLeave(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	_, _, err := execHandler(t, "session", "create",
		"--session", "sess-leave-001",
		"--root-id", "root", "--primary-id", "primary")
	require.NoError(t, err)

	_, _, err = execHandler(t, "session", "join",
		"--session", "sess-leave-001",
		"--agent-id", "worker-2",
		"--parent", "root")
	require.NoError(t, err)

	_, _, err = execHandler(t, "session", "leave",
		"--session", "sess-leave-001",
		"--agent-id", "worker-2")
	require.NoError(t, err)
}

func TestRunSessionDelete(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	_, _, err := execHandler(t, "session", "create",
		"--session", "sess-del-001",
		"--root-id", "root", "--primary-id", "primary")
	require.NoError(t, err)

	_, _, err = execHandler(t, "session", "delete", "--session", "sess-del-001")
	require.NoError(t, err)

	// Show after delete should fail.
	_, _, err = execHandler(t, "session", "show", "sess-del-001")
	require.Error(t, err)
}

func TestRunSessionPurge(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	stdout, _, err := execHandler(t, "session", "purge")
	require.NoError(t, err)
	assert.Contains(t, stdout, "No stale sessions found")
}

func TestRunSessionWriteDeleteCurrent(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	// Create a session first so the mapping is valid.
	_, _, err := execHandler(t, "session", "create",
		"--session", "sess-cur-001",
		"--root-id", "root", "--primary-id", "primary")
	require.NoError(t, err)

	// Write PID mapping.
	_, _, err = execHandler(t, "session", "write-current",
		"--pid", "99999",
		"--session", "sess-cur-001")
	require.NoError(t, err)

	// Delete PID mapping.
	_, _, err = execHandler(t, "session", "delete-current", "--pid", "99999")
	require.NoError(t, err)
}

// ---------------------------------------------------------------
// helpers
// ---------------------------------------------------------------

// seedAttr writes a markdown attribute file into the global store.
func seedAttr(t *testing.T, se *cliSubprocessEnv, kind, slug, content string) {
	t.Helper()
	dir := filepath.Join(se.home, ".punt-labs", "ethos", kind)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, slug+".md"), []byte(content), 0o644))
}
