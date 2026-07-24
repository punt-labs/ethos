package resolve

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
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
		sid, source := SessionID(ss)
		assert.Equal(t, "env-session", sid)
		assert.Equal(t, SessionSourceEnv, source)
	})

	t.Run("walk fallback", func(t *testing.T) {
		t.Setenv("ETHOS_SESSION", "")
		pid := process.FindClaudePID()
		require.NoError(t, ss.WriteCurrentSession(pid, "walk-session"))
		sid, source := SessionID(ss)
		assert.Equal(t, "walk-session", sid)
		assert.Equal(t, "walk", source)
	})

	t.Run("neither resolves", func(t *testing.T) {
		t.Setenv("ETHOS_SESSION", "")
		empty := session.NewStore(t.TempDir())
		sid, source := SessionID(empty)
		assert.Empty(t, sid)
		assert.Empty(t, source)
	})
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
