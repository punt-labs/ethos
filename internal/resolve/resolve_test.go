package resolve

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/process"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testStoreWithIdentity creates a store with a single identity.
func testStoreWithIdentity(t *testing.T, id *identity.Identity) identity.IdentityStore {
	t.Helper()
	s := identity.NewStore(t.TempDir())
	require.NoError(t, s.Save(id))
	return s
}

// setGitConfig writes a minimal gitconfig to a temp file and sets
// environment variables to isolate from the real user's config.
func setGitConfig(t *testing.T, name, email string) {
	t.Helper()
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, ".gitconfig")
	content := "[user]\n"
	if name != "" {
		content += "\tname = " + name + "\n"
	}
	if email != "" {
		content += "\temail = " + email + "\n"
	}
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o644))
	t.Setenv("GIT_CONFIG_GLOBAL", configPath)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// --- Resolve tests ---

func TestResolve_IamDeclaration(t *testing.T) {
	setGitConfig(t, "unknown", "")
	t.Setenv("USER", "nobody")
	t.Setenv("ETHOS_SESSION", "") // exercise the PID walk, not an ambient env

	s := testStoreWithIdentity(t, &identity.Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human",
	})

	// Set up a session store with a roster containing our PID's persona.
	root := t.TempDir()
	ss := session.NewStore(root)

	// Use FindClaudePID to get the PID that Resolve will look up.
	pid := process.FindClaudePID()
	sessionID := "test-iam-session"
	require.NoError(t, ss.Create(sessionID,
		session.Participant{AgentID: "root", Persona: "root"},
		session.Participant{AgentID: pid, Persona: "mal", Parent: "root"},
		"", "",
	))
	require.NoError(t, ss.WriteCurrentSession(pid, sessionID))

	handle, err := Resolve(s, ss)
	require.NoError(t, err)
	assert.Equal(t, "mal", handle)
}

// TestResolve_ToleratesLegacyKeyedParticipant pins the round 2 finding: a
// session whose primary participant was written before DES-074 keys it on
// process.LegacyClaudePID (the pre-fix walk-derived PID), not the new
// preferred process.FindClaudePID (CLAUDE_PID, corroborated). A caller
// resolving "myself" via the new key alone would find no participant
// match and hard-fail (measured: `ethos whoami` on a real in-flight
// session went from resolving cleanly to `session ... has no participant
// matching "<new pid>"`) until that session ends and a fresh SessionStart
// rekeys it. resolveFromSession must fall back to the legacy key.
func TestResolve_ToleratesLegacyKeyedParticipant(t *testing.T) {
	setGitConfig(t, "unknown", "")
	t.Setenv("USER", "nobody")
	t.Setenv("ETHOS_SESSION", "")
	// Force a live, corroborating CLAUDE_PID distinct from LegacyClaudePID's
	// walk result. Using our GRANDPARENT rather than our immediate parent:
	// in an environment with no real "claude" ancestor (CI, a detached
	// process — round 2 finding), the walk's own fallback is exactly
	// os.Getppid(), so forcing CLAUDE_PID to the parent would make the two
	// coincide by accident of environment rather than exercise the
	// two-key scenario this test is about.
	parentPID := os.Getppid()
	grandparentPID, err := process.ParentPID(parentPID)
	require.NoError(t, err, "need a real grandparent to run this test")
	t.Setenv("CLAUDE_PID", strconv.Itoa(grandparentPID))

	s := testStoreWithIdentity(t, &identity.Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human",
	})

	root := t.TempDir()
	ss := session.NewStore(root)

	legacyPID := process.LegacyClaudePID()
	preferredPID := process.FindClaudePID()
	require.NotEqual(t, legacyPID, preferredPID,
		"test setup requires the legacy and preferred keys to differ")

	sessionID := "legacy-keyed-session"
	require.NoError(t, ss.Create(sessionID,
		session.Participant{AgentID: "root", Persona: "root"},
		session.Participant{AgentID: legacyPID, Persona: "mal", Parent: "root"},
		"", "",
	))
	require.NoError(t, ss.WriteCurrentSession(preferredPID, sessionID))

	handle, err := Resolve(s, ss)
	require.NoError(t, err)
	assert.Equal(t, "mal", handle, "must tolerate a participant keyed on the legacy walk-derived PID")
}

// TestResolve_ParticipantMissFallsThroughToGitOS pins the round 2, R3
// binding ruling: "a participant miss is NOT fatal; only an unresolvable
// session is." A session that resolves and loads fine, but has no
// participant matching this caller (under either the preferred or the
// legacy PID) is the ordinary state for any process that has not run
// `iam` yet — Resolve must fall through to git/OS, not error.
func TestResolve_ParticipantMissFallsThroughToGitOS(t *testing.T) {
	setGitConfig(t, "someone", "someone@example.com")
	t.Setenv("ETHOS_SESSION", "")

	s := testStoreWithIdentity(t, &identity.Identity{
		Name: "Someone", Handle: "someone", Kind: "human", GitHub: "someone",
	})

	root := t.TempDir()
	ss := session.NewStore(root)
	pid := process.FindClaudePID()
	sessionID := "no-participant-session"
	require.NoError(t, ss.Create(sessionID,
		session.Participant{AgentID: "root", Persona: "root"},
		session.Participant{AgentID: "someone-else-entirely", Persona: "not-me", Parent: "root"},
		"", "",
	))
	require.NoError(t, ss.WriteCurrentSession(pid, sessionID))

	handle, err := Resolve(s, ss)
	require.NoError(t, err, "a participant miss must not be a fatal error")
	assert.Equal(t, "someone", handle, "must fall through to the git identity")
}

func TestResolve_SessionFromEnv(t *testing.T) {
	// No current-pointer is written; discovery is via ETHOS_SESSION alone
	// (the Codex / plain-terminal path). whoami must still reflect the
	// declared persona (REC-1).
	setGitConfig(t, "unknown", "")
	t.Setenv("USER", "nobody")
	s := testStoreWithIdentity(t, &identity.Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human",
	})

	root := t.TempDir()
	ss := session.NewStore(root)
	pid := process.FindClaudePID()
	sessionID := "harness-env-session"
	require.NoError(t, ss.Create(sessionID,
		session.Participant{AgentID: "root", Persona: "root"},
		session.Participant{AgentID: pid, Persona: "mal", Parent: "root"},
		"", "",
	))
	t.Setenv("ETHOS_SESSION", sessionID)

	handle, err := Resolve(s, ss)
	require.NoError(t, err)
	assert.Equal(t, "mal", handle, "whoami must resolve the persona via ETHOS_SESSION, no PID pointer")
}

func TestResolve_SessionAgentIDParity(t *testing.T) {
	// A Codex user who exports ETHOS_AGENT_ID: iam keys the participant on
	// that value, and whoami must look it up by the same key (REC-3),
	// even though FindClaudePID would not match.
	setGitConfig(t, "unknown", "")
	t.Setenv("USER", "nobody")
	s := testStoreWithIdentity(t, &identity.Identity{
		Name: "Brian K", Handle: "bwk", Kind: "agent",
	})

	root := t.TempDir()
	ss := session.NewStore(root)
	sessionID := "harness-agentid-session"
	require.NoError(t, ss.Create(sessionID,
		session.Participant{AgentID: "root", Persona: "root"},
		session.Participant{AgentID: "bwk-codex", Persona: "bwk", Parent: "root"},
		"", "",
	))
	t.Setenv("ETHOS_SESSION", sessionID)
	t.Setenv("ETHOS_AGENT_ID", "bwk-codex")

	handle, err := Resolve(s, ss)
	require.NoError(t, err)
	assert.Equal(t, "bwk", handle, "participant lookup must honor ETHOS_AGENT_ID")
}

func TestSessionID(t *testing.T) {
	root := t.TempDir()
	ss := session.NewStore(root)

	t.Run("env wins over walk", func(t *testing.T) {
		t.Setenv("ETHOS_SESSION", "env-session")
		sid, source, err := SessionID(ss)
		require.NoError(t, err)
		assert.Equal(t, "env-session", sid)
		assert.Equal(t, SessionSourceEnv, source)
	})

	t.Run("walk fallback", func(t *testing.T) {
		t.Setenv("ETHOS_SESSION", "")
		// This suite usually runs inside a real Claude Code session, where
		// CLAUDE_PID is itself set — strip it so this genuinely exercises
		// the walk this subtest is named for, not the env+corroboration
		// path "env wins over walk" already covers.
		t.Setenv("CLAUDE_PID", "")
		pid := process.FindClaudePID()
		require.NoError(t, ss.WriteCurrentSession(pid, "walk-session"))
		sid, source, err := SessionID(ss)
		require.NoError(t, err)
		assert.Equal(t, "walk-session", sid)
		assert.Equal(t, "walk", source)
	})

	t.Run("neither resolves, under Claude Code", func(t *testing.T) {
		// DES-074: a session that WAS expected (running under Claude Code)
		// but cannot be identified is a named error, never a silent ("",
		// "") pair. Force the branch deterministically -- this suite's
		// ambient CLAUDE_PID happens to make it true today, but a real CI
		// run (no claude ancestor at all) would make it false and this
		// assertion would silently stop testing what it claims to.
		t.Setenv("ETHOS_SESSION", "")
		old := UnderClaudeCode
		UnderClaudeCode = func() bool { return true }
		t.Cleanup(func() { UnderClaudeCode = old })
		empty := session.NewStore(t.TempDir())
		sid, source, err := SessionID(empty)
		assert.Empty(t, sid)
		assert.Empty(t, source)
		assert.ErrorIs(t, err, ErrNoSession)
	})

	t.Run("neither resolves, not under Claude Code", func(t *testing.T) {
		// The other DES-074 branch: no session was ever expected here, so
		// SessionID returns the distinct, named ErrNotUnderClaudeCode --
		// never nil (round 2: a nil error here was indistinguishable from
		// "resolved" to a caller checking only `err == nil`).
		t.Setenv("ETHOS_SESSION", "")
		old := UnderClaudeCode
		UnderClaudeCode = func() bool { return false }
		t.Cleanup(func() { UnderClaudeCode = old })
		empty := session.NewStore(t.TempDir())
		sid, source, err := SessionID(empty)
		assert.Empty(t, sid)
		assert.Empty(t, source)
		assert.ErrorIs(t, err, ErrNotUnderClaudeCode)
	})
}

// TestSessionID_ConcurrentSessionsDoNotCollide pins the ethos-vqwn fix: two
// concurrent Claude Code sessions that shared a topmost-ancestor PID under
// the pre-DES-074 walk (six rosters across four repos measured to PID
// 518779 on 2026-09-07) must resolve to their OWN session each once every
// session keys its pointer file on its own Claude process's PID instead. A
// table test with injected PIDs and a temp store root stands in for two
// real Claude Code processes, per the mission's own guidance.
func TestSessionID_ConcurrentSessionsDoNotCollide(t *testing.T) {
	// Round 2 finding: a version of this test using literal string keys
	// ("11111"/"22222") proves the STORE does not collide on distinct
	// keys — true of any key-value store, and true before this fix too.
	// ethos-vqwn was never "the store collides on distinct keys"; it was
	// "FindClaudePID returns the SAME key for different sessions" (the
	// pre-fix topmost-ancestor walk collapsing every concurrent session
	// onto the shared "claude daemon run" PID). This version drives the
	// keys through the actual mechanism: two simulated sessions, each
	// resolving its OWN CLAUDE_PID (our real parent and grandparent —
	// both genuinely live, always-distinct ancestors), proving
	// FindClaudePID itself gives each session a distinct key AND that the
	// store keeps them separate once it does.
	t.Setenv("ETHOS_SESSION", "")
	root := t.TempDir()
	ss := session.NewStore(root)

	parentPID := os.Getppid()
	grandparentPID, err := process.ParentPID(parentPID)
	require.NoError(t, err, "need a real grandparent to run this test")

	t.Setenv("CLAUDE_PID", strconv.Itoa(parentPID))
	sessionAPID := process.FindClaudePID()
	t.Setenv("CLAUDE_PID", strconv.Itoa(grandparentPID))
	sessionBPID := process.FindClaudePID()
	require.NotEqual(t, sessionAPID, sessionBPID,
		"two sessions with distinct owning processes must resolve distinct FindClaudePID keys")

	require.NoError(t, ss.WriteCurrentSession(sessionAPID, "session-repo-a"))
	require.NoError(t, ss.WriteCurrentSession(sessionBPID, "session-repo-b"))

	idA, err := ss.ReadCurrentSession(sessionAPID)
	require.NoError(t, err)
	assert.Equal(t, "session-repo-a", idA, "session A's pointer must not be clobbered by session B's write")

	idB, err := ss.ReadCurrentSession(sessionBPID)
	require.NoError(t, err)
	assert.Equal(t, "session-repo-b", idB)
}

// TestSessionID_ResolvesAcrossSessionChange pins the sync.Once removal
// (tree.go, formerly line 31): a long-lived process (ethos serve) must
// resolve the SECOND session after a session change at a stable PID — e.g.
// surviving a Claude Code /clear that starts a fresh session under the same
// owning process — not keep answering with the first session it ever saw.
func TestSessionID_ResolvesAcrossSessionChange(t *testing.T) {
	t.Setenv("ETHOS_SESSION", "")
	root := t.TempDir()
	ss := session.NewStore(root)
	pid := process.FindClaudePID()

	require.NoError(t, ss.WriteCurrentSession(pid, "session-before-clear"))
	first, _, err := SessionID(ss)
	require.NoError(t, err)
	assert.Equal(t, "session-before-clear", first)

	require.NoError(t, ss.WriteCurrentSession(pid, "session-after-clear"))
	second, _, err := SessionID(ss)
	require.NoError(t, err)
	assert.Equal(t, "session-after-clear", second, "a cached resolver would still return the first session")
}

// TestSessionID_UnresolvableIsNamedError pins the DES-074 fail-loud
// contract directly: with no ETHOS_SESSION and no pointer file for this
// process's Claude PID, SessionID must return ErrNoSession — a caller that
// requires a session (iam, mission claim/release) then refuses with a
// non-zero exit rather than silently trying some other identity. This is
// the "session was expected" branch, forced deterministically since this
// suite's ambient CLAUDE_PID cannot be relied on to make it true in every
// environment (a real CI run has no claude ancestor at all).
func TestSessionID_UnresolvableIsNamedError(t *testing.T) {
	t.Setenv("ETHOS_SESSION", "")
	old := UnderClaudeCode
	UnderClaudeCode = func() bool { return true }
	t.Cleanup(func() { UnderClaudeCode = old })
	ss := session.NewStore(t.TempDir())

	sid, source, err := SessionID(ss)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoSession)
	assert.Empty(t, sid)
	assert.Empty(t, source)
	assert.Contains(t, err.Error(), "ethos session start", "the error must name the remedy")
}

// TestSessionID_SurfacesRealReadCauseNotJustGenericRemedy pins round 2,
// R6: retryReadCurrentSession's accumulated error must not be discarded
// in favor of the bare ErrNoSession sentinel. "set ETHOS_SESSION" is the
// right remedy for the common case (no pointer file at all), but a
// determinable, different cause — here, the pointer "file" is actually a
// directory — is not fixed by that remedy and must be visible in the
// message. errors.Is(err, ErrNoSession) must still hold, since callers
// pattern-match on it.
func TestSessionID_SurfacesRealReadCauseNotJustGenericRemedy(t *testing.T) {
	t.Setenv("ETHOS_SESSION", "")
	old := UnderClaudeCode
	UnderClaudeCode = func() bool { return true }
	t.Cleanup(func() { UnderClaudeCode = old })

	root := t.TempDir()
	ss := session.NewStore(root)
	pid := process.FindClaudePID()
	// Force a real, determinable read failure distinct from "not found":
	// the pointer "file" is a directory.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sessions", "current", pid), 0o700))

	_, _, err := SessionID(ss)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoSession)
	assert.NotEmpty(t, err.Error())
	// The real cause (a directory where a file was expected) must be
	// visible somewhere in the chain, not swallowed by the generic remedy
	// text alone.
	assert.Contains(t, err.Error(), pid, "the failing path/PID should be traceable in the message")
}

// TestSessionID_RetriesPointerFileRace pins DES-074 point 5: a consumer
// can start before SessionStart finishes writing the pointer file. The
// race is simulated by writing it from a goroutine partway through the
// retry window; a resolver with no retry would see the first read miss
// and return ErrNoSession instead of the session that landed a few
// milliseconds later.
func TestSessionID_RetriesPointerFileRace(t *testing.T) {
	t.Setenv("ETHOS_SESSION", "")
	old := UnderClaudeCode
	UnderClaudeCode = func() bool { return true } // the retry only fires under Claude Code
	t.Cleanup(func() { UnderClaudeCode = old })

	root := t.TempDir()
	ss := session.NewStore(root)
	pid := process.FindClaudePID()

	go func() {
		time.Sleep(pointerRetryDelay / 2)
		_ = ss.WriteCurrentSession(pid, "raced-session")
	}()

	sid, source, err := SessionID(ss)
	require.NoError(t, err)
	assert.Equal(t, "raced-session", sid)
	assert.Equal(t, "walk", source)
}

// TestSessionID_RetrySkippedWhenNotUnderClaudeCode pins the other half of
// point 5: the retry must not fire when no session was ever expected --
// only the "session was expected but unresolvable" branch pays the retry
// latency. A missing goroutine writer here means a passing retry would
// have to be spurious; this test relies on the immediate ErrNoSession-free
// return alone; timing is asserted structurally, not by wall-clock bound,
// to avoid a flaky CI threshold.
func TestSessionID_RetrySkippedWhenNotUnderClaudeCode(t *testing.T) {
	t.Setenv("ETHOS_SESSION", "")
	old := UnderClaudeCode
	UnderClaudeCode = func() bool { return false }
	t.Cleanup(func() { UnderClaudeCode = old })

	ss := session.NewStore(t.TempDir())
	start := time.Now()
	sid, source, err := SessionID(ss)
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, ErrNotUnderClaudeCode)
	assert.Empty(t, sid)
	assert.Empty(t, source)
	assert.Less(t, elapsed, pointerRetryDelay,
		"not-under-Claude-Code must return immediately, not pay the under-Claude-Code retry latency")
}

// TestSessionID_InvariantHoldsAtMinimalRetryBudget pins mission 005
// finding D: SessionID's own doc comment claims "err == nil if and only
// if id != ”" as a mechanical invariant, but before this fix that was
// only true at the shipped pointerRetryAttempts value of 10.
// retryReadCurrentSession's loop runs pointerRetryAttempts-1 times, so at
// 1 (or less) the loop body never executes and lastErr, seeded to nil,
// was returned unchanged -- silently reporting a resolved id=="" alongside
// err==nil, breaking the invariant the whole design leans on. Driving the
// var to 1 here proves the fix holds structurally, not merely at 10.
func TestSessionID_InvariantHoldsAtMinimalRetryBudget(t *testing.T) {
	t.Setenv("ETHOS_SESSION", "")
	old := UnderClaudeCode
	UnderClaudeCode = func() bool { return true }
	t.Cleanup(func() { UnderClaudeCode = old })
	oldAttempts := pointerRetryAttempts
	pointerRetryAttempts = 1
	t.Cleanup(func() { pointerRetryAttempts = oldAttempts })

	ss := session.NewStore(t.TempDir())
	sid, source, err := SessionID(ss)

	require.Error(t, err, "err must be non-nil whenever id is empty, regardless of the retry budget")
	assert.ErrorIs(t, err, ErrNoSession)
	assert.Empty(t, sid)
	assert.Empty(t, source)
}

func TestResolve_GitNameMatchesGitHub(t *testing.T) {
	setGitConfig(t, "mal-github", "")
	t.Setenv("USER", "nobody")
	s := testStoreWithIdentity(t, &identity.Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human", GitHub: "mal-github",
	})

	handle, err := Resolve(s, nil)
	require.NoError(t, err)
	assert.Equal(t, "mal", handle)
}

func TestResolve_GitEmailMatchesEmail(t *testing.T) {
	setGitConfig(t, "unknown-user", "mal@serenity.ship")
	t.Setenv("USER", "nobody")
	s := testStoreWithIdentity(t, &identity.Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human", Email: "mal@serenity.ship",
	})

	handle, err := Resolve(s, nil)
	require.NoError(t, err)
	assert.Equal(t, "mal", handle)
}

// TestResolve_AmbiguousEmailFailsLoud pins ethos-u4kq at the level the
// operator meets it: `ethos whoami` with two identities sharing the
// git email must refuse and name both, not answer with an arbitrary
// one. The collision is reachable in an ordinary repo — `ethos setup`
// defaults a human's email to git user.email.
func TestResolve_AmbiguousEmailFailsLoud(t *testing.T) {
	setGitConfig(t, "unknown-user", "crew@serenity.ship")
	t.Setenv("USER", "nobody")
	t.Setenv("ETHOS_SESSION", "")

	s := identity.NewStore(t.TempDir())
	require.NoError(t, s.Save(&identity.Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human", Email: "crew@serenity.ship",
	}))
	require.NoError(t, s.Save(&identity.Identity{
		Name: "Zoe Washburne", Handle: "zoe", Kind: "human", Email: "crew@serenity.ship",
	}))

	handle, err := Resolve(s, nil)
	require.Error(t, err, "an ambiguous email must not resolve to an arbitrary identity")
	assert.Empty(t, handle)
	assert.Contains(t, err.Error(), "ambiguous identity: 2 matches")
	assert.Contains(t, err.Error(), "mal, zoe")
}

func TestResolve_OSUserMatchesHandle(t *testing.T) {
	setGitConfig(t, "unknown-user", "unknown@example.com")
	t.Setenv("USER", "mal")
	s := testStoreWithIdentity(t, &identity.Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human",
	})

	handle, err := Resolve(s, nil)
	require.NoError(t, err)
	assert.Equal(t, "mal", handle)
}

func TestResolve_NoMatch(t *testing.T) {
	setGitConfig(t, "unknown-user", "unknown@example.com")
	t.Setenv("USER", "nobody")
	s := testStoreWithIdentity(t, &identity.Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human",
	})

	_, err := Resolve(s, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown-user")
	assert.Contains(t, err.Error(), "nobody")
}

// A no-match under repo-only must say WHY the global store — which does
// hold the caller's identity — went unconsulted. Without the hint the
// user sees a generic "no identity matches" and has no way to connect it
// to the repo's resolution setting (DES-057 Part A).
func TestResolve_NoMatchHintsRepoOnly(t *testing.T) {
	setGitConfig(t, "unknown-user", "unknown@example.com")
	t.Setenv("USER", "nobody")

	repo, global := identity.NewStore(t.TempDir()), identity.NewStore(t.TempDir())
	require.NoError(t, global.Save(&identity.Identity{
		Name: "Mal Reynolds", Handle: "nobody", Kind: "human",
	}))

	layered := identity.NewLayeredStoreWithBundle(repo, nil, global, false)
	handle, err := Resolve(layered, nil)
	require.NoError(t, err, "layered still finds the global identity")
	assert.Equal(t, "nobody", handle)

	repoOnly := identity.NewLayeredStoreWithBundle(repo, nil, global, true)
	_, err = Resolve(repoOnly, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolution: repo-only")
	assert.Contains(t, err.Error(), "ethos vendor")
}

func TestResolve_PriorityOrder(t *testing.T) {
	// git user.name should take priority over $USER.
	setGitConfig(t, "mal-github", "")
	t.Setenv("USER", "mal")
	s := identity.NewStore(t.TempDir())
	require.NoError(t, s.Save(&identity.Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human", GitHub: "mal-github",
	}))
	require.NoError(t, s.Save(&identity.Identity{
		Name: "Other Person", Handle: "other", Kind: "human", GitHub: "other-github",
	}))

	handle, err := Resolve(s, nil)
	require.NoError(t, err)
	assert.Equal(t, "mal", handle)
}

// --- LoadRepoConfig tests ---

func TestLoadRepoConfig(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, root string)
		wantAgent  string
		wantTeam   string
		wantBundle string
		wantNil    bool
		wantErr    bool
	}{
		{
			name: "new path only",
			setup: func(t *testing.T, root string) {
				t.Helper()
				dir := filepath.Join(root, ".punt-labs")
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(dir, "ethos.yaml"),
					[]byte("agent: claude\nteam: engineering\nactive_bundle: gstack\n"), 0o644))
			},
			wantAgent:  "claude",
			wantTeam:   "engineering",
			wantBundle: "gstack",
		},
		{
			name: "old path only",
			setup: func(t *testing.T, root string) {
				t.Helper()
				dir := filepath.Join(root, ".punt-labs", "ethos")
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(dir, "config.yaml"),
					[]byte("agent: legacy-agent\n"), 0o644))
			},
			wantAgent: "legacy-agent",
		},
		{
			name: "both present new wins",
			setup: func(t *testing.T, root string) {
				t.Helper()
				puntDir := filepath.Join(root, ".punt-labs")
				require.NoError(t, os.MkdirAll(puntDir, 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(puntDir, "ethos.yaml"),
					[]byte("agent: new-agent\n"), 0o644))
				ethosDir := filepath.Join(puntDir, "ethos")
				require.NoError(t, os.MkdirAll(ethosDir, 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(ethosDir, "config.yaml"),
					[]byte("agent: old-agent\n"), 0o644))
			},
			wantAgent: "new-agent",
		},
		{
			name:    "neither present",
			setup:   func(t *testing.T, root string) { t.Helper() },
			wantNil: true,
		},
		{
			name: "invalid yaml",
			setup: func(t *testing.T, root string) {
				t.Helper()
				dir := filepath.Join(root, ".punt-labs")
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(dir, "ethos.yaml"),
					[]byte(":\n  :\n    - [invalid"), 0o644))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)

			cfg, err := LoadRepoConfig(root)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, cfg)
				return
			}
			require.NotNil(t, cfg)
			assert.Equal(t, tt.wantAgent, cfg.Agent)
			assert.Equal(t, tt.wantTeam, cfg.Team)
			assert.Equal(t, tt.wantBundle, cfg.ActiveBundle)
		})
	}
}

func TestLoadRepoConfig_PermissionError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	root := t.TempDir()
	dir := filepath.Join(root, ".punt-labs")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	f := filepath.Join(dir, "ethos.yaml")
	require.NoError(t, os.WriteFile(f, []byte("agent: x\n"), 0o644))
	require.NoError(t, os.Chmod(f, 0o000))
	t.Cleanup(func() { os.Chmod(f, 0o644) }) //nolint:errcheck

	cfg, err := LoadRepoConfig(root)
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "reading")
}

// --- ResolveAgent tests ---

func TestResolveAgent_ConfigSet(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".punt-labs")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "ethos.yaml"),
		[]byte("agent: claude\n"), 0o644))

	handle, err := ResolveAgent(root)
	require.NoError(t, err)
	assert.Equal(t, "claude", handle)
}

func TestResolveAgent_LegacyFallback(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("agent: legacy\n"), 0o644))

	handle, err := ResolveAgent(root)
	require.NoError(t, err)
	assert.Equal(t, "legacy", handle)
}

func TestResolveAgent_NoConfig(t *testing.T) {
	handle, err := ResolveAgent(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "", handle)
}

func TestResolveAgent_EmptyRoot(t *testing.T) {
	handle, err := ResolveAgent("")
	require.NoError(t, err)
	assert.Equal(t, "", handle)
}

func TestResolveAgent_NoAgentField(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".punt-labs")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "ethos.yaml"),
		[]byte("# empty config\n"), 0o644))

	handle, err := ResolveAgent(root)
	require.NoError(t, err)
	assert.Equal(t, "", handle)
}

// TestResolveAgent_MalformedYAML verifies that a .punt-labs/ethos.yaml
// that exists but cannot be parsed produces an error containing both
// the "resolve agent" outer wrap and the inner "parsing repo config"
// wrap from LoadRepoConfig. Pre-dc0, this path silently logged to
// stderr and returned the empty string, making every caller treat
// "broken config" and "not configured" as the same case.
func TestResolveAgent_MalformedYAML(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".punt-labs")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "ethos.yaml"),
		[]byte("agent: [unclosed\n"), 0o644))

	handle, err := ResolveAgent(root)
	require.Error(t, err)
	assert.Equal(t, "", handle)
	assert.Contains(t, err.Error(), "resolve agent",
		"outer wrap must name the operation")
	assert.Contains(t, err.Error(), "parsing repo config",
		"inner wrap from LoadRepoConfig must be preserved")
}

// TestResolveAgent_PermissionError verifies the non-parse read-error
// path. Uses the same skip-as-root pattern as
// TestLoadRepoConfig_PermissionError — root bypasses file mode bits,
// so the chmod 0o000 has no effect and the test would fail spuriously.
func TestResolveAgent_PermissionError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	root := t.TempDir()
	dir := filepath.Join(root, ".punt-labs")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	f := filepath.Join(dir, "ethos.yaml")
	require.NoError(t, os.WriteFile(f, []byte("agent: claude\n"), 0o644))
	require.NoError(t, os.Chmod(f, 0o000))
	t.Cleanup(func() { os.Chmod(f, 0o644) }) //nolint:errcheck

	handle, err := ResolveAgent(root)
	require.Error(t, err)
	assert.Equal(t, "", handle)
	assert.Contains(t, err.Error(), "resolve agent")
	assert.Contains(t, err.Error(), "reading")
}

// --- ResolveTeam tests ---

func TestResolveTeam(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantTeam string
	}{
		{"set", "team: engineering\n", "engineering"},
		{"empty", "agent: claude\n", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".punt-labs")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, "ethos.yaml"),
				[]byte(tt.yaml), 0o644))

			team, err := ResolveTeam(root)
			require.NoError(t, err)
			assert.Equal(t, tt.wantTeam, team)
		})
	}
}

func TestResolveTeam_MissingConfig(t *testing.T) {
	team, err := ResolveTeam(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "", team)
}

func TestResolveTeam_EmptyRoot(t *testing.T) {
	team, err := ResolveTeam("")
	require.NoError(t, err)
	assert.Equal(t, "", team)
}

// TestResolveTeam_MalformedYAML mirrors TestResolveAgent_MalformedYAML
// for the team path. Same wrap chain, different outer prefix:
// "resolve team" instead of "resolve agent".
func TestResolveTeam_MalformedYAML(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".punt-labs")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "ethos.yaml"),
		[]byte("team: [unclosed\n"), 0o644))

	team, err := ResolveTeam(root)
	require.Error(t, err)
	assert.Equal(t, "", team)
	assert.Contains(t, err.Error(), "resolve team",
		"outer wrap must name the operation")
	assert.Contains(t, err.Error(), "parsing repo config",
		"inner wrap from LoadRepoConfig must be preserved")
}

// TestResolveTeam_PermissionError mirrors TestResolveAgent_PermissionError
// for the team path.
func TestResolveTeam_PermissionError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	root := t.TempDir()
	dir := filepath.Join(root, ".punt-labs")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	f := filepath.Join(dir, "ethos.yaml")
	require.NoError(t, os.WriteFile(f, []byte("team: engineering\n"), 0o644))
	require.NoError(t, os.Chmod(f, 0o000))
	t.Cleanup(func() { os.Chmod(f, 0o644) }) //nolint:errcheck

	team, err := ResolveTeam(root)
	require.Error(t, err)
	assert.Equal(t, "", team)
	assert.Contains(t, err.Error(), "resolve team")
	assert.Contains(t, err.Error(), "reading")
}

// --- ResolveActiveBundle tests ---

func TestResolveActiveBundle(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantName string
	}{
		{"set", "active_bundle: gstack\n", "gstack"},
		{"empty", "agent: claude\n", ""},
		{"with other fields", "agent: claude\nteam: eng\nactive_bundle: punt-labs\n", "punt-labs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".punt-labs")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, "ethos.yaml"),
				[]byte(tt.yaml), 0o644))

			got, err := ResolveActiveBundle(root)
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, got)
		})
	}
}

func TestResolveActiveBundle_MissingConfig(t *testing.T) {
	got, err := ResolveActiveBundle(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestResolveActiveBundle_EmptyRoot(t *testing.T) {
	got, err := ResolveActiveBundle("")
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestResolveActiveBundle_MalformedYAML(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".punt-labs")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "ethos.yaml"),
		[]byte("active_bundle: [unclosed\n"), 0o644))

	got, err := ResolveActiveBundle(root)
	require.Error(t, err)
	assert.Equal(t, "", got)
	assert.Contains(t, err.Error(), "resolve active bundle")
	assert.Contains(t, err.Error(), "parsing repo config")
}

// --- ResolveResolution tests (DES-057) ---

func TestResolveResolution(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    string
		wantErr bool
	}{
		{name: "unset means layered", yaml: "agent: claude\n", want: ResolutionLayered},
		{name: "explicit layered", yaml: "resolution: layered\n", want: ResolutionLayered},
		{name: "repo-only", yaml: "resolution: repo-only\n", want: ResolutionRepoOnly},
		{
			name: "alongside other fields",
			yaml: "agent: claude\nactive_bundle: punt-labs\nresolution: repo-only\n",
			want: ResolutionRepoOnly,
		},
		// A typo must not degrade quietly to layered: leaving the global
		// fallback in place is exactly what repo-only exists to remove.
		{name: "typo is an error", yaml: "resolution: repo_only\n", want: ResolutionLayered, wantErr: true},
		{name: "malformed yaml", yaml: "resolution: [unclosed\n", want: ResolutionLayered, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".punt-labs")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, "ethos.yaml"), []byte(tt.yaml), 0o644))

			got, err := ResolveResolution(root)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "resolve resolution")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveResolution_NoRepoOrConfig(t *testing.T) {
	got, err := ResolveResolution("")
	require.NoError(t, err)
	assert.Equal(t, ResolutionLayered, got)

	got, err = ResolveResolution(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, ResolutionLayered, got)
}

// --- FindRepoRoot tests ---

func TestFindRepoRoot_FindsGitDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	subdir := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(subdir, 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(subdir))

	assert.Equal(t, root, FindRepoRoot())
}

func TestFindRepoRoot_NoGitDir(t *testing.T) {
	dir := t.TempDir()
	origDir, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(dir))

	// May find a .git above the temp dir on some systems.
	// The important thing is it doesn't panic or error.
	result := FindRepoRoot()
	if result != "" {
		// Found a .git somewhere above — valid on dev machines.
		_, err := os.Stat(filepath.Join(result, ".git"))
		assert.NoError(t, err)
	}
}

// TestStoreRepoRoot_LinkedWorktree pins the ethos-yofr fix: from inside a
// linked git worktree, StoreRepoRoot (and FindRepoEthosRoot, which rides on
// it) resolves the MAIN work tree that holds .punt-labs/ethos, while
// FindRepoRoot keeps returning the worktree for per-checkout state.
func TestStoreRepoRoot_LinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	main := filepath.Join(base, "main")
	require.NoError(t, os.MkdirAll(main, 0o755))

	gitRepo(t, main)

	// The repo-layer store the worktree must resolve to.
	ethosRoot := filepath.Join(main, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(ethosRoot, 0o755))

	wt := filepath.Join(base, "wt")
	runGit(t, main, "worktree", "add", wt)

	chdir(t, wt)

	assert.Equal(t, realpath(t, main), realpath(t, StoreRepoRoot()),
		"the store must resolve to the main work tree that holds .punt-labs/ethos")
	assert.Equal(t, realpath(t, ethosRoot), realpath(t, FindRepoEthosRoot()),
		"FindRepoEthosRoot rides on StoreRepoRoot, so the repo store resolves from the worktree")
	assert.Equal(t, realpath(t, wt), realpath(t, FindRepoRoot()),
		"FindRepoRoot stays worktree-local for per-checkout state")
}

// TestRepoRoot_EnvOverrideValid pins that a well-formed ETHOS_REPO_ROOT
// forces the root for every resolver: an existing directory holding a
// .punt-labs/ethos store.
func TestRepoRoot_EnvOverrideValid(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".punt-labs", "ethos"), 0o755))
	t.Setenv("ETHOS_REPO_ROOT", dir)
	assert.Equal(t, dir, FindRepoRoot())
	assert.Equal(t, dir, StoreRepoRoot())
	assert.Equal(t, dir, EnvRepoRoot())
}

// TestRepoRoot_EnvOverrideInvalid pins the SFH F1 fix: an override that
// lies — a path that does not exist, or (for the store) one with no
// .punt-labs/ethos — is refused loudly and never returned, so it cannot
// silently redirect writes to the wrong tree.
func TestRepoRoot_EnvOverrideInvalid(t *testing.T) {
	t.Run("nonexistent path is refused", func(t *testing.T) {
		bogus := filepath.Join(t.TempDir(), "does-not-exist")
		t.Setenv("ETHOS_REPO_ROOT", bogus)
		out := captureStderr(t, func() {
			assert.Equal(t, "", StoreRepoRoot())
			assert.Equal(t, "", FindRepoRoot())
		})
		assert.Contains(t, out, bogus)
		assert.Contains(t, out, "refusing to use it")
	})

	t.Run("no store is refused by StoreRepoRoot but accepted by FindRepoRoot", func(t *testing.T) {
		dir := t.TempDir() // exists, but has no .punt-labs/ethos
		t.Setenv("ETHOS_REPO_ROOT", dir)
		out := captureStderr(t, func() {
			assert.Equal(t, "", StoreRepoRoot(),
				"StoreRepoRoot requires a .punt-labs/ethos store")
			assert.Equal(t, dir, FindRepoRoot(),
				"FindRepoRoot only requires the directory to exist (per-checkout state)")
		})
		assert.Contains(t, out, dir)
		assert.Contains(t, out, ".punt-labs")
	})
}

// TestStoreRepoRoot_StaleWorktreeWarns pins the SFH F2 fix: when a
// worktree's git dir is gone (the main repo moved or was deleted), the
// resolver warns and falls back to the worktree store rather than silently
// treating the stale pointer as a clean submodule.
func TestStoreRepoRoot_StaleWorktreeWarns(t *testing.T) {
	dir := t.TempDir()
	// A .git file pointing at a git dir that does not exist — the shape of a
	// worktree whose main repo has moved. manualStoreRoot handles this
	// directly; call it so the test does not depend on git's own behavior.
	gone := filepath.Join(t.TempDir(), "gone", ".git", "worktrees", "wt")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"),
		[]byte("gitdir: "+gone+"\n"), 0o644))

	out := captureStderr(t, func() {
		assert.Equal(t, realpath(t, dir), realpath(t, manualStoreRoot(dir)),
			"a stale worktree falls back to its own store")
	})
	assert.Contains(t, out, "may have moved")
	assert.Contains(t, out, gone)
}

// TestStoreRepoRoot_SubmoduleWorktreeStaysOut pins the SFH F4 fix: a common
// dir that is not a .git directory (e.g. a submodule's .git/modules/<name>)
// must not resolve to a bogus root inside .git — gitStoreRoot keeps the
// worktree.
func TestStoreRepoRoot_SubmoduleWorktreeStaysOut(t *testing.T) {
	// gitStoreRoot's decision hinges on filepath.Base(common). A common dir
	// under .git/modules has a basename that is the module name, not ".git",
	// so the resolver must decline to take its parent. manualStoreRoot
	// applies the same guard; exercise it with a commondir that resolves to
	// a modules dir.
	dir := t.TempDir()
	// The module admin dir lives under a super-repo's .git/modules, not under
	// the worktree — the worktree's .git is a file pointing at it.
	admin := filepath.Join(t.TempDir(), "super", ".git", "modules", "sub")
	require.NoError(t, os.MkdirAll(admin, 0o755))
	// commondir points at the module admin dir itself (basename "sub").
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(".\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"),
		[]byte("gitdir: "+admin+"\n"), 0o644))

	got := manualStoreRoot(dir)
	assert.Equal(t, realpath(t, dir), realpath(t, got),
		"a non-.git common dir must not resolve into .git/modules")
	assert.NotContains(t, got, ".git",
		"the resolved root must not sit inside a .git path")
}

// realpath resolves symlinks so a comparison holds on macOS, where
// t.TempDir() lives under /var → /private/var and git reports the resolved
// form while os.Getwd preserves the symlink.
func realpath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}

// gitRepo initializes a git repo in dir with a first commit, so worktree
// and common-dir resolution have something to resolve.
func gitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "t")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init")
}

// chdir changes to dir and restores the cwd at test end.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(dir))
}

// captureStderr redirects os.Stderr for the duration of fn and returns what
// was written, so a test can assert on the loud warnings the resolvers emit.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	w.Close()
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.String()
}

// --- GitConfig tests ---

func TestGitConfig_ReadsValue(t *testing.T) {
	setGitConfig(t, "test-user", "test@example.com")

	assert.Equal(t, "test-user", GitConfig("user.name"))
	assert.Equal(t, "test@example.com", GitConfig("user.email"))
}

func TestGitConfig_MissingKey(t *testing.T) {
	setGitConfig(t, "", "")
	assert.Equal(t, "", GitConfig("user.name"))
}

// --- parseRepoName tests ---

func TestParseRepoName(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"HTTPS", "https://github.com/punt-labs/ethos.git", "punt-labs/ethos"},
		{"HTTPS no .git", "https://github.com/punt-labs/ethos", "punt-labs/ethos"},
		{"SSH", "git@github.com:punt-labs/ethos.git", "punt-labs/ethos"},
		{"SSH no .git", "git@github.com:punt-labs/ethos", "punt-labs/ethos"},
		{"malformed SSH no slash", "git@github.com:bareword", ""},
		{"bare name", "bareword", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseRepoName(tt.url))
		})
	}
}

// --- RepoName tests ---

func TestRepoName(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"HTTPS URL", "https://github.com/punt-labs/ethos.git", "punt-labs/ethos"},
		{"SSH URL", "git@github.com:punt-labs/ethos.git", "punt-labs/ethos"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			origDir, err := os.Getwd()
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.Chdir(origDir) })
			require.NoError(t, os.Chdir(dir))

			// Isolate git config.
			setGitConfig(t, "", "")

			runGit(t, dir, "init")
			runGit(t, dir, "remote", "add", "origin", tt.url)

			assert.Equal(t, tt.want, RepoName())
		})
	}
}

func TestRepoName_NoRemote(t *testing.T) {
	dir := t.TempDir()
	origDir, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(dir))

	setGitConfig(t, "", "")
	runGit(t, dir, "init")

	assert.Equal(t, "", RepoName())
}

// --- ResolveMaxDelegationDepth tests (DES-054 v5) ---

func TestResolveMaxDelegationDepth(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, root string) string // returns repoRoot to pass in
		def     int
		want    int
		wantErr bool
	}{
		{
			name: "no repo root returns default",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				return ""
			},
			def:  16,
			want: 16,
		},
		{
			name: "no config returns default",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				return root
			},
			def:  16,
			want: 16,
		},
		{
			name: "config without field returns default",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				dir := filepath.Join(root, ".punt-labs")
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(dir, "ethos.yaml"),
					[]byte("agent: claude\n"), 0o644))
				return root
			},
			def:  16,
			want: 16,
		},
		{
			name: "explicit value overrides default",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				dir := filepath.Join(root, ".punt-labs")
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(dir, "ethos.yaml"),
					[]byte("max_delegation_depth: 4\n"), 0o644))
				return root
			},
			def:  16,
			want: 4,
		},
		{
			name: "zero in config means default",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				dir := filepath.Join(root, ".punt-labs")
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(dir, "ethos.yaml"),
					[]byte("max_delegation_depth: 0\n"), 0o644))
				return root
			},
			def:  16,
			want: 16,
		},
		{
			name: "negative value surfaces as error",
			setup: func(t *testing.T, root string) string {
				t.Helper()
				dir := filepath.Join(root, ".punt-labs")
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(dir, "ethos.yaml"),
					[]byte("max_delegation_depth: -3\n"), 0o644))
				return root
			},
			def:     16,
			want:    16,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := tt.setup(t, t.TempDir())
			got, err := ResolveMaxDelegationDepth(root, tt.def)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// runGit runs a git command in the given directory.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}
