package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureSubagentStartOutput runs HandleSubagentStart and captures stdout.
func captureSubagentStartOutput(t *testing.T, input string, s identity.IdentityStore, ss *session.Store) string {
	t.Helper()

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
	require.NoError(t, HandleSubagentStart(in, s, ss))

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.String()
}

func TestHandleSubagentStart_PersonaBlock(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	// Create a personality and writing style for the subagent identity.
	ps := attribute.NewStore(dir, attribute.Personalities)
	require.NoError(t, ps.Save(&attribute.Attribute{
		Slug:    "kernighan",
		Content: "# Kernighan\n\nA methodical systems programmer.\n\n- Simplicity first\n- Clarity over cleverness",
	}))
	ws := attribute.NewStore(dir, attribute.WritingStyles)
	require.NoError(t, ws.Save(&attribute.Attribute{
		Slug:    "kernighan-prose",
		Content: "# Kernighan Prose\n\nShort declarative sentences.\n\n- Under 25 words\n- Data over adjectives",
	}))
	ts := attribute.NewStore(dir, attribute.Talents)
	require.NoError(t, ts.Save(&attribute.Attribute{
		Slug:    "go-specialist",
		Content: "# Go Specialist",
	}))

	require.NoError(t, s.Save(&identity.Identity{
		Name:         "Brian K",
		Handle:       "bwk",
		Kind:         "agent",
		Personality:  "kernighan",
		WritingStyle: "kernighan-prose",
		Talents:      []string{"go-specialist"},
	}))

	// Create parent identity for the "reports to" line.
	require.NoError(t, s.Save(&identity.Identity{
		Name:   "Claude Agento",
		Handle: "claude",
		Kind:   "agent",
	}))

	// Create a session with a parent agent. Use FindClaudePID so
	// resolveParentLine can match the subagent's Parent field.
	claudePID := process.FindClaudePID()
	require.NoError(t, ss.Create("sub-test-1",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: claudePID, Persona: "claude"},
		"", "",
	))

	payload := `{
		"agent_id": "sub-1",
		"agent_type": "bwk",
		"session_id": "sub-test-1"
	}`

	out := captureSubagentStartOutput(t, payload, s, ss)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "You are Brian K (bwk)")
	assert.Contains(t, ctx, "A methodical systems programmer.")
	assert.Contains(t, ctx, "## Personality")
	assert.Contains(t, ctx, "Simplicity first")
	assert.Contains(t, ctx, "## Writing Style")
	assert.Contains(t, ctx, "Under 25 words")
	assert.Contains(t, ctx, "## Talents")
	assert.Contains(t, ctx, "go-specialist")
	assert.Contains(t, ctx, "You report to Claude Agento (claude).")
	assert.Equal(t, "SubagentStart", result.HookSpecificOutput.HookEventName)
}

func TestHandleSubagentStart_WithExtensions(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	ps := attribute.NewStore(dir, attribute.Personalities)
	require.NoError(t, ps.Save(&attribute.Attribute{
		Slug:    "kernighan",
		Content: "# Kernighan\n\nA methodical systems programmer.\n\n- Simplicity first",
	}))
	ws := attribute.NewStore(dir, attribute.WritingStyles)
	require.NoError(t, ws.Save(&attribute.Attribute{
		Slug:    "kernighan-prose",
		Content: "# Kernighan Prose\n\nShort declarative sentences.",
	}))

	require.NoError(t, s.Save(&identity.Identity{
		Name:         "Brian K",
		Handle:       "bwk",
		Kind:         "agent",
		Personality:  "kernighan",
		WritingStyle: "kernighan-prose",
	}))

	// Write extension with session_context.
	require.NoError(t, s.ExtSet("bwk", "quarry", "session_context", "memory instructions here"))

	claudePID := process.FindClaudePID()
	require.NoError(t, ss.Create("ext-test-1",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: claudePID, Persona: ""},
		"", "",
	))

	payload := `{
		"agent_id": "sub-ext-1",
		"agent_type": "bwk",
		"session_id": "ext-test-1"
	}`

	out := captureSubagentStartOutput(t, payload, s, ss)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "You are Brian K (bwk)")
	assert.Contains(t, ctx, "A methodical systems programmer.")
	assert.Contains(t, ctx, "memory instructions here")
}

func TestHandleSubagentStart_NoExtensions(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	ps := attribute.NewStore(dir, attribute.Personalities)
	require.NoError(t, ps.Save(&attribute.Attribute{
		Slug:    "kernighan",
		Content: "# Kernighan\n\nA methodical systems programmer.\n\n- Simplicity first",
	}))
	ws := attribute.NewStore(dir, attribute.WritingStyles)
	require.NoError(t, ws.Save(&attribute.Attribute{
		Slug:    "kernighan-prose",
		Content: "# Kernighan Prose\n\nShort declarative sentences.",
	}))

	require.NoError(t, s.Save(&identity.Identity{
		Name:         "Brian K",
		Handle:       "bwk",
		Kind:         "agent",
		Personality:  "kernighan",
		WritingStyle: "kernighan-prose",
	}))

	// No extensions written -- bwk.ext/ does not exist.

	claudePID := process.FindClaudePID()
	require.NoError(t, ss.Create("noext-test-1",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: claudePID, Persona: ""},
		"", "",
	))

	payload := `{
		"agent_id": "sub-noext-1",
		"agent_type": "bwk",
		"session_id": "noext-test-1"
	}`

	out := captureSubagentStartOutput(t, payload, s, ss)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "You are Brian K (bwk)")
	assert.Contains(t, ctx, "A methodical systems programmer.")
	assert.NotContains(t, ctx, "memory instructions")
}

func TestHandleSubagentStart_ExtensionWithoutSessionContext(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	ps := attribute.NewStore(dir, attribute.Personalities)
	require.NoError(t, ps.Save(&attribute.Attribute{
		Slug:    "kernighan",
		Content: "# Kernighan\n\nA methodical systems programmer.\n\n- Simplicity first",
	}))
	ws := attribute.NewStore(dir, attribute.WritingStyles)
	require.NoError(t, ws.Save(&attribute.Attribute{
		Slug:    "kernighan-prose",
		Content: "# Kernighan Prose\n\nShort declarative sentences.",
	}))

	require.NoError(t, s.Save(&identity.Identity{
		Name:         "Brian K",
		Handle:       "bwk",
		Kind:         "agent",
		Personality:  "kernighan",
		WritingStyle: "kernighan-prose",
	}))

	// Write extension with a key other than session_context.
	require.NoError(t, s.ExtSet("bwk", "quarry", "provider", "some-value"))

	claudePID := process.FindClaudePID()
	require.NoError(t, ss.Create("extnosc-test-1",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: claudePID, Persona: ""},
		"", "",
	))

	payload := `{
		"agent_id": "sub-extnosc-1",
		"agent_type": "bwk",
		"session_id": "extnosc-test-1"
	}`

	out := captureSubagentStartOutput(t, payload, s, ss)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "You are Brian K (bwk)")
	assert.Contains(t, ctx, "A methodical systems programmer.")
	assert.NotContains(t, ctx, "some-value")
}

func TestHandleSubagentStart_NoMatchingPersona_NoOutput(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	// Create a session but no identity for the agent_type.
	require.NoError(t, ss.Create("sub-test-2",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: "12345", Persona: "claude"},
		"", "",
	))

	payload := `{
		"agent_id": "sub-2",
		"agent_type": "code-reviewer",
		"session_id": "sub-test-2"
	}`

	out := captureSubagentStartOutput(t, payload, s, ss)
	assert.Equal(t, "", out)
}

func TestHandleSubagentStart_PersonaNoPersonality_GracefulFallback(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)

	// Identity with no personality or writing style -- just name/handle/kind.
	require.NoError(t, s.Save(&identity.Identity{
		Name:   "Reviewer Bot",
		Handle: "reviewer",
		Kind:   "agent",
	}))

	require.NoError(t, ss.Create("sub-test-3",
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: "12345", Persona: "claude"},
		"", "",
	))

	payload := `{
		"agent_id": "sub-3",
		"agent_type": "reviewer",
		"session_id": "sub-test-3"
	}`

	out := captureSubagentStartOutput(t, payload, s, ss)

	// No personality or writing style means BuildPersonaBlock returns "".
	// The handler should emit nothing in this case.
	assert.Equal(t, "", out)
}

// --- Phase 3.3 verifier hash gate tests (DES-033) ---
//
// These tests exercise the SubagentStart hook's frozen-evaluator
// enforcement: the handler refuses to spawn a subagent when the
// pinned evaluator hash on any open mission disagrees with the
// recomputed hash from current identity content.

// setupVerifierTest builds the full dependency graph for a verifier-
// gate test: identity store with the named handle seeded, attribute
// stores with personality/writing-style/talent files written, mission
// store, session store, and HashSources. Returns everything the test
// needs to mutate the world between contract create and verifier
// spawn — exactly the integration shape DES-033 specifies.
func setupVerifierTest(t *testing.T, evaluator string) (
	dir string,
	idStore *identity.Store,
	missionStore *mission.Store,
	sessionStore *session.Store,
	hashSources mission.HashSources,
) {
	t.Helper()
	dir = t.TempDir()
	idStore = identity.NewStore(dir)
	missionStore = mission.NewStore(dir)
	sessionStore = session.NewStore(dir)

	// Seed personality, writing style, and talent files.
	personalities := attribute.NewStore(dir, attribute.Personalities)
	require.NoError(t, personalities.Save(&attribute.Attribute{
		Slug:    "bernstein",
		Content: "# Bernstein\n\nMethodical security review.\n",
	}))
	writingStyles := attribute.NewStore(dir, attribute.WritingStyles)
	require.NoError(t, writingStyles.Save(&attribute.Attribute{
		Slug:    "bernstein-prose",
		Content: "# Bernstein Prose\n\nShort declarative sentences.\n",
	}))
	talents := attribute.NewStore(dir, attribute.Talents)
	require.NoError(t, talents.Save(&attribute.Attribute{
		Slug:    "security",
		Content: "# Security\n\nThreat modeling.\n",
	}))

	require.NoError(t, idStore.Save(&identity.Identity{
		Name:         "Dan B",
		Handle:       evaluator,
		Kind:         "agent",
		Personality:  "bernstein",
		WritingStyle: "bernstein-prose",
		Talents:      []string{"security"},
	}))

	hashSources = mission.NewLiveHashSources(idStore, nil, nil)
	return
}

// validVerifierContract returns a fully-populated mission contract
// naming the given evaluator. Server-controlled fields are left
// empty for ApplyServerFields to fill in.
func validVerifierContract(evaluator string) mission.Contract {
	return mission.Contract{
		Leader: "claude",
		Worker: "bwk",
		Evaluator: mission.Evaluator{
			Handle: evaluator,
		},
		Inputs: mission.Inputs{
			Bead: "ethos-07m.7",
		},
		WriteSet:        []string{"internal/test/"},
		SuccessCriteria: []string{"hash gate is enforced"},
		Budget: mission.Budget{
			Rounds:              3,
			ReflectionAfterEach: true,
		},
	}
}

// runHookForVerifier captures HandleSubagentStartWithDeps's behavior
// for a verifier spawn payload. Returns the handler's error and
// whatever it wrote to stdout.
func runHookForVerifier(
	t *testing.T,
	idStore *identity.Store,
	ss *session.Store,
	missions *mission.Store,
	hash mission.HashSources,
	agentType string,
) (string, error) {
	t.Helper()

	// Spawn a session so the join path has a target. The session
	// store has no Exists method; Load with an os-not-exist error
	// is the closest equivalent.
	sessionID := "verifier-test-" + agentType
	if _, err := ss.Load(sessionID); err != nil {
		require.NoError(t, ss.Create(sessionID,
			session.Participant{AgentID: "user1", Persona: "jim"},
			session.Participant{AgentID: "12345", Persona: "claude"},
			"", "",
		))
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = oldStdout
		w.Close()
		r.Close()
	})

	payload := fmt.Sprintf(`{"agent_id":"sub-verifier","agent_type":%q,"session_id":%q}`,
		agentType, sessionID)
	in := bytes.NewReader([]byte(payload))
	hookErr := HandleSubagentStartWithDeps(in, SubagentStartDeps{
		Identities: idStore,
		Sessions:   ss,
		Missions:   missions,
		Hash:       hash,
	})

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String(), hookErr
}

// TestSubagentStart_VerifierMatchingHashAllowsSpawn asserts the happy
// path: a freshly created mission has its evaluator's content
// unchanged on disk, so the recomputed hash matches the pinned hash
// and the spawn is allowed (no error returned).
func TestSubagentStart_VerifierMatchingHashAllowsSpawn(t *testing.T) {
	_, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	out, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.NoError(t, err, "matching hash must allow the spawn")
	assert.Contains(t, out, "Dan B", "persona block must still be emitted on a successful spawn")
}

// TestSubagentStart_VerifierDriftedPersonalityRefusesSpawn asserts the
// load-bearing invariant from DES-033: editing the evaluator's
// personality file between mission create and verifier spawn refuses
// the spawn with an actionable error message.
func TestSubagentStart_VerifierDriftedPersonalityRefusesSpawn(t *testing.T) {
	dir, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	// Mutate the evaluator's personality file ON DISK between create
	// and spawn. This is the exact attacker model the gate exists
	// to defeat.
	personalityPath := filepath.Join(dir, "personalities", "bernstein.md")
	require.NoError(t, os.WriteFile(
		personalityPath,
		[]byte("# Bernstein\n\nDrifted content.\nNew rules.\n"),
		0o600,
	))

	_, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.Error(t, err, "drifted personality must refuse the spawn")
	msg := err.Error()
	assert.Contains(t, msg, c.MissionID, "error must name the mission")
	assert.Contains(t, msg, "djb", "error must name the evaluator handle")
	assert.Contains(t, msg, "pinned hash", "error must label the pinned hash")
	assert.Contains(t, msg, "current hash", "error must label the current hash")
	assert.Contains(t, msg, "relaunch", "error must tell the operator how to recover")
}

// TestSubagentStart_VerifierDriftedWritingStyleRefusesSpawn asserts
// that drift in any one content source — not just personality — is
// detected. The hash function covers all four (DES-033); this test
// covers writing style as a representative second source.
func TestSubagentStart_VerifierDriftedWritingStyleRefusesSpawn(t *testing.T) {
	dir, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	stylePath := filepath.Join(dir, "writing-styles", "bernstein-prose.md")
	require.NoError(t, os.WriteFile(
		stylePath,
		[]byte("# Bernstein Prose\n\nLonger sentences are now allowed.\n"),
		0o600,
	))

	_, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.Error(t, err, "drifted writing style must refuse the spawn")
	assert.Contains(t, err.Error(), c.MissionID)
}

// TestSubagentStart_VerifierDriftedTalentRefusesSpawn covers a third
// content source — talents — to keep the table covered. Adding a new
// content source to DES-033 should add a row here.
func TestSubagentStart_VerifierDriftedTalentRefusesSpawn(t *testing.T) {
	dir, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	talentPath := filepath.Join(dir, "talents", "security.md")
	require.NoError(t, os.WriteFile(
		talentPath,
		[]byte("# Security\n\nNew threat-modeling rules.\n"),
		0o600,
	))

	_, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.Error(t, err, "drifted talent content must refuse the spawn")
	assert.Contains(t, err.Error(), c.MissionID)
}

// TestSubagentStart_VerifierGateNoOpForUnrelatedAgentType asserts that
// a subagent spawn for an agent_type that is NOT the evaluator of any
// open mission passes through unchanged. The gate must not block
// general subagent activity.
func TestSubagentStart_VerifierGateNoOpForUnrelatedAgentType(t *testing.T) {
	_, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	// Spawn a different agent type. The gate must not block.
	_, err := runHookForVerifier(t, idStore, sessions, missions, hash, "bwk")
	require.NoError(t, err)
}

// TestSubagentStart_VerifierGateNoOpForClosedMission asserts that
// missions that have transitioned to a terminal status are out of
// the gate's purview. The contract is no longer active; drift is
// historically interesting but not actionable.
func TestSubagentStart_VerifierGateNoOpForClosedMission(t *testing.T) {
	dir, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))
	require.NoError(t, missions.Close(c.MissionID, mission.StatusClosed))

	// Drift the personality. A closed mission must NOT block the spawn.
	personalityPath := filepath.Join(dir, "personalities", "bernstein.md")
	require.NoError(t, os.WriteFile(
		personalityPath,
		[]byte("# Bernstein\n\nClosed mission, drift is allowed now.\n"),
		0o600,
	))

	_, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.NoError(t, err, "closed mission must not block verifier spawn")
}

// TestSubagentStart_VerifierGateLegacyMissionAllowsSpawn asserts that
// pre-3.3 missions with an empty Evaluator.Hash do NOT block verifier
// spawns. They predate the gate; refusing them would force operators
// to close every existing mission before upgrading. The hook logs a
// warning to stderr and proceeds.
func TestSubagentStart_VerifierGateLegacyMissionAllowsSpawn(t *testing.T) {
	_, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	// Create a contract directly via Store.Create (no
	// ApplyServerFields), simulating a pre-3.3 mission whose
	// Evaluator.Hash is empty. The mission_id is hand-built so we
	// don't need a counter advance.
	c := validVerifierContract("djb")
	c.MissionID = "m-2026-04-08-099"
	c.Status = mission.StatusOpen
	now := time.Now().UTC().Format(time.RFC3339)
	c.CreatedAt = now
	c.UpdatedAt = now
	c.Evaluator.PinnedAt = now
	c.Evaluator.Hash = "" // pre-3.3 placeholder
	require.NoError(t, missions.Create(&c))

	_, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.NoError(t, err, "legacy missions with empty hash must not block spawn")
}

// TestSubagentStart_VerifierGateNoMissionStoreIsLegacy asserts that
// the legacy HandleSubagentStart entry point (with no mission store
// configured) skips the hash gate entirely. Existing installs must
// not break when they upgrade to a binary with verifier semantics.
func TestSubagentStart_VerifierGateNoMissionStoreIsLegacy(t *testing.T) {
	_, idStore, _, sessions, _ := setupVerifierTest(t, "djb")

	// HandleSubagentStartWithDeps with nil Missions: gate is a no-op.
	sessionID := "no-mission-test"
	require.NoError(t, sessions.Create(sessionID,
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: "12345", Persona: "claude"},
		"", "",
	))

	payload := `{"agent_id":"sub-1","agent_type":"djb","session_id":"no-mission-test"}`

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	hookErr := HandleSubagentStartWithDeps(bytes.NewReader([]byte(payload)),
		SubagentStartDeps{
			Identities: idStore,
			Sessions:   sessions,
		})
	w.Close()
	os.Stdout = oldStdout
	r.Close()

	require.NoError(t, hookErr, "legacy install (no mission store) must not block")
}
