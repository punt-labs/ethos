package resolve

import (
	"os"
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

// --- ResolveAgent tests ---

func TestResolveAgent_ConfigSet(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(configDir, "config.yaml"),
		[]byte("agent: claude\n"),
		0o644,
	))

	assert.Equal(t, "claude", ResolveAgent(root))
}

func TestResolveAgent_NoConfig(t *testing.T) {
	assert.Equal(t, "", ResolveAgent(t.TempDir()))
}

func TestResolveAgent_EmptyRoot(t *testing.T) {
	assert.Equal(t, "", ResolveAgent(""))
}

func TestResolveAgent_NoAgentField(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(configDir, "config.yaml"),
		[]byte("# empty config\n"),
		0o644,
	))

	assert.Equal(t, "", ResolveAgent(root))
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
