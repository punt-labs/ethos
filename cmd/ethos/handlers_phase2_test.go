//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// resetPhase2Flags zeroes package-level flag vars used by role, team,
// ext, and adr commands so tests do not leak state.
func resetPhase2Flags(t *testing.T) {
	t.Helper()
	roleCreateFile = ""
	teamCreateFile = ""
	adrCreateTitle = ""
	adrCreateContext = ""
	adrCreateDecision = ""
	adrCreateStatus = "proposed"
	adrCreateAuthor = ""
	adrCreateMissionID = ""
	adrCreateBeadID = ""
	adrListStatus = "all"
	t.Cleanup(func() {
		roleCreateFile = ""
		teamCreateFile = ""
		adrCreateTitle = ""
		adrCreateContext = ""
		adrCreateDecision = ""
		adrCreateStatus = "proposed"
		adrCreateAuthor = ""
		adrCreateMissionID = ""
		adrCreateBeadID = ""
		adrListStatus = "all"
	})
}

// setupPhase2Env creates the fixture dirs needed by role, team, ext,
// and adr tests on top of the standard CLI subprocess env.
func setupPhase2Env(t *testing.T) *cliSubprocessEnv {
	t.Helper()
	se := setupCLISubprocessEnv(t)

	globalEthos := filepath.Join(se.home, ".punt-labs", "ethos")
	for _, d := range []string{"roles", "teams", "adrs"} {
		require.NoError(t, os.MkdirAll(filepath.Join(globalEthos, d), 0o755))
	}

	setInProcessEnv(t, se)
	return se
}

// --- role tests ---

func TestRunRoleCreate(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	// Create via --file flag.
	roleFile := filepath.Join(t.TempDir(), "dev.yaml")
	data, merr := yaml.Marshal(map[string]interface{}{
		"name":             "dev",
		"responsibilities": []string{"write code"},
		"permissions":      []string{"read"},
	})
	require.NoError(t, merr)
	require.NoError(t, os.WriteFile(roleFile, data, 0o644))

	stdout, _, err := execHandler(t, "role", "create", "dev", "-f", roleFile)
	require.NoError(t, err)
	assert.Contains(t, stdout, `"dev"`)
}

func TestRunRoleList(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	// Seed a role.
	seedRole(t, se, "reviewer")

	stdout, _, err := execHandler(t, "role", "list")
	require.NoError(t, err)
	assert.Contains(t, stdout, "reviewer")
}

func TestRunRoleShow(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	seedRole(t, se, "reviewer")

	stdout, _, err := execHandler(t, "role", "show", "reviewer")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Name: reviewer")
}

func TestRunRoleDelete(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	seedRole(t, se, "temp-role")

	stdout, _, err := execHandler(t, "role", "delete", "temp-role")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Deleted")

	// Show after delete should fail.
	_, _, err = execHandler(t, "role", "show", "temp-role")
	require.Error(t, err)
}

func TestRunRoleList_JSON(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	seedRole(t, se, "lead")

	stdout, _, err := execHandler(t, "role", "list", "--json")
	require.NoError(t, err)
	var names []string
	require.NoError(t, json.Unmarshal([]byte(stdout), &names))
	assert.Contains(t, names, "lead")
}

// --- team tests ---

func TestRunTeamCreate(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	// Need identity + role for validation.
	seedRole(t, se, "eng")

	teamFile := filepath.Join(t.TempDir(), "alpha.yaml")
	data, merr := yaml.Marshal(map[string]interface{}{
		"name":    "alpha",
		"members": []map[string]string{{"identity": "test-agent", "role": "eng"}},
	})
	require.NoError(t, merr)
	require.NoError(t, os.WriteFile(teamFile, data, 0o644))

	stdout, _, err := execHandler(t, "team", "create", "alpha", "-f", teamFile)
	require.NoError(t, err)
	assert.Contains(t, stdout, `"alpha"`)
}

func TestRunTeamList(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	seedRole(t, se, "eng")
	seedTeam(t, se, "bravo", "test-agent", "eng")

	stdout, _, err := execHandler(t, "team", "list")
	require.NoError(t, err)
	assert.Contains(t, stdout, "bravo")
}

func TestRunTeamShow(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	seedRole(t, se, "eng")
	seedTeam(t, se, "charlie", "test-agent", "eng")

	stdout, _, err := execHandler(t, "team", "show", "charlie")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Name: charlie")
	assert.Contains(t, stdout, "test-agent (eng)")
}

func TestRunTeamDelete(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	seedRole(t, se, "eng")
	seedTeam(t, se, "delta", "test-agent", "eng")

	stdout, _, err := execHandler(t, "team", "delete", "delta")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Deleted")

	_, _, err = execHandler(t, "team", "show", "delta")
	require.Error(t, err)
}

func TestRunTeamList_JSON(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	seedRole(t, se, "eng")
	seedTeam(t, se, "echo", "test-agent", "eng")

	stdout, _, err := execHandler(t, "team", "list", "--json")
	require.NoError(t, err)
	var names []string
	require.NoError(t, json.Unmarshal([]byte(stdout), &names))
	assert.Contains(t, names, "echo")
}

func TestRunTeamAddMember(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	seedRole(t, se, "eng")
	seedRole(t, se, "lead")
	seedTeam(t, se, "foxtrot", "test-agent", "eng")

	// Add a second identity so add-member's identity check passes.
	seedIdentity(t, se, "other-agent")

	stdout, _, err := execHandler(t, "team", "add-member", "foxtrot", "other-agent", "lead")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Added other-agent (lead)")
}

func TestRunTeamRemoveMember(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	seedRole(t, se, "eng")
	seedRole(t, se, "lead")
	// Team with two members so removing one still leaves a valid team.
	seedIdentity(t, se, "other-agent")
	seedTeamTwoMembers(t, se, "golf", "test-agent", "eng", "other-agent", "lead")

	stdout, _, err := execHandler(t, "team", "remove-member", "golf", "other-agent", "lead")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Removed other-agent (lead)")
}

// --- ext tests ---

func TestRunExtSet(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	_, _, err := execHandler(t, "ext", "set", "test-agent", "vox", "voice", "nova")
	require.NoError(t, err)
}

func TestRunExtGet(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	_, _, err := execHandler(t, "ext", "set", "test-agent", "vox", "voice", "nova")
	require.NoError(t, err)

	stdout, _, err := execHandler(t, "ext", "get", "test-agent", "vox", "voice")
	require.NoError(t, err)
	assert.Contains(t, stdout, "nova")
}

func TestRunExtList(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	_, _, err := execHandler(t, "ext", "set", "test-agent", "vox", "voice", "nova")
	require.NoError(t, err)

	stdout, _, err := execHandler(t, "ext", "list", "test-agent")
	require.NoError(t, err)
	assert.Contains(t, stdout, "vox")
}

func TestRunExtDel(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	_, _, err := execHandler(t, "ext", "set", "test-agent", "vox", "voice", "nova")
	require.NoError(t, err)

	_, _, err = execHandler(t, "ext", "del", "test-agent", "vox", "voice")
	require.NoError(t, err)

	// Get after delete should fail.
	_, _, err = execHandler(t, "ext", "get", "test-agent", "vox", "voice")
	require.Error(t, err)
}

func TestRunExtGet_NotFound(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	_, _, err := execHandler(t, "ext", "get", "test-agent", "nonexistent", "key")
	require.Error(t, err)
}

// --- adr tests ---

func TestRunADRCreate(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	stdout, _, err := execHandler(t, "adr", "create",
		"--title", "Use RunE",
		"--decision", "Convert all handlers to RunE",
		"--context", "Testability")
	require.NoError(t, err)
	assert.Contains(t, stdout, "DES-001")
	assert.Contains(t, stdout, "Use RunE")
}

func TestRunADRList(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	_, _, err := execHandler(t, "adr", "create",
		"--title", "First ADR",
		"--decision", "something")
	require.NoError(t, err)

	stdout, _, err := execHandler(t, "adr", "list")
	require.NoError(t, err)
	assert.Contains(t, stdout, "DES-001")
	assert.Contains(t, stdout, "First ADR")
}

func TestRunADRShow(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	_, _, err := execHandler(t, "adr", "create",
		"--title", "Show Test",
		"--decision", "test decision",
		"--context", "test context")
	require.NoError(t, err)

	stdout, _, err := execHandler(t, "adr", "show", "DES-001")
	require.NoError(t, err)
	assert.Contains(t, stdout, "ID:       DES-001")
	assert.Contains(t, stdout, "Title:    Show Test")
	assert.Contains(t, stdout, "test context")
	assert.Contains(t, stdout, "test decision")
}

func TestRunADRSettle(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	_, _, err := execHandler(t, "adr", "create",
		"--title", "Settle Test",
		"--decision", "settle this")
	require.NoError(t, err)

	stdout, _, err := execHandler(t, "adr", "settle", "DES-001")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Settled DES-001")

	// Verify status changed.
	stdout, _, err = execHandler(t, "adr", "show", "DES-001", "--json")
	require.NoError(t, err)
	var a map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &a))
	assert.Equal(t, "settled", a["status"])
}

func TestRunADRList_JSON(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	_, _, err := execHandler(t, "adr", "create",
		"--title", "JSON Test",
		"--decision", "json output")
	require.NoError(t, err)

	stdout, _, err := execHandler(t, "adr", "list", "--json")
	require.NoError(t, err)
	var adrs []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &adrs))
	require.Len(t, adrs, 1)
	assert.Equal(t, "DES-001", adrs[0]["id"])
}

func TestRunADRCreate_MissingTitle(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	_, _, err := execHandler(t, "adr", "create", "--decision", "no title")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--title is required")
}

func TestRunADRShow_NotFound(t *testing.T) {
	setupPhase2Env(t)
	resetPhase2Flags(t)

	_, _, err := execHandler(t, "adr", "show", "DES-999")
	require.Error(t, err)
}

func TestRunRoleShow_JSON(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	seedRole(t, se, "reviewer")

	stdout, _, err := execHandler(t, "role", "show", "reviewer", "--json")
	require.NoError(t, err)
	var r map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &r))
	assert.Equal(t, "reviewer", r["name"])
	assert.NotNil(t, r["responsibilities"])
}

func TestRunTeamShow_JSON(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	seedRole(t, se, "eng")
	seedTeam(t, se, "gamma", "test-agent", "eng")

	stdout, _, err := execHandler(t, "team", "show", "gamma", "--json")
	require.NoError(t, err)
	var tm map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &tm))
	assert.Equal(t, "gamma", tm["name"])
	members, ok := tm["members"].([]interface{})
	require.True(t, ok)
	require.Len(t, members, 1)
}

func TestRunTeamAddCollab(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	seedRole(t, se, "eng")
	seedRole(t, se, "lead")
	seedIdentity(t, se, "other-agent")
	seedTeamTwoMembers(t, se, "sigma", "test-agent", "eng", "other-agent", "lead")

	stdout, _, err := execHandler(t, "team", "add-collab", "sigma", "eng", "lead", "reports_to")
	require.NoError(t, err)
	assert.Contains(t, stdout, "eng -> lead (reports_to)")

	// Verify the collaboration persists.
	stdout, _, err = execHandler(t, "team", "show", "sigma", "--json")
	require.NoError(t, err)
	var tm map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &tm))
	collabs, ok := tm["collaborations"].([]interface{})
	require.True(t, ok)
	require.Len(t, collabs, 1)
	c := collabs[0].(map[string]interface{})
	assert.Equal(t, "eng", c["from"])
	assert.Equal(t, "lead", c["to"])
	assert.Equal(t, "reports_to", c["type"])
}

func TestRunTeamForRepo(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)

	// Test the "no argument, no git remote" path — explicit repo arg.
	seedRole(t, se, "eng")

	// Seed a team with a repository field.
	dir := filepath.Join(se.home, ".punt-labs", "ethos", "teams")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	data, err := yaml.Marshal(map[string]interface{}{
		"name":         "repo-team",
		"repositories": []string{"ethos"},
		"members":      []map[string]string{{"identity": "test-agent", "role": "eng"}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "repo-team.yaml"), data, 0o644))

	// Should find the team by repo name.
	stdout, _, err := execHandler(t, "team", "for-repo", "ethos")
	require.NoError(t, err)
	assert.Contains(t, stdout, "repo-team")
	assert.Contains(t, stdout, "test-agent (eng)")

	// Non-matching repo should print "no team found".
	stdout, _, err = execHandler(t, "team", "for-repo", "nonexistent")
	require.NoError(t, err)
	assert.Contains(t, stdout, "no team found")
}

// --- helpers ---

// seedRole creates a role YAML in the global store.
func seedRole(t *testing.T, se *cliSubprocessEnv, name string) {
	t.Helper()
	dir := filepath.Join(se.home, ".punt-labs", "ethos", "roles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	data, err := yaml.Marshal(map[string]interface{}{
		"name":             name,
		"responsibilities": []string{"do things"},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), data, 0o644))
}

// seedTeam creates a team YAML in the global store with one member.
func seedTeam(t *testing.T, se *cliSubprocessEnv, name, ident, role string) {
	t.Helper()
	dir := filepath.Join(se.home, ".punt-labs", "ethos", "teams")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	data, err := yaml.Marshal(map[string]interface{}{
		"name":    name,
		"members": []map[string]string{{"identity": ident, "role": role}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), data, 0o644))
}

// seedTeamTwoMembers creates a team YAML with two members.
func seedTeamTwoMembers(t *testing.T, se *cliSubprocessEnv, name, id1, role1, id2, role2 string) {
	t.Helper()
	dir := filepath.Join(se.home, ".punt-labs", "ethos", "teams")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	data, err := yaml.Marshal(map[string]interface{}{
		"name": name,
		"members": []map[string]string{
			{"identity": id1, "role": role1},
			{"identity": id2, "role": role2},
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), data, 0o644))
}

// seedIdentity creates an identity YAML in the global store.
func seedIdentity(t *testing.T, se *cliSubprocessEnv, handle string) {
	t.Helper()
	dir := filepath.Join(se.home, ".punt-labs", "ethos", "identities")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	data, err := yaml.Marshal(map[string]interface{}{
		"name":   handle,
		"handle": handle,
		"kind":   "agent",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, handle+".yaml"), data, 0o644))
}
