//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// phase3RepoID is the identity given to a test repo's origin so the
// identity-scoped purge and vacuum logic matches the rosters and tombstones
// these tests create (a roster's Repo is an identity, never a checkout path).
const phase3RepoID = "punt-labs/ethos"

// addOriginRemote points repo's origin at an org/name so audit.RepoIdentity
// derives id from the checkout.
func addOriginRemote(t *testing.T, repo, id string) {
	t.Helper()
	cmd := testhelpers.GitCommand("-C", repo, "remote", "add", "origin", "git@github.com:"+id+".git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add origin: %v: %s", err, out)
	}
}

// resetPhase3Flags zeroes package-level flag vars used by session
// commands so tests do not leak state.
func resetPhase3Flags(t *testing.T) {
	t.Helper()
	sessionJoinAgentID = ""
	sessionJoinPersona = ""
	sessionJoinParent = ""
	sessionJoinAgentType = ""
	sessionJoinSession = ""
	sessionLeaveAgentID = ""
	sessionLeaveSession = ""
	sessionIamSession = ""
	t.Cleanup(func() {
		sessionJoinAgentID = ""
		sessionJoinPersona = ""
		sessionJoinParent = ""
		sessionJoinAgentType = ""
		sessionJoinSession = ""
		sessionLeaveAgentID = ""
		sessionLeaveSession = ""
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

func TestRunSessionList(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	createRoster(t, "sess-002", "r", "p")

	stdout, _, err := execHandler(t, "session", "list")
	require.NoError(t, err)
	assert.Contains(t, stdout, "sess-002")
}

func TestRunSessionList_JSON(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	createRoster(t, "sess-003", "r", "p")

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

	createRoster(t, "sess-show-001", "root", "primary")

	stdout, _, err := execHandler(t, "session", "show", "sess-show-001")
	require.NoError(t, err)
	assert.Contains(t, stdout, "sess-show-001")
	assert.Contains(t, stdout, "root")
}

func TestRunSessionShowByID_JSON(t *testing.T) {
	setupPhase3Env(t)
	resetPhase3Flags(t)

	createRoster(t, "sess-json-001", "root", "primary")

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

	createRoster(t, "sess-join-001", "root", "primary")

	_, _, err := execHandler(t, "session", "join",
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

	createRoster(t, "sess-leave-001", "root", "primary")

	_, _, err := execHandler(t, "session", "join",
		"--session", "sess-leave-001",
		"--agent-id", "worker-2",
		"--parent", "root")
	require.NoError(t, err)

	_, _, err = execHandler(t, "session", "leave",
		"--session", "sess-leave-001",
		"--agent-id", "worker-2")
	require.NoError(t, err)
}

// TestRunAuditSeal_VacuumRunsAfterSealing pins SFH S3: the vacuum cross-check
// must warn about a lost session even on a commit that actually sealed a
// chunk — not only on a total no-op. A flagged tombstone's warning has to
// repeat at every commit until acked.
func TestRunAuditSeal_VacuumRunsAfterSealing(t *testing.T) {
	se := setupPhase3Env(t)
	resetPhase3Flags(t)
	addOriginRemote(t, se.repo, phase3RepoID)

	// A live audit line for one session so this seal writes a chunk
	// (SessionsSealed > 0) — the case the old no-op gate skipped the vacuum on.
	live := audit.LiveAuditPath(se.repo, "sess-live")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	line := `{"ts":"` + audit.FormatLineTS(100) + `","session":"sess-live","tool":"Read"}` + "\n"
	require.NoError(t, os.WriteFile(live, []byte(line), 0o600))

	// A flagged tombstone for a lost session bound to this repo's identity → the
	// vacuum must warn regardless of whether anything sealed.
	sessionsDir := filepath.Join(se.home, ".punt-labs", "ethos", "sessions")
	require.NoError(t, audit.WriteTombstone(sessionsDir, audit.Tombstone{
		Session: "sess-lost", Repo: phase3RepoID, UnsealedLines: true,
	}))

	_, stderr, err := execHandler(t, "audit", "seal")
	require.NoError(t, err)
	assert.Contains(t, stderr, "sess-lost", "vacuum warning must appear even when a chunk sealed")
	// A stream that never held a line has no tail to have lost, so the
	// no-live-file warning is not printed for it: every commit outside a
	// Claude Code session would otherwise carry that line.
	assert.NotContains(t, stderr, "has no shared live file")
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

// A roster whose repository identity was unresolved is shown as such: `list`
// says "unresolved" where a no-origin checkout says "-", and `show` prints the
// reason.
func TestUnresolvedRosterListShow(t *testing.T) {
	se := setupPhase3Env(t)
	resetPhase3Flags(t)
	ss := sessionStore()
	root := session.Participant{AgentID: "u"}
	primary := session.Participant{AgentID: "9999999", Parent: "u"}
	require.NoError(t, ss.CreateUnresolved("sess-unres-1", root, primary, "git did not answer", se.repo, "h"))
	require.NoError(t, ss.CreateInCheckout("sess-noorigin-1", root, primary, "", se.repo, "h"))

	list, _, err := execHandler(t, "session", "list")
	require.NoError(t, err)
	for _, line := range strings.Split(list, "\n") {
		switch {
		case strings.Contains(line, "sess-unr"):
			assert.Contains(t, line, "unresolved", "the unresolved roster says so: %q", line)
		case strings.Contains(line, "sess-noo"):
			assert.NotContains(t, line, "unresolved", "a no-origin roster is not unresolved: %q", line)
		}
	}

	show, _, err := execHandler(t, "session", "show", "sess-unres-1")
	require.NoError(t, err)
	assert.Contains(t, show, "unresolved")
	assert.Contains(t, show, "git did not answer")
}

// createRoster writes a session roster straight to the store: the session
// create command is gone, so rosters come from the store the hooks use.
func createRoster(t *testing.T, id, root, primary string) {
	t.Helper()
	require.NoError(t, sessionStore().Create(id,
		session.Participant{AgentID: root},
		session.Participant{AgentID: primary, Parent: root}, "", ""))
}
