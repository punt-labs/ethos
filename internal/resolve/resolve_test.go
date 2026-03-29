package resolve

import (
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
		name      string
		setup     func(t *testing.T, root string)
		wantAgent string
		wantTeam  string
		wantNil   bool
		wantErr   bool
	}{
		{
			name: "new path only",
			setup: func(t *testing.T, root string) {
				t.Helper()
				dir := filepath.Join(root, ".punt-labs")
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(
					filepath.Join(dir, "ethos.yaml"),
					[]byte("agent: claude\nteam: engineering\n"), 0o644))
			},
			wantAgent: "claude",
			wantTeam:  "engineering",
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

	assert.Equal(t, "claude", ResolveAgent(root))
}

func TestResolveAgent_LegacyFallback(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("agent: legacy\n"), 0o644))

	assert.Equal(t, "legacy", ResolveAgent(root))
}

func TestResolveAgent_NoConfig(t *testing.T) {
	assert.Equal(t, "", ResolveAgent(t.TempDir()))
}

func TestResolveAgent_EmptyRoot(t *testing.T) {
	assert.Equal(t, "", ResolveAgent(""))
}

func TestResolveAgent_NoAgentField(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".punt-labs")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "ethos.yaml"),
		[]byte("# empty config\n"), 0o644))

	assert.Equal(t, "", ResolveAgent(root))
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

			assert.Equal(t, tt.wantTeam, ResolveTeam(root))
		})
	}
}

func TestResolveTeam_MissingConfig(t *testing.T) {
	assert.Equal(t, "", ResolveTeam(t.TempDir()))
}

func TestResolveTeam_EmptyRoot(t *testing.T) {
	assert.Equal(t, "", ResolveTeam(""))
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

// runGit runs a git command in the given directory.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}
