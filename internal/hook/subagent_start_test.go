package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/punt-labs/ethos/internal/team"
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

	hs, err := mission.NewLiveHashSources(idStore,
		role.NewLayeredStore("", dir),
		team.NewLayeredStore("", dir),
	)
	require.NoError(t, err)
	hashSources = hs
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
			Ticket: "ethos-07m.7",
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
//
// Phase 3.5 changes the shape of the successful injection: instead of
// the normal persona block, a verifier spawn receives the isolation
// block containing the mission contract, verification criteria, and
// file allowlist. The persona block is deliberately suppressed — the
// verifier operates against the pinned contract, not against worker
// scratch or parent context derived from the persona chain.
func TestSubagentStart_VerifierMatchingHashAllowsSpawn(t *testing.T) {
	_, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	out, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.NoError(t, err, "matching hash must allow the spawn")
	// Phase 3.5 isolation block replaces the persona block for
	// verifier spawns. The block identifies the mission and the
	// verifier, lists the contract, verification criteria, and the
	// allowlist. The persona identity (name, personality body) is
	// deliberately suppressed.
	assert.Contains(t, out, "Verifier context", "isolation block header must be emitted")
	assert.Contains(t, out, c.MissionID, "isolation block must name the mission")
	assert.Contains(t, out, "frozen verifier", "isolation block must name the verifier role")
	assert.NotContains(t, out, "Dan B",
		"Phase 3.5: the persona block is suppressed on verifier spawns")
}

// TestSubagentStart_VerifierDriftedPersonalityRefusesSpawn asserts the
// load-bearing invariant from DES-033: editing the evaluator's
// personality file between mission create and verifier spawn refuses
// the spawn with an actionable error message.
//
// The error names the mission, the evaluator handle, the rollup drift
// (`pinned ... -> current ...`), the per-section content breakdown so
// the operator can see which file they edited, and BOTH recovery
// options (revert the edit or close+relaunch).
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
	assert.Contains(t, msg, "pinned ", "error must label the pinned hash")
	assert.Contains(t, msg, "current ", "error must label the current hash")
	assert.Contains(t, msg, "current content sections", "error must list the current per-section hashes")
	assert.Contains(t, msg, "personality:", "error must name the personality section")
	assert.Contains(t, msg, "writing_style:", "error must name the writing_style section")
	assert.Contains(t, msg, `talent "security"`, "error must name each talent by slug")
	assert.Contains(t, msg, "revert the edit", "error must offer the revert recovery path")
	assert.Contains(t, msg, "relaunch", "error must offer the relaunch recovery path")
}

// TestSubagentStart_VerifierAggregatesMultipleDriftedMissions asserts
// the H2 invariant: when the operator has edited one evaluator whose
// content is shared by several open missions, the hook emits a single
// aggregate error naming every drifted mission — not N separate
// refusal cycles. Each round of mission create must see every
// drifted mission at once so the operator can plan their recovery
// in one pass.
func TestSubagentStart_VerifierAggregatesMultipleDriftedMissions(t *testing.T) {
	dir, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	// Three missions, each with a disjoint write_set so Phase 3.2's
	// cross-mission conflict check does not collapse them.
	c1 := validVerifierContract("djb")
	c1.WriteSet = []string{"internal/multi/a/"}
	require.NoError(t, missions.ApplyServerFields(&c1, time.Now(), hash))
	require.NoError(t, missions.Create(&c1))

	c2 := validVerifierContract("djb")
	c2.WriteSet = []string{"internal/multi/b/"}
	require.NoError(t, missions.ApplyServerFields(&c2, time.Now(), hash))
	require.NoError(t, missions.Create(&c2))

	c3 := validVerifierContract("djb")
	c3.WriteSet = []string{"internal/multi/c/"}
	require.NoError(t, missions.ApplyServerFields(&c3, time.Now(), hash))
	require.NoError(t, missions.Create(&c3))

	// One edit to the evaluator's personality invalidates all three.
	personalityPath := filepath.Join(dir, "personalities", "bernstein.md")
	require.NoError(t, os.WriteFile(
		personalityPath,
		[]byte("# Bernstein\n\nDrifted content across three missions.\n"),
		0o600,
	))

	_, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.Error(t, err, "drift must refuse the spawn")
	msg := err.Error()
	assert.Contains(t, msg, "3 open missions", "header must state the drifted mission count")
	assert.Contains(t, msg, c1.MissionID, "error must name mission 1")
	assert.Contains(t, msg, c2.MissionID, "error must name mission 2")
	assert.Contains(t, msg, c3.MissionID, "error must name mission 3")
	// Aggregate phrasing must offer the multi-mission recovery path.
	assert.Contains(t, msg, "close the listed missions",
		"aggregate error must use the plural recovery instruction")
	assert.Contains(t, msg, "revert the edit",
		"aggregate error must also offer the revert recovery path")
	// The per-section listing is rendered once, not once per mission.
	assert.Equal(t, 1, strings.Count(msg, "current content sections"),
		"the content-sections block must appear exactly once")
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
	// Phase 3.6: Close requires a result artifact for the current
	// round. Submit one before closing so the test exercises the
	// verifier-gate no-op path, not the Phase 3.6 gate refusal.
	require.NoError(t, missions.AppendResult(c.MissionID, &mission.Result{
		Mission:    c.MissionID,
		Round:      c.CurrentRound,
		Author:     c.Worker,
		Verdict:    mission.VerdictPass,
		Confidence: 0.9,
		Evidence: []mission.EvidenceCheck{
			{Name: "make check", Status: mission.EvidenceStatusPass},
		},
	}))
	_, err := missions.Close(c.MissionID, mission.StatusClosed)
	require.NoError(t, err)

	// Drift the personality. A closed mission must NOT block the spawn.
	personalityPath := filepath.Join(dir, "personalities", "bernstein.md")
	require.NoError(t, os.WriteFile(
		personalityPath,
		[]byte("# Bernstein\n\nClosed mission, drift is allowed now.\n"),
		0o600,
	))

	_, err = runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
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

// TestSubagentStart_VerifierGateLegacyMissionSkipsRecompute is the
// round 4 Copilot-CP1 regression test. It proves the legacy-mission
// path never triggers the current-hash recompute, even when the
// recompute would itself fail.
//
// The scenario: a pre-3.3 mission has an empty pinned hash and is
// open. The operator deletes the evaluator's personality .md file
// (which would make the live-store hash compute fail with an
// "unresolved attribute warnings" error). Prior to round 4, the
// handler computed the breakdown before the empty-hash check, so the
// recompute would fail and block every verifier spawn. Round 4
// reorders the check: empty-hash legacy missions skip recompute
// entirely, and the spawn is allowed.
//
// A silent-skip on a legacy mission whose content has been damaged
// is the correct behavior: the operator must upgrade by relaunching,
// not by fighting the gate.
func TestSubagentStart_VerifierGateLegacyMissionSkipsRecompute(t *testing.T) {
	dir, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	// Create a legacy (empty-hash) mission.
	c := validVerifierContract("djb")
	c.MissionID = "m-2026-04-08-098"
	c.Status = mission.StatusOpen
	now := time.Now().UTC().Format(time.RFC3339)
	c.CreatedAt = now
	c.UpdatedAt = now
	c.Evaluator.PinnedAt = now
	c.Evaluator.Hash = "" // pre-3.3 placeholder
	require.NoError(t, missions.Create(&c))

	// Break the hash recompute by deleting the personality file. The
	// identity loader will surface "unresolved attribute warnings"
	// for this handle and ComputeEvaluatorHashBreakdown will fail. If
	// the legacy check runs AFTER the recompute, the hook returns
	// this error and blocks the spawn. If the legacy check runs
	// BEFORE the recompute (CP1 fix), the spawn is allowed because
	// the legacy mission short-circuits the loop before compute.
	require.NoError(t, os.Remove(filepath.Join(dir, "personalities", "bernstein.md")))

	_, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.NoError(t, err,
		"legacy mission must skip recompute; a broken recompute on a pre-3.3 mission must not block the spawn")
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

// TestSubagentStart_VerifierGateMisconfiguredHashIsFatal asserts that a
// caller who wires in a mission store but forgets to populate
// HashSources fails loudly. Silently skipping the gate on a
// misconfigured hash bundle would let drifted evaluator content
// through — the same silent-bypass case DES-033 was written to
// prevent. Mirrors the fail-fast rule enforced by
// TestApplyServerFields_RejectsNilSources in the mission package.
func TestSubagentStart_VerifierGateMisconfiguredHashIsFatal(t *testing.T) {
	_, idStore, missions, sessions, _ := setupVerifierTest(t, "djb")

	sessionID := "verifier-test-djb"
	require.NoError(t, sessions.Create(sessionID,
		session.Participant{AgentID: "user1", Persona: "jim"},
		session.Participant{AgentID: "12345", Persona: "claude"},
		"", "",
	))

	payload := `{"agent_id":"sub-verifier","agent_type":"djb","session_id":"verifier-test-djb"}`

	hookErr := HandleSubagentStartWithDeps(bytes.NewReader([]byte(payload)),
		SubagentStartDeps{
			Identities: idStore,
			Sessions:   sessions,
			Missions:   missions,
			Hash:       mission.HashSources{}, // deliberately empty
		})

	require.Error(t, hookErr, "misconfigured hash sources must refuse the spawn")
	assert.Contains(t, hookErr.Error(), "misconfigured",
		"error must label the misconfiguration so the operator fixes the wiring, not the content")
}

// TestSubagentStart_VerifierGateCorruptMissionIsFatal asserts that a
// hand-corrupted mission file on disk blocks the spawn. Silently
// skipping an unparseable contract would let an attacker bypass the
// frozen-evaluator gate by truncating or mangling the YAML — so the
// gate returns a fatal error that names the offending mission ID.
func TestSubagentStart_VerifierGateCorruptMissionIsFatal(t *testing.T) {
	_, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	// Seed one valid open mission so List walks at least one file
	// through the normal code path.
	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	// Write a corrupt YAML file directly into the missions directory
	// with a mission_id-shaped filename. The List walker treats any
	// non-dotfile .yaml file as a mission, so Load will be called
	// on this file and fail to decode.
	corruptPath := filepath.Join(missions.Root(), "missions", "m-2026-04-08-999.yaml")
	require.NoError(t, os.WriteFile(corruptPath, []byte("not valid yaml {[}\n"), 0o600))

	_, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.Error(t, err, "corrupt mission file must refuse the spawn")
	msg := err.Error()
	assert.Contains(t, msg, "failed to load mission",
		"error must label the load failure so the operator knows which layer broke")
	assert.Contains(t, msg, "m-2026-04-08-999",
		"error must name the offending mission ID so the operator can find the file")
}

// --- Phase 3.5 verifier context isolation tests ---
//
// These tests exercise the additionalContext shape the hook emits
// when the spawned subagent matches an open mission's evaluator
// handle. The isolation block replaces the normal persona/extension
// blocks and contains ONLY the mission contract (byte-for-byte from
// disk), the success criteria, and the file allowlist derived from
// the write_set. Parent transcript, worker scratch, and prior
// reasoning are excluded by virtue of never being added.

// TestSubagentStart_VerifierIsolationBlockShape asserts the full
// shape of the isolation block: header, verifier role line, contract
// YAML block, verification criteria list, allowlist list, and the
// explicit "MUST NOT read outside the allowlist" directive. This is
// the contract the verifier subagent sees on its first prompt.
func TestSubagentStart_VerifierIsolationBlockShape(t *testing.T) {
	_, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	c := validVerifierContract("djb")
	c.WriteSet = []string{"internal/test/", "cmd/ethos/mission.go"}
	c.SuccessCriteria = []string{
		"all new tests pass",
		"no files touched outside write_set",
	}
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	out, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.NoError(t, err)

	// Parse the JSON envelope.
	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	ctx := result.HookSpecificOutput.AdditionalContext
	require.NotEmpty(t, ctx, "verifier spawn must emit an additionalContext block")

	// Header + role line. The block root uses H2 (## Verifier context)
	// so the hierarchy does not collide with the host prompt's H1
	// structure; sub-sections (contract, criteria, allowlist) use H3.
	assert.Contains(t, ctx, "## Verifier context",
		"isolation block must begin with a clearly labeled H2 header")
	assert.Contains(t, ctx, "### Mission contract",
		"mission contract sub-section must be an H3 under the H2 root")
	assert.Contains(t, ctx, "### Verification criteria",
		"verification criteria sub-section must be an H3 under the H2 root")
	assert.Contains(t, ctx, "### File allowlist",
		"file allowlist sub-section must be an H3 under the H2 root")
	assert.Contains(t, ctx, c.MissionID,
		"isolation block must name the mission in the header")
	assert.Contains(t, ctx, `frozen verifier "djb"`,
		"isolation block must name the verifier role with the handle")

	// Explicit isolation directives.
	assert.Contains(t, ctx, "context isolation")
	assert.Contains(t, ctx, "MUST NOT read")
	assert.Contains(t, ctx, "parent transcript")
	assert.Contains(t, ctx, "outside the allowlist")

	// Contract YAML block (byte-for-byte from disk), fenced so the
	// verifier can read the whole thing without it smearing into
	// surrounding prose.
	assert.Contains(t, ctx, "Mission contract (byte-for-byte from disk)")
	assert.Contains(t, ctx, "```yaml")
	assert.Contains(t, ctx, "mission_id: "+c.MissionID)
	assert.Contains(t, ctx, "evaluator:")
	assert.Contains(t, ctx, "    handle: djb")

	// Verification criteria list — every entry from the contract.
	assert.Contains(t, ctx, "Verification criteria")
	for _, sc := range c.SuccessCriteria {
		assert.Contains(t, ctx, sc,
			"every success criterion must appear verbatim in the block")
	}

	// File allowlist — every write_set entry plus the contract file.
	assert.Contains(t, ctx, "File allowlist")
	for _, entry := range c.WriteSet {
		assert.Contains(t, ctx, entry,
			"every write_set entry must appear in the allowlist")
	}
	contractPath := missions.ContractPath(c.MissionID)
	assert.Contains(t, ctx, contractPath,
		"contract file path must be in the allowlist so the verifier can reread it")
}

// TestSubagentStart_VerifierIsolationContractBytesExactMatch asserts
// the byte-for-byte invariant from Phase 3.5: the contract block
// inside the isolation injection is read from disk, not re-marshaled
// from the parsed Contract struct. A re-marshal could produce
// different bytes (key reordering, comment loss) than the originally
// pinned contract, which would let the operator smuggle content past
// the trust boundary.
//
// The test reads the contract file from disk, strips the code fence,
// and compares the bytes directly.
func TestSubagentStart_VerifierIsolationContractBytesExactMatch(t *testing.T) {
	_, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	contractBytes, err := os.ReadFile(missions.ContractPath(c.MissionID))
	require.NoError(t, err)

	out, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.NoError(t, err)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	ctx := result.HookSpecificOutput.AdditionalContext
	// The block wraps contract bytes in a ```yaml fence; assert the
	// raw bytes appear inside it as-is.
	assert.Contains(t, ctx, string(contractBytes),
		"contract YAML inside the isolation block must be byte-for-byte identical to the on-disk file")
}

// TestSubagentStart_VerifierIsolationExcludesParentIdentity asserts
// the load-bearing Phase 3.5 rule: the isolation block does NOT
// contain the persona block, the identity name, the personality
// body, the writing style, or the extension context. A verifier
// spawn is a clean slate against the contract — not a continuation
// of the worker's scratch state.
//
// The setupVerifierTest fixture seeds identity "djb" with full
// personality and writing-style content. Without the isolation
// path, the hook would emit "Dan B", the personality body, and the
// "You report to" line from the parent resolution. The test asserts
// none of that leaks.
func TestSubagentStart_VerifierIsolationExcludesParentIdentity(t *testing.T) {
	_, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	out, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.NoError(t, err)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	ctx := result.HookSpecificOutput.AdditionalContext

	// None of the persona-identity material leaks into the verifier's
	// context. The isolation block is the only thing injected.
	assert.NotContains(t, ctx, "Dan B",
		"Phase 3.5: the persona name is suppressed on verifier spawns")
	assert.NotContains(t, ctx, "Methodical security review",
		"Phase 3.5: the personality body is suppressed on verifier spawns")
	assert.NotContains(t, ctx, "You report to",
		"Phase 3.5: the parent-reports-to line is suppressed on verifier spawns")
	assert.NotContains(t, ctx, "## Personality",
		"Phase 3.5: the persona block header is suppressed on verifier spawns")
	assert.NotContains(t, ctx, "## Writing Style",
		"Phase 3.5: the writing style block is suppressed on verifier spawns")
}

// TestSubagentStart_VerifierIsolationSkippedForNonEvaluator is the
// backwards-compatibility anchor: a subagent spawn whose agentType is
// NOT the evaluator handle of any open mission gets the normal
// persona/extension block, not the isolation block. Phase 3.5's
// isolation fires only when the discriminator matches.
func TestSubagentStart_VerifierIsolationSkippedForNonEvaluator(t *testing.T) {
	dir, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	// Seed a second identity (`bwk`) with its own persona content so
	// the normal injection path has something to render.
	require.NoError(t, attribute.NewStore(dir, attribute.Personalities).Save(&attribute.Attribute{
		Slug:    "kernighan",
		Content: "# Kernighan\n\nA methodical systems programmer.\n",
	}))
	require.NoError(t, attribute.NewStore(dir, attribute.WritingStyles).Save(&attribute.Attribute{
		Slug:    "kernighan-prose",
		Content: "# Kernighan Prose\n\nShort declarative sentences.\n",
	}))
	require.NoError(t, idStore.Save(&identity.Identity{
		Name:         "Brian K",
		Handle:       "bwk",
		Kind:         "agent",
		Personality:  "kernighan",
		WritingStyle: "kernighan-prose",
	}))

	// Create a mission with djb as evaluator; bwk is the worker.
	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	// Now spawn a subagent as bwk — bwk is the worker, not the
	// evaluator, so the isolation path does NOT fire and the normal
	// persona block is emitted.
	out, err := runHookForVerifier(t, idStore, sessions, missions, hash, "bwk")
	require.NoError(t, err)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	ctx := result.HookSpecificOutput.AdditionalContext

	assert.Contains(t, ctx, "Brian K",
		"non-verifier spawn must still receive its persona block")
	assert.Contains(t, ctx, "A methodical systems programmer",
		"non-verifier spawn must still receive its personality body")
	assert.NotContains(t, ctx, "Verifier context",
		"non-verifier spawn must NOT receive the isolation block")
}

// TestSubagentStart_VerifierIsolationNoOpForClosedMission asserts
// that a subagent whose handle MATCHES a closed mission's evaluator
// gets the normal persona block, not the isolation block. The
// isolation gate's discriminator is "open missions only" — closed,
// failed, or escalated missions are out of Phase 3.5's purview.
func TestSubagentStart_VerifierIsolationNoOpForClosedMission(t *testing.T) {
	dir, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	// Create and close the only mission.
	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))
	// Phase 3.6: the Close gate requires a result for the current
	// round. Submit one before closing so the test exercises the
	// verifier-isolation no-op path.
	require.NoError(t, missions.AppendResult(c.MissionID, &mission.Result{
		Mission:    c.MissionID,
		Round:      c.CurrentRound,
		Author:     c.Worker,
		Verdict:    mission.VerdictPass,
		Confidence: 0.9,
		Evidence: []mission.EvidenceCheck{
			{Name: "make check", Status: mission.EvidenceStatusPass},
		},
	}))
	_, err := missions.Close(c.MissionID, mission.StatusClosed)
	require.NoError(t, err)

	// Spawning djb now should fall through to the normal persona
	// path — the fixture seeds djb with a personality body.
	_ = dir // silence unused warning on some refactor paths
	out, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.NoError(t, err, "closed mission must not block or isolate the spawn")

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	ctx := result.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Dan B",
		"closed mission must not suppress the persona block")
	assert.NotContains(t, ctx, "Verifier context",
		"closed mission must not emit the isolation block")
}

// TestSubagentStart_VerifierIsolationWriteSetIsAllowlist asserts
// that the file allowlist block in the isolation context contains
// every write_set entry from the contract AND the contract file
// path itself. The test exercises a write_set with multiple entries
// so the allowlist rendering handles the list case, not just a
// single-entry edge.
func TestSubagentStart_VerifierIsolationWriteSetIsAllowlist(t *testing.T) {
	_, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	c := validVerifierContract("djb")
	c.WriteSet = []string{
		"internal/mission/store.go",
		"internal/mission/validate.go",
		"internal/hook/subagent_start.go",
	}
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	out, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.NoError(t, err)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	ctx := result.HookSpecificOutput.AdditionalContext

	for _, entry := range c.WriteSet {
		assert.Contains(t, ctx, entry,
			"allowlist must include every write_set entry")
	}
	assert.Contains(t, ctx, missions.ContractPath(c.MissionID),
		"allowlist must include the contract file path")
}

// TestVerifierAllowlist_Deduplicates asserts that if a mission's
// write_set accidentally lists the contract file path, the allowlist
// does not produce a double entry. Unit test of the pure helper.
func TestVerifierAllowlist_Deduplicates(t *testing.T) {
	tmpRoot := t.TempDir()
	store := mission.NewStore(tmpRoot)
	contractPath := store.ContractPath("m-2026-04-08-001")

	c := &mission.Contract{
		MissionID: "m-2026-04-08-001",
		WriteSet: []string{
			"internal/foo/",
			contractPath, // accidental inclusion
			"internal/bar/",
		},
	}
	list := verifierAllowlist(c, store)

	// One entry per distinct path; contract path appears once even
	// though it was in the write_set.
	count := 0
	for _, entry := range list {
		if entry == contractPath {
			count++
		}
	}
	assert.Equal(t, 1, count, "contract path must appear exactly once")
}

// TestVerifierAllowlist_PreservesOrder asserts that write_set
// entries retain their declaration order in the allowlist. The
// operator wrote them in that order for a reason and the verifier's
// injection must reflect that ordering stably.
func TestVerifierAllowlist_PreservesOrder(t *testing.T) {
	store := mission.NewStore(t.TempDir())
	c := &mission.Contract{
		MissionID: "m-2026-04-08-001",
		WriteSet: []string{
			"z-last",
			"a-first",
			"m-middle",
		},
	}
	list := verifierAllowlist(c, store)
	// First three entries should be the write_set in declaration
	// order; the contract file path is appended last.
	require.GreaterOrEqual(t, len(list), 3)
	assert.Equal(t, "z-last", list[0])
	assert.Equal(t, "a-first", list[1])
	assert.Equal(t, "m-middle", list[2])
}

// TestSubagentStart_VerifierEmitsAllowlistEnv asserts that a verifier
// spawn's hook result includes the ETHOS_VERIFIER_ALLOWLIST env var
// with the colon-separated allowlist. This is the bridge between
// SubagentStart (which knows the verifier's write_set) and PreToolUse
// (which enforces the allowlist mechanically).
func TestSubagentStart_VerifierEmitsAllowlistEnv(t *testing.T) {
	_, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	c := validVerifierContract("djb")
	c.WriteSet = []string{"internal/hook/pretooluse.go", "internal/hook/pretooluse_test.go"}
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	out, err := runHookForVerifier(t, idStore, sessions, missions, hash, "djb")
	require.NoError(t, err)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	require.NotNil(t, result.Env, "verifier spawn must set env vars")
	allowlist, ok := result.Env["ETHOS_VERIFIER_ALLOWLIST"]
	require.True(t, ok, "env must contain ETHOS_VERIFIER_ALLOWLIST")

	// The allowlist must contain every write_set entry and the contract file.
	for _, entry := range c.WriteSet {
		assert.Contains(t, allowlist, entry,
			"allowlist env var must include every write_set entry")
	}
	contractPath := missions.ContractPath(c.MissionID)
	assert.Contains(t, allowlist, contractPath,
		"allowlist env var must include the contract file path")
}

// TestSubagentStart_NonVerifierOmitsAllowlistEnv asserts that a
// non-verifier spawn does not set ETHOS_VERIFIER_ALLOWLIST. The env
// field must be nil or absent so PreToolUse operates in passthrough
// mode for normal subagents.
func TestSubagentStart_NonVerifierOmitsAllowlistEnv(t *testing.T) {
	dir, idStore, missions, sessions, hash := setupVerifierTest(t, "djb")

	// Seed a second identity so the normal persona path fires.
	require.NoError(t, attribute.NewStore(dir, attribute.Personalities).Save(&attribute.Attribute{
		Slug:    "kernighan",
		Content: "# Kernighan\n\nA methodical systems programmer.\n",
	}))
	require.NoError(t, idStore.Save(&identity.Identity{
		Name:        "Brian K",
		Handle:      "bwk",
		Kind:        "agent",
		Personality: "kernighan",
	}))

	// Create a mission with djb as evaluator, bwk as worker.
	c := validVerifierContract("djb")
	require.NoError(t, missions.ApplyServerFields(&c, time.Now(), hash))
	require.NoError(t, missions.Create(&c))

	// Spawn bwk — not the evaluator.
	out, err := runHookForVerifier(t, idStore, sessions, missions, hash, "bwk")
	require.NoError(t, err)

	var result SubagentStartResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))

	assert.Nil(t, result.Env,
		"non-verifier spawn must not set env vars")
}
