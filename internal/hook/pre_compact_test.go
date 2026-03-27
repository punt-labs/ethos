package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/punt-labs/ethos/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PreCompactResult mirrors the JSON structure emitted by HandlePreCompact.
type PreCompactResult struct {
	SystemMessage string `json:"systemMessage"`
}

// capturePreCompactOutput runs HandlePreCompact and captures stdout.
func capturePreCompactOutput(t *testing.T, input string, deps PreCompactDeps) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		os.Stdout = oldStdout
		_ = r.Close()
	})
	os.Stdout = w

	// Drain the pipe in a goroutine to avoid deadlock if output
	// exceeds the OS pipe buffer (~64KB).
	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, readErr := buf.ReadFrom(r)
		done <- readErr
	}()

	in := bytes.NewReader([]byte(input))
	require.NoError(t, HandlePreCompact(in, deps))

	w.Close() // unblocks the reader goroutine
	os.Stdout = oldStdout

	require.NoError(t, <-done)
	return buf.String()
}

// makeDeps creates PreCompactDeps from an identity store and session store.
func makeDeps(s identity.IdentityStore, ss *session.Store) PreCompactDeps {
	return PreCompactDeps{Identities: s, Sessions: ss}
}

// setupRepoWithTeam creates a fake repo root with .git/ and a
// .punt-labs/ethos.yaml that sets both agent and team. It chdir's
// into the repo root so resolve.FindRepoRoot + ResolveTeam work.
func setupRepoWithTeam(t *testing.T, agentHandle, teamName string) {
	t.Helper()
	repoRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755))
	puntDir := filepath.Join(repoRoot, ".punt-labs")
	require.NoError(t, os.MkdirAll(puntDir, 0o755))
	cfg := "agent: " + agentHandle + "\nteam: " + teamName + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(puntDir, "ethos.yaml"), []byte(cfg), 0o644))

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck
}

func TestHandlePreCompact_FullPersona(t *testing.T) {
	id := &identity.Identity{
		Name:         "Claude Agento",
		Handle:       "claude",
		Kind:         "agent",
		Personality:  "principal-engineer",
		WritingStyle: "concise-quantified",
		Talents:      []string{"formal-methods", "product-strategy"},
	}
	personality := "# Principal Engineer\n\nStrategic and thorough.\n\n- Think before acting\n- Data over adjectives\n- Simplicity first"
	writingStyle := "# Concise Quantified\n\nShort and data-driven.\n\n- Under 30 words\n- Lead with numbers\n- No weasel words"

	s, ss := setupIdentityWithAttributes(t, id, personality, writingStyle)

	// Create a session with a root human and a primary agent.
	root := session.Participant{AgentID: "jfreeman", Persona: "jim"}
	primary := session.Participant{AgentID: "12345", Persona: "claude", Parent: "jfreeman"}
	require.NoError(t, ss.Create("test-session", root, primary))

	payload, err := json.Marshal(map[string]string{"session_id": "test-session"})
	require.NoError(t, err)

	out := capturePreCompactOutput(t, string(payload), makeDeps(s, ss))

	var result PreCompactResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.SystemMessage
	// Full persona block — not condensed.
	assert.Contains(t, ctx, "You are Claude Agento (claude), Strategic and thorough.")
	assert.NotContains(t, ctx, "thorough..") // No double period.
	assert.Contains(t, ctx, "## Personality")
	assert.Contains(t, ctx, "Think before acting")
	assert.Contains(t, ctx, "Data over adjectives")
	assert.Contains(t, ctx, "Simplicity first")
	// First paragraph is deduplicated — should not repeat in personality section.
	assert.Contains(t, ctx, "## Writing Style")
	assert.Contains(t, ctx, "Under 30 words")
	assert.Contains(t, ctx, "Lead with numbers")
	assert.Contains(t, ctx, "No weasel words")
	assert.Contains(t, ctx, "## Talents")
	assert.Contains(t, ctx, "formal-methods, product-strategy")
}

func TestHandlePreCompact_WithTeamContext(t *testing.T) {
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
	require.NoError(t, s.Save(&identity.Identity{
		Name:   "Claude Agento",
		Handle: "claude",
		Kind:   "agent",
	}))

	// Create roles.
	require.NoError(t, rs.Save(&role.Role{
		Name:             "ceo",
		Responsibilities: []string{"Sets strategic direction", "Makes go/no-go decisions"},
	}))
	require.NoError(t, rs.Save(&role.Role{
		Name:             "coo",
		Responsibilities: []string{"Execution quality and velocity", "Plans work, assigns agents"},
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

	// Hermetic repo root with team config.
	setupRepoWithTeam(t, "claude", "test-eng")

	// Create session.
	root := session.Participant{AgentID: "jfreeman", Persona: "jfreeman"}
	primary := session.Participant{AgentID: "12345", Persona: "claude", Parent: "jfreeman"}
	require.NoError(t, ss.Create("test-session", root, primary))

	payload, err := json.Marshal(map[string]string{"session_id": "test-session"})
	require.NoError(t, err)

	deps := PreCompactDeps{
		Identities: s,
		Sessions:   ss,
		Teams:      ts,
		Roles:      rs,
	}
	out := capturePreCompactOutput(t, string(payload), deps)

	var result PreCompactResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.SystemMessage
	assert.Contains(t, ctx, "## Team: test-eng")
	assert.Contains(t, ctx, "Jim Freeman (jfreeman) — ceo")
	assert.Contains(t, ctx, "Claude Agento (claude) — coo")
	assert.Contains(t, ctx, "Sets strategic direction")
	assert.Contains(t, ctx, "Execution quality and velocity")
	assert.Contains(t, ctx, "coo → ceo (reports_to)")
}

func TestHandlePreCompact_NoSession_NoOutput(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	out := capturePreCompactOutput(t, `{}`, makeDeps(s, ss))
	assert.Equal(t, "", out)
}

func TestHandlePreCompact_SessionNotFound_NoOutput(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	out := capturePreCompactOutput(t, `{"session_id": "missing"}`, makeDeps(s, ss))
	assert.Equal(t, "", out)
}

func TestHandlePreCompact_NoPersonaOnAgent_NoOutput(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	root := session.Participant{AgentID: "jfreeman", Persona: "jim"}
	primary := session.Participant{AgentID: "99999", Persona: "", Parent: "jfreeman"}
	require.NoError(t, ss.Create("test-session", root, primary))

	out := capturePreCompactOutput(t, `{"session_id": "test-session"}`, makeDeps(s, ss))
	assert.Equal(t, "", out)
}

func TestHandlePreCompact_PersonaIdentityNotFound_NoOutput(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	root := session.Participant{AgentID: "jfreeman", Persona: "jim"}
	primary := session.Participant{AgentID: "99999", Persona: "ghost", Parent: "jfreeman"}
	require.NoError(t, ss.Create("test-session", root, primary))

	out := capturePreCompactOutput(t, `{"session_id": "test-session"}`, makeDeps(s, ss))
	assert.Equal(t, "", out)
}
