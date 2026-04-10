package hook

import (
	"bytes"
	"encoding/json"
	"errors"
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
		Name:   "Test Human",
		Handle: "test-human",
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
			{Identity: "test-human", Role: "ceo"},
			{Identity: "claude", Role: "coo"},
		},
		Collaborations: []team.Collaboration{
			{From: "coo", To: "ceo", Type: "reports_to"},
		},
	}, identityExists, roleExists))

	isolateGitConfig(t, "test-human")
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
	assert.Contains(t, ctx, "Test Human (test-human) — ceo")
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
		Name:   "Test Human",
		Handle: "test-human",
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
			{Identity: "test-human", Role: "ceo"},
			{Identity: "claude", Role: "coo"},
		},
		Collaborations: []team.Collaboration{
			{From: "coo", To: "ceo", Type: "reports_to"},
		},
	}, identityExists, roleExists))

	isolateGitConfig(t, "test-human")
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

// TestHandleSessionStart_GenerateAgentsErrorPropagates covers ethos-9ai.6
// C1: the only production caller of GenerateAgentFiles (HandleSessionStart)
// must return the wrapped error, not log-and-continue. Before the fix,
// a broken team reference (or any GenerateAgentFiles failure) was logged
// to stderr and swallowed; `ethos hook session-start` exited zero and
// `ethos doctor` had no signal to gate on.
//
// Exercise path: a valid config that names a team file that doesn't
// exist. ResolveAgent succeeds (cfg parses), store.Load(agentPersona)
// succeeds (identity exists), then GenerateAgentFiles calls
// teams.Load(teamName) which returns a not-found error. That error
// wraps as "loading team %q: %w"; HandleSessionStart's wrap
// "generating agents: %w" sits on top.
//
// The companion test TestHandleSessionStart_ResolveAgentErrorPropagates
// (ethos-dc0) covers the malformed-ethos.yaml path that was unreachable
// from HandleSessionStart before dc0 — at the time 9ai.6 r2 landed,
// resolve.ResolveAgent silently swallowed LoadRepoConfig errors and
// returned "", so the agentPersona == "" branch fell back to the human
// one-liner before GenerateAgentFiles was reached. Post-dc0 that path
// IS reachable and the dc0 test exercises it directly.
func TestHandleSessionStart_GenerateAgentsErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)
	rs := role.NewLayeredStore("", dir)
	ts := team.NewLayeredStore("", dir)

	// Human identity — needed for resolve.Resolve to succeed.
	require.NoError(t, s.Save(&identity.Identity{
		Name:   "Test Human",
		Handle: "test-human",
		Kind:   "human",
	}))
	// Agent identity — the config points at this handle, and
	// store.Load(agentPersona) must succeed so the flow reaches
	// GenerateAgentFiles.
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

	// Repo root with a config that names a team file that does NOT
	// exist on disk. ResolveAgent parses the config and returns
	// "claude"; GenerateAgentFiles parses the same config, reads
	// cfg.Team == "missing-team", calls teams.Load("missing-team"),
	// and returns a wrapped not-found error.
	repoRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755))
	puntDir := filepath.Join(repoRoot, ".punt-labs")
	require.NoError(t, os.MkdirAll(puntDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(puntDir, "ethos.yaml"),
		[]byte("agent: claude\nteam: missing-team\n"),
		0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(puntDir, "ethos"), 0o755))

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	isolateGitConfig(t, "test-human")

	deps := SessionStartDeps{
		Store:    s,
		Sessions: ss,
		Teams:    ts,
		Roles:    rs,
	}

	// Direct call — captureSessionStartOutput asserts NoError, which
	// is the exact opposite of what we want to verify here. Suppress
	// stdout so an unexpected JSON write in a regression path does
	// not leak into test output.
	oldStdout := os.Stdout
	devNull, openErr := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, openErr)
	os.Stdout = devNull
	t.Cleanup(func() {
		os.Stdout = oldStdout
		devNull.Close()
	})

	err = HandleSessionStart(bytes.NewReader([]byte(`{"session_id":"s-propagate"}`)), deps)
	require.Error(t, err,
		"HandleSessionStart must return the wrapped GenerateAgentFiles error, not swallow it")
	assert.Contains(t, err.Error(), "generating agents",
		"HandleSessionStart must wrap the downstream error with its operation context")
	assert.Contains(t, err.Error(), "loading team",
		"the wrapped chain must include GenerateAgentFiles's teams.Load wrap")
	assert.Contains(t, err.Error(), "missing-team",
		"the error must name the team that failed to load so a user can debug the config")

	// %w chain must unwrap to the inner error, not just render. A
	// regression that drops the outer wrap to %v would still produce a
	// rendered string containing the substrings above, but the unwrap
	// would return nil at depth 1. Guard against that.
	inner := errors.Unwrap(err)
	require.NotNil(t, inner,
		"HandleSessionStart wrap must use %%w; errors.Unwrap returned nil")
	assert.Contains(t, inner.Error(), "loading team",
		"depth-1 error must be GenerateAgentFiles's teams.Load wrap")
}

// TestHandleSessionStart_ResolveAgentErrorPropagates covers ethos-dc0:
// a malformed .punt-labs/ethos.yaml must now cause HandleSessionStart
// to return a non-nil error with the full wrap chain. This is the test
// case that 9ai.6 r2 could not use — at that point resolve.ResolveAgent
// silently swallowed LoadRepoConfig errors and returned "", and
// HandleSessionStart's early-return on agentPersona == "" fell back to
// the human one-liner before GenerateAgentFiles was reached.
//
// Post-dc0, ResolveAgent returns (string, error) and wraps the
// LoadRepoConfig error with the operation-noun prefix "resolve agent".
// HandleSessionStart then wraps that with the gerund prefix
// "resolving agent". Two distinct verb forms at the two layers —
// the dc0 r2 distinct-verbs invariant, matching the 9ai.6 r2
// precedent. The inner LoadRepoConfig wrap "parsing repo config:
// %w" and the innermost yaml decoder error are preserved all the
// way up.
//
// Exercise path: valid human identity; fake repo root with
// .punt-labs/ethos.yaml containing unparseable YAML ("agent: [unclosed").
// LoadRepoConfig's yaml.Unmarshal fails, LoadRepoConfig wraps it as
// "parsing repo config", ResolveAgent wraps that as "resolve agent",
// and HandleSessionStart adds the outer "resolving agent" layer.
// The final chain reads: "resolving agent: resolve agent: parsing
// repo config: yaml: ...".
func TestHandleSessionStart_ResolveAgentErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)
	rs := role.NewLayeredStore("", dir)
	ts := team.NewLayeredStore("", dir)

	// Human identity — needed for resolve.Resolve to succeed before
	// the ResolveAgent call so the test doesn't short-circuit on an
	// earlier failure mode.
	require.NoError(t, s.Save(&identity.Identity{
		Name:   "Test Human",
		Handle: "test-human",
		Kind:   "human",
	}))

	// Fake repo root with a .git dir and a malformed ethos.yaml.
	// "agent: [unclosed" is an unterminated flow sequence — the yaml
	// decoder rejects it unambiguously with "did not find expected node
	// content" or similar. Any unambiguously-broken yaml would do; this
	// shape matches the spec's suggested fixture.
	repoRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755))
	puntDir := filepath.Join(repoRoot, ".punt-labs")
	require.NoError(t, os.MkdirAll(puntDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(puntDir, "ethos.yaml"),
		[]byte("agent: [unclosed\n"),
		0o644))

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	isolateGitConfig(t, "test-human")

	deps := SessionStartDeps{
		Store:    s,
		Sessions: ss,
		Teams:    ts,
		Roles:    rs,
	}

	// Suppress stdout the same way the 9ai.6 r2 test does — no JSON
	// output is expected on the error path, but a regression that
	// writes partial JSON before returning the error would leak into
	// test output without the redirect.
	oldStdout := os.Stdout
	devNull, openErr := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, openErr)
	os.Stdout = devNull
	t.Cleanup(func() {
		os.Stdout = oldStdout
		devNull.Close()
	})

	err = HandleSessionStart(bytes.NewReader([]byte(`{"session_id":"s-dc0"}`)), deps)
	require.Error(t, err,
		"HandleSessionStart must return the wrapped ResolveAgent error, not swallow it")

	// Distinct-verbs invariant (dc0 r2, fixing the doubled-prefix
	// collision 4 of 4 reviewers flagged on round 1). The chain has
	// four layers; the outer two use distinct verb forms so a reader
	// sees context added at each step, not the same word twice:
	//
	//   ethos hook session-start:            (main.go wrapper)
	//     resolving agent:                   (HandleSessionStart, gerund)
	//       resolve agent:                   (ResolveAgent, operation noun)
	//         parsing repo config:           (LoadRepoConfig)
	//           yaml: line ...               (yaml decoder root cause)
	//
	// Matches the 9ai.6 r2 precedent: inner uses the operation noun
	// ("generate agents"), outer uses the gerund ("generating agents").
	// A future regression that collapses them back to a single form
	// fails one of the two assertions below.
	assert.Contains(t, err.Error(), "resolving agent",
		"HandleSessionStart's outer wrap (gerund) must be present")
	assert.Contains(t, err.Error(), "resolve agent",
		"ResolveAgent's inner wrap (operation noun) must be preserved by the %w chain")
	assert.Contains(t, err.Error(), "parsing repo config",
		"the wrapped chain must include LoadRepoConfig's yaml.Unmarshal wrap")

	// %w chain must unwrap — guard against a regression that drops
	// the outer wrap to %v and leaves only a rendered string.
	inner := errors.Unwrap(err)
	require.NotNil(t, inner,
		"HandleSessionStart wrap must use %%w; errors.Unwrap returned nil")
}
