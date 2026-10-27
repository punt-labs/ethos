package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/punt-labs/ethos/v4/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refuseOverride makes the repository store unlocatable: ETHOS_REPO_ROOT names
// a directory that does not exist, so the resolver refuses it.
func refuseOverride(t *testing.T) string {
	t.Helper()
	bad := filepath.Join(t.TempDir(), "nope")
	t.Setenv("ETHOS_REPO_ROOT", bad)
	return bad
}

// When the store cannot be located the session starts without its persona; the
// session-start output says why, with the cause, so the operator is not left to
// wonder where the persona went.
func TestSessionStartSaysWhyTheStoreIsMissing(t *testing.T) {
	isolateGitConfig(t, "alice")
	bad := refuseOverride(t)
	dir := t.TempDir()
	deps := SessionStartDeps{Store: identity.NewStore(dir), Sessions: session.NewStore(dir)}

	out := captureSessionStartOutput(t, `{"session_id":"s1"}`, deps)

	var res struct {
		SystemMessage      string `json:"systemMessage"`
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &res), out)
	for name, text := range map[string]string{
		"additionalContext": res.HookSpecificOutput.AdditionalContext,
		"systemMessage":     res.SystemMessage,
	} {
		assert.Contains(t, text, "repository store could not be located", name)
		assert.Contains(t, text, bad, name)
	}
}

// The pre-compact hook re-injects the persona and team; with the store missing
// the team section names the cause instead of vanishing.
func TestPreCompactSaysWhyTheTeamIsMissing(t *testing.T) {
	bad := refuseOverride(t)
	dir := t.TempDir()
	s := identity.NewStore(dir)
	require.NoError(t, s.Save(&identity.Identity{Name: "Claude", Handle: "claude", Kind: "agent"}))
	ss := session.NewStore(dir)
	require.NoError(t, ss.Create("s1",
		session.Participant{AgentID: "h", Persona: "h"},
		session.Participant{AgentID: "1", Persona: "claude", Parent: "h"}, "", ""))
	deps := PreCompactDeps{
		Identities: s, Sessions: ss,
		Teams: team.NewLayeredStore("", dir), Roles: role.NewLayeredStore("", dir),
	}

	out, err := captureStdout(t, func() error {
		return HandlePreCompact(bytes.NewReader([]byte(`{"session_id":"s1"}`)), deps)
	})
	require.NoError(t, err)
	assert.Contains(t, out, "repository store could not be located")
	assert.Contains(t, out, bad)
}

// A session that started without its persona (the store was unlocatable) has
// none in its roster; compaction still says why.
func TestPreCompactSaysWhyThePersonaIsMissing(t *testing.T) {
	bad := refuseOverride(t)
	dir := t.TempDir()
	ss := session.NewStore(dir)
	require.NoError(t, ss.Create("s1",
		session.Participant{AgentID: "h", Persona: "h"},
		session.Participant{AgentID: "1", Parent: "h"}, "", ""))
	deps := PreCompactDeps{Identities: identity.NewStore(dir), Sessions: ss}

	out, err := captureStdout(t, func() error {
		return HandlePreCompact(bytes.NewReader([]byte(`{"session_id":"s1"}`)), deps)
	})
	require.NoError(t, err)
	assert.Contains(t, out, "repository store could not be located")
	assert.Contains(t, out, bad)
}

// Outside a repository there is nothing to explain.
func TestNoStoreNoteOutsideARepository(t *testing.T) {
	assert.Empty(t, storeUnlocatedNote(nil))
	assert.Empty(t, storeUnlocatedNote(wrapNotInRepo()))
}

func wrapNotInRepo() error { return fmt.Errorf("/x is %w", resolve.ErrNotInRepo) }
