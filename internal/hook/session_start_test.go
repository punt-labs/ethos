package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/punt-labs/ethos/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupIdentityWithAttributes creates an identity and its attribute .md files
// in a temp directory. Returns the identity store and session store.
func setupIdentityWithAttributes(t *testing.T, id *identity.Identity, personality, writingStyle string) (*identity.Store, *session.Store) {
	t.Helper()
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	if personality != "" {
		ps := attribute.NewStore(dir, attribute.Personalities)
		require.NoError(t, ps.Save(&attribute.Attribute{
			Slug:    id.Personality,
			Content: personality,
		}))
	}
	if writingStyle != "" {
		ws := attribute.NewStore(dir, attribute.WritingStyles)
		require.NoError(t, ws.Save(&attribute.Attribute{
			Slug:    id.WritingStyle,
			Content: writingStyle,
		}))
	}

	// Create talent .md files so Save's ValidateRefs passes.
	if len(id.Talents) > 0 {
		ts := attribute.NewStore(dir, attribute.Talents)
		for _, slug := range id.Talents {
			require.NoError(t, ts.Save(&attribute.Attribute{
				Slug:    slug,
				Content: "# " + slug,
			}))
		}
	}

	require.NoError(t, s.Save(id))
	return s, ss
}

// isolateGitConfig prevents git config from interfering with resolve.
func isolateGitConfig(t *testing.T, user string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(tmp, "empty.gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("USER", user)
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "empty.gitconfig"), []byte(""), 0o644))
}

// setupRepoWithAgent creates a fake repo root with .git/ and a
// .punt-labs/ethos.yaml that sets the agent to the given handle.
// It chdir's into the repo root so resolve.FindRepoRoot works.
func setupRepoWithAgent(t *testing.T, agentHandle string) string {
	t.Helper()
	repoRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755))

	puntDir := filepath.Join(repoRoot, ".punt-labs")
	require.NoError(t, os.MkdirAll(puntDir, 0o755))
	cfg := "agent: " + agentHandle + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(puntDir, "ethos.yaml"), []byte(cfg), 0o644))

	// Create the ethos subdir so FindRepoEthosRoot finds it.
	require.NoError(t, os.MkdirAll(filepath.Join(puntDir, "ethos"), 0o755))

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	return repoRoot
}

// setupRepoWithAgentLegacy creates a fake repo root using the legacy
// config path .punt-labs/ethos/config.yaml for fallback testing.
func setupRepoWithAgentLegacy(t *testing.T, agentHandle string) string {
	t.Helper()
	repoRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755))

	ethosDir := filepath.Join(repoRoot, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(ethosDir, 0o755))
	cfg := "agent: " + agentHandle + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(ethosDir, "config.yaml"), []byte(cfg), 0o644))

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	return repoRoot
}

// captureSessionStartOutput runs HandleSessionStart and captures stdout.
func captureSessionStartOutput(t *testing.T, input string, deps SessionStartDeps) string {
	t.Helper()

	// Capture stdout with cleanup to prevent leaks on early exit.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		os.Stdout = oldStdout
		w.Close()
		r.Close()
	})
	os.Stdout = w

	in := bytes.NewReader([]byte(input))
	require.NoError(t, HandleSessionStart(in, deps))

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.String()
}

func TestHandleSessionStart_PersonaBlock(t *testing.T) {
	// The human identity — used for roster creation.
	humanID := &identity.Identity{
		Name:   "Alice",
		Handle: "alice",
		Kind:   "human",
	}
	// The agent identity — used for persona injection.
	agentID := &identity.Identity{
		Name:         "Claude Agento",
		Handle:       "claude",
		Kind:         "agent",
		Personality:  "calm-engineer",
		WritingStyle: "concise-quant",
		Talents:      []string{"engineering"},
	}
	personality := "# Calm Engineer\n\nA calm and methodical engineer.\n\n- Stay focused\n- Data over adjectives"
	writingStyle := "# Concise Quantified\n\nShort and data-driven.\n\n- Under 30 words\n- Lead with numbers"

	s, ss := setupIdentityWithAttributes(t, agentID, personality, writingStyle)
	require.NoError(t, s.Save(humanID))
	isolateGitConfig(t, "alice")
	setupRepoWithAgent(t, "claude")

	out := captureSessionStartOutput(t, `{"session_id": "s1"}`, SessionStartDeps{Store: s, Sessions: ss})

	var result SessionStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "You are Claude Agento (claude)")
	assert.Contains(t, ctx, "A calm and methodical engineer.")
	assert.Contains(t, ctx, "## Personality")
	assert.Contains(t, ctx, "Stay focused")
	assert.Contains(t, ctx, "## Writing Style")
	assert.Contains(t, ctx, "Under 30 words")
	assert.Contains(t, ctx, "## Talents")
	assert.Contains(t, ctx, "engineering")
}

func TestHandleSessionStart_NoPersonality_FallsBack(t *testing.T) {
	id := &identity.Identity{
		Name:   "Bob",
		Handle: "bob",
		Kind:   "human",
	}

	s, ss := setupIdentityWithAttributes(t, id, "", "")
	isolateGitConfig(t, "bob")

	out := captureSessionStartOutput(t, `{"session_id": "s2"}`, SessionStartDeps{Store: s, Sessions: ss})

	var result SessionStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	// Should fall back to one-line format.
	assert.Contains(t, ctx, "Active identity: Bob (bob)")
	assert.NotContains(t, ctx, "## Personality")
}

func TestHandleSessionStart_NoIdentity_NoOutput(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	isolateGitConfig(t, "nobody")

	out := captureSessionStartOutput(t, `{"session_id": "s3"}`, SessionStartDeps{Store: s, Sessions: ss})

	// No identity resolved -- should produce no output.
	assert.Equal(t, "", out)
}

func TestHandleSessionStart_PersonalityOnly(t *testing.T) {
	// Agent identity with personality but no writing style.
	agentID := &identity.Identity{
		Name:        "Claude Agento",
		Handle:      "claude",
		Kind:        "agent",
		Personality: "methodical",
	}
	personality := "# Methodical\n\nQuiet and patient.\n\n- Think before acting\n- Simplicity first"

	humanID := &identity.Identity{Name: "Carol", Handle: "carol", Kind: "human"}
	s, ss := setupIdentityWithAttributes(t, agentID, personality, "")
	require.NoError(t, s.Save(humanID))
	isolateGitConfig(t, "carol")
	setupRepoWithAgent(t, "claude")

	out := captureSessionStartOutput(t, `{"session_id": "s4"}`, SessionStartDeps{Store: s, Sessions: ss})

	var result SessionStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "You are Claude Agento (claude)")
	assert.Contains(t, ctx, "## Personality")
	assert.Contains(t, ctx, "Think before acting")
	assert.NotContains(t, ctx, "## Writing Style")
}

func TestHandleSessionStart_WithExtensionContext(t *testing.T) {
	agentID := &identity.Identity{
		Name:         "Claude Agento",
		Handle:       "claude",
		Kind:         "agent",
		Personality:  "calm-engineer",
		WritingStyle: "concise-quant",
	}
	personality := "# Calm Engineer\n\nA calm and methodical engineer.\n\n- Stay focused"
	writingStyle := "# Concise Quantified\n\nShort and data-driven.\n\n- Under 30 words"

	s, ss := setupIdentityWithAttributes(t, agentID, personality, writingStyle)

	// Set quarry extension with session_context on the agent identity.
	require.NoError(t, s.ExtSet("claude", "quarry", "session_context", "You have memory via quarry. Collection: claude-memory"))

	humanID := &identity.Identity{Name: "Eve", Handle: "eve", Kind: "human"}
	require.NoError(t, s.Save(humanID))
	isolateGitConfig(t, "eve")
	setupRepoWithAgent(t, "claude")

	out := captureSessionStartOutput(t, `{"session_id": "s-ext"}`, SessionStartDeps{Store: s, Sessions: ss})

	var result SessionStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "claude-memory")
	assert.Contains(t, ctx, "## Personality", "persona block should still be present")
}

func TestHandleSessionStart_WithTeamContext(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)
	rs := role.NewLayeredStore("", dir)
	ts := team.NewLayeredStore("", dir)

	// Create identities.
	require.NoError(t, s.Save(&identity.Identity{
		Name:   "Jim Freeman",
		Handle: "jfreeman",
		Kind:   "human",
	}))
	agentID := &identity.Identity{
		Name:         "Claude Agento",
		Handle:       "claude",
		Kind:         "agent",
		Personality:  "calm-engineer",
		WritingStyle: "concise-quant",
	}
	ps := attribute.NewStore(dir, attribute.Personalities)
	require.NoError(t, ps.Save(&attribute.Attribute{
		Slug:    "calm-engineer",
		Content: "# Calm Engineer\n\nA calm and methodical engineer.\n\n- Stay focused",
	}))
	ws := attribute.NewStore(dir, attribute.WritingStyles)
	require.NoError(t, ws.Save(&attribute.Attribute{
		Slug:    "concise-quant",
		Content: "# Concise Quantified\n\nShort and data-driven.\n\n- Under 30 words",
	}))
	require.NoError(t, s.Save(agentID))

	// Create roles.
	require.NoError(t, rs.Save(&role.Role{
		Name:             "ceo",
		Responsibilities: []string{"Sets strategic direction"},
	}))
	require.NoError(t, rs.Save(&role.Role{
		Name:             "coo",
		Responsibilities: []string{"Execution quality"},
	}))

	// Create team.
	identityExists := func(handle string) bool { return s.Exists(handle) }
	roleExists := func(name string) bool { return rs.Exists(name) }
	require.NoError(t, ts.Save(&team.Team{
		Name: "test-eng",
		Members: []team.Member{
			{Identity: "jfreeman", Role: "ceo"},
			{Identity: "claude", Role: "coo"},
		},
		Collaborations: []team.Collaboration{
			{From: "coo", To: "ceo", Type: "reports_to"},
		},
	}, identityExists, roleExists))

	isolateGitConfig(t, "jfreeman")
	setupRepoWithTeam(t, "claude", "test-eng")

	deps := SessionStartDeps{
		Store:    s,
		Sessions: ss,
		Teams:    ts,
		Roles:    rs,
	}
	out := captureSessionStartOutput(t, `{"session_id": "s-team"}`, deps)

	var result SessionStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "## Team: test-eng")
	assert.Contains(t, ctx, "Jim Freeman (jfreeman) — ceo")
	assert.Contains(t, ctx, "Claude Agento (claude) — coo")
	assert.Contains(t, ctx, "Sets strategic direction")
	assert.Contains(t, ctx, "Execution quality")
	assert.Contains(t, ctx, "coo → ceo (reports_to)")
	// Persona block should still be present.
	assert.Contains(t, ctx, "You are Claude Agento (claude)")
}

func TestHandleSessionStart_WithExtensionContextAndTeam(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)
	rs := role.NewLayeredStore("", dir)
	ts := team.NewLayeredStore("", dir)

	// Create identities.
	require.NoError(t, s.Save(&identity.Identity{
		Name:   "Jim Freeman",
		Handle: "jfreeman",
		Kind:   "human",
	}))
	agentID := &identity.Identity{
		Name:         "Claude Agento",
		Handle:       "claude",
		Kind:         "agent",
		Personality:  "calm-engineer",
		WritingStyle: "concise-quant",
	}
	ps := attribute.NewStore(dir, attribute.Personalities)
	require.NoError(t, ps.Save(&attribute.Attribute{
		Slug:    "calm-engineer",
		Content: "# Calm Engineer\n\nA calm and methodical engineer.\n\n- Stay focused",
	}))
	ws := attribute.NewStore(dir, attribute.WritingStyles)
	require.NoError(t, ws.Save(&attribute.Attribute{
		Slug:    "concise-quant",
		Content: "# Concise Quantified\n\nShort and data-driven.\n\n- Under 30 words",
	}))
	require.NoError(t, s.Save(agentID))

	// Set quarry extension with session_context.
	require.NoError(t, s.ExtSet("claude", "quarry", "session_context", "## Memory\n\nYou have memory. Collection: claude-team-mem"))

	// Create roles.
	require.NoError(t, rs.Save(&role.Role{
		Name:             "ceo",
		Responsibilities: []string{"Sets strategic direction"},
	}))
	require.NoError(t, rs.Save(&role.Role{
		Name:             "coo",
		Responsibilities: []string{"Execution quality"},
	}))

	// Create team.
	identityExists := func(handle string) bool { return s.Exists(handle) }
	roleExists := func(name string) bool { return rs.Exists(name) }
	require.NoError(t, ts.Save(&team.Team{
		Name: "test-eng",
		Members: []team.Member{
			{Identity: "jfreeman", Role: "ceo"},
			{Identity: "claude", Role: "coo"},
		},
		Collaborations: []team.Collaboration{
			{From: "coo", To: "ceo", Type: "reports_to"},
		},
	}, identityExists, roleExists))

	isolateGitConfig(t, "jfreeman")
	setupRepoWithTeam(t, "claude", "test-eng")

	deps := SessionStartDeps{
		Store:    s,
		Sessions: ss,
		Teams:    ts,
		Roles:    rs,
	}
	out := captureSessionStartOutput(t, `{"session_id": "s-ext-team"}`, deps)

	var result SessionStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	// All three sections present.
	assert.Contains(t, ctx, "You are Claude Agento (claude)")
	assert.Contains(t, ctx, "## Memory")
	assert.Contains(t, ctx, "claude-team-mem")
	assert.Contains(t, ctx, "## Team: test-eng")

	// Verify ordering: extension context before Team.
	memIdx := strings.Index(ctx, "## Memory")
	teamIdx := strings.Index(ctx, "## Team: test-eng")
	assert.Greater(t, teamIdx, memIdx, "extension context should appear before team section")
}

func TestHandleSessionStart_LegacyConfigPath(t *testing.T) {
	agentID := &identity.Identity{
		Name:   "Claude Agento",
		Handle: "claude",
		Kind:   "agent",
	}
	humanID := &identity.Identity{Name: "Dave", Handle: "dave", Kind: "human"}

	s, ss := setupIdentityWithAttributes(t, agentID, "", "")
	require.NoError(t, s.Save(humanID))
	isolateGitConfig(t, "dave")
	setupRepoWithAgentLegacy(t, "claude")

	out := captureSessionStartOutput(t, `{"session_id": "s5"}`, SessionStartDeps{Store: s, Sessions: ss})

	var result SessionStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Active identity: Claude Agento (claude)")
}
