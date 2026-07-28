package hook

import (
	"encoding/json"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DES-057's degrade. A manifest-recorded extension the repo no longer
// holds must be REPORTED at session start and the session must continue.
//
// Failing here would brick a live session over agent memory wiring;
// saying nothing would hand the operator an agent that looks correct and
// has quietly lost it (Bugbot, PR #410). Doctor and agent generation are
// the surfaces that refuse.
func TestHandleSessionStart_DegradesOnMissingExt(t *testing.T) {
	dir := t.TempDir()
	repo := identity.NewStore(dir)
	ss := session.NewStore(dir)

	agent := &identity.Identity{
		Name: "Claude Agento", Handle: "claude", Kind: "agent",
		Personality: "calm-engineer",
	}
	writeAttr(t, dir, "personalities", "calm-engineer", "# Calm Engineer\n\nMethodical.\n")
	require.NoError(t, repo.Save(agent))
	require.NoError(t, repo.Save(&identity.Identity{Name: "Eve", Handle: "eve", Kind: "human"}))

	// repo-only, with a manifest claiming an ext file that is not there.
	store := identity.NewLayeredStoreWithBundle(repo, nil, identity.NewStore(t.TempDir()), true)
	store.WithRequiredExt(map[string][]string{"claude": {"quarry.yaml"}})

	isolateGitConfig(t, "eve")
	setupRepoWithAgent(t, "claude")

	var out string
	stderr := captureStderr(t, func() {
		out = captureSessionStartOutput(t, `{"session_id": "s-degrade"}`,
			SessionStartDeps{Store: store, Sessions: ss})
	})

	// Reported...
	assert.Contains(t, stderr, "ext/claude/quarry")
	assert.Contains(t, stderr, "ethos vendor claude --apply")

	// ...and the session still starts with the persona injected.
	var result SessionStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Contains(t, result.HookSpecificOutput.AdditionalContext, "Claude Agento")
}

// writeAttr writes an attribute .md directly under an ethos root.
func writeAttr(t *testing.T, root, dir, slug, body string) {
	t.Helper()
	writeFile(t, root+"/"+dir+"/"+slug+".md", body)
}
