//go:build linux || darwin

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/team"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// selectedSetupBundle adds an identity and a role that exist only in the
// selected bundle. Stale leadership callbacks cannot validate this member.
func selectedSetupBundle(t *testing.T, home, repo string) string {
	t.Helper()
	src := filepath.Join(home, ".punt-labs", "ethos", "bundles", "foundation")
	dst := filepath.Join(repo, ".punt-labs", "ethos-bundles", "acme")
	renameBundle(t, src, dst, "foundation", "acme")
	require.NoError(t, identity.NewStore(dst).Save(&identity.Identity{
		Name: "Acme Specialist", Handle: "acme-specialist", Kind: "agent",
	}))
	require.NoError(t, role.NewStore(dst).Save(&role.Role{Name: "acme-specialist"}))
	srcTeam, err := team.NewStore(dst).Load("acme")
	require.NoError(t, err)
	srcTeam.Members = append(srcTeam.Members, team.Member{Identity: "acme-specialist", Role: "acme-specialist"})
	data, err := yaml.Marshal(srcTeam)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dst, "teams", "acme.yaml"), data, 0o644))
	return dst
}

func TestCLI_SetupSnapshot_LegacyOnlySelectionSurvives(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the real CLI must build; this acceptance must not skip")
	se := freshCLIEnv(t, "legacy-human")
	_, stderr, code := runCLI(t, se, "seed")
	require.Equal(t, 0, code, stderr)
	legacy := filepath.Join(se.repo, ".punt-labs", "ethos", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o755))
	initial := "agent: claude\nactive_bundle: foundation\nteam: foundation\nresolution: layered\nrecord: full\nmax_delegation_depth: 7\n"
	require.NoError(t, os.WriteFile(legacy, []byte(initial), 0o600))
	canonical := filepath.Join(se.repo, ".punt-labs", "ethos.yaml")
	require.NoFileExists(t, canonical)
	answers := writeAnswers(t, se.repo, "name: Legacy Human\nhandle: legacy-human\n")
	_, stderr, code = runCLI(t, se, "setup", "--file", answers)
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stderr, `bundle "foundation" already active`)
	data, err := os.ReadFile(canonical)
	require.NoError(t, err)
	var cfg resolve.RepoConfig
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	assert.Equal(t, resolve.RepoConfig{Agent: "claude", ActiveBundle: "foundation", Team: "foundation", Resolution: "layered", Record: "full", MaxDelegationDepth: 7}, cfg)
	data, err = os.ReadFile(legacy)
	require.NoError(t, err)
	assert.Equal(t, initial, string(data), "the supported legacy source is not rewritten")
	stdout, stderr, code := runCLI(t, se, "team", "active")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "foundation (source: global")
	stdout, stderr, code = runHookIn(t, se, se.repo, "session-start", `{"session_id":"legacy-next-request"}`)
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "## Team: foundation", "the next request must load the persisted team")
}

func TestCLI_SetupSnapshot_SoloAgentPresence(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the real CLI must build; this acceptance must not skip")
	for _, tc := range []struct {
		name  string
		agent string
		want  string
	}{
		{name: "absent", want: "claude"},
		{name: "explicit-empty", agent: "agent: \"\" # deliberately no default agent\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			se := freshCLIEnv(t, "solo-human")
			_, stderr, code := runCLI(t, se, "seed")
			require.Equal(t, 0, code, stderr)
			initial := tc.agent + "team: custom-team\ncustom:\n  enabled: true\n"
			writeRepoConfigFile(t, se.repo, initial)
			answers := writeAnswers(t, se.repo, "name: Solo Human\nhandle: solo-human\nemail: solo@example.test\n")
			_, stderr, code = runCLI(t, se, "setup", "--solo", "--file", answers)
			require.Equal(t, 0, code, stderr)
			body := readRepoConfigFile(t, se.repo)
			var fields map[string]yaml.Node
			require.NoError(t, yaml.Unmarshal([]byte(body), &fields))
			agent, present := fields["agent"]
			assert.True(t, present, "setup adds an absent key, but keeps an explicit empty key")
			assert.Equal(t, tc.want, agent.Value)
			assert.Equal(t, "custom-team", fields["team"].Value)
			assert.Equal(t, yaml.MappingNode, fields["custom"].Kind)
			if tc.agent != "" {
				assert.Equal(t, initial, body, "explicit-empty config needs no write, including its comment")
			}
		})
	}
}

func TestSetupSnapshot_AgentInsertionResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial string
		added   bool
	}{
		{name: "absent", initial: "team: custom-team\n", added: true},
		{name: "empty-file", added: true},
		{name: "explicit-empty", initial: "agent: \"\"\nteam: custom-team\n"},
		{name: "explicit-null", initial: "agent: null\nteam: custom-team\n"},
		{name: "replacement-value-not-adopted", initial: "agent: later-agent\nteam: later-team\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			captured := &resolve.RepoConfig{Team: "captured-team"}
			want := *captured
			writeRepoConfigFile(t, repo, tc.initial)
			added, err := mergeRepoConfigAgent(repo, captured)
			require.NoError(t, err)
			assert.Equal(t, tc.added, added, "only an actual successful insertion may advance private config")
			assert.Equal(t, want, *captured, "disk values never replace the operation's captured selection")
			if !tc.added {
				assert.Equal(t, tc.initial, readRepoConfigFile(t, repo))
			}
		})
	}
}

func TestSetupSnapshot_AgentInsertionFailureAndRetry(t *testing.T) {
	repo := t.TempDir()
	initial := "# keep this comment\nteam: custom-team\ncustom:\n  enabled: true\n"
	writeRepoConfigFile(t, repo, initial)
	captured := &resolve.RepoConfig{Team: "captured-team"}
	want := *captured
	obstruction := filepath.Join(repo, ".punt-labs", "local")
	require.NoError(t, os.WriteFile(obstruction, []byte("unchanged\n"), 0o600))
	added, err := mergeRepoConfigAgent(repo, captured)
	require.Error(t, err)
	assert.False(t, added, "a failed insertion must not advance the private config")
	assert.Equal(t, initial, readRepoConfigFile(t, repo))
	assert.Equal(t, want, *captured)
	require.NoError(t, os.Remove(obstruction))
	added, err = mergeRepoConfigAgent(repo, captured)
	require.NoError(t, err)
	assert.True(t, added)
	body := readRepoConfigFile(t, repo)
	assert.Contains(t, body, "agent: claude")
	assert.Contains(t, body, initial, "existing nested fields and comments survive insertion")
	assert.Equal(t, want, *captured)
}

func TestSetupSnapshot_AgentInsertionRefusesMalformedReplacement(t *testing.T) {
	for _, replacement := range []string{"agent: [invalid\n", "- not-a-config-mapping\n"} {
		t.Run(replacement, func(t *testing.T) {
			repo := t.TempDir()
			writeRepoConfigFile(t, repo, replacement)
			captured := &resolve.RepoConfig{}
			added, err := mergeRepoConfigAgent(repo, captured)
			require.Error(t, err)
			assert.False(t, added)
			assert.Equal(t, replacement, readRepoConfigFile(t, repo))
			assert.Empty(t, captured.Agent)
		})
	}
}

func TestSetupSnapshot_CheckoutFailureRefusesBeforeWrite(t *testing.T) {
	home, repo := setupTestEnv(t)
	entry := resolve.Roots()
	require.NoError(t, entry.Err())
	failure := errors.New("captured checkout failure")
	entry.CheckoutErr = failure
	cmdRoots.Store(&repoRoots{Located: entry, Global: true})
	t.Cleanup(func() { cmdRoots.Store(nil) })
	setupFile = filepath.Join(repo, "answers.yaml")
	writeSetupFile(t, setupFile, "name: Refused Human\nhandle: refused-human\n")
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	require.ErrorIs(t, runSetup(cmd), failure)
	assert.NoFileExists(t, filepath.Join(home, ".punt-labs", "ethos", "identities", "refused-human.yaml"))
	assert.NoFileExists(t, filepath.Join(repo, ".punt-labs", "ethos.yaml"))
}

func TestSetupSnapshot_CanonicalConfigWriteFailureAndRetry(t *testing.T) {
	repo := t.TempDir()
	legacy := filepath.Join(repo, ".punt-labs", "ethos", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o755))
	initial := "agent: claude\nactive_bundle: foundation\nteam: foundation\nrecord: full\n"
	require.NoError(t, os.WriteFile(legacy, []byte(initial), 0o600))
	entry := &resolve.RepoConfig{Agent: "claude", ActiveBundle: "foundation", Team: "foundation", Record: "full"}
	want := *entry
	// Refuse staging under an obstructed local zone. A failed own-write must
	// not expose an agent-only canonical file or advance the caller's config.
	obstruction := filepath.Join(repo, ".punt-labs", "local")
	require.NoError(t, os.WriteFile(obstruction, []byte("unchanged\n"), 0o600))
	added, err := mergeRepoConfigAgent(repo, entry)
	require.Error(t, err)
	assert.False(t, added)
	assert.Equal(t, want, *entry)
	canonical := filepath.Join(repo, ".punt-labs", "ethos.yaml")
	assert.NoFileExists(t, canonical)
	data, err := os.ReadFile(legacy)
	require.NoError(t, err)
	assert.Equal(t, initial, string(data))
	require.NoError(t, os.Remove(obstruction))
	added, err = mergeRepoConfigAgent(repo, entry)
	require.NoError(t, err)
	assert.False(t, added, "the captured agent already existed")
	data, err = os.ReadFile(canonical)
	require.NoError(t, err)
	var written resolve.RepoConfig
	require.NoError(t, yaml.Unmarshal(data, &written))
	assert.Equal(t, want, written)
	assert.Equal(t, want, *entry)
}

func TestSetupSnapshot_SelectedBundleLeadership(t *testing.T) {
	for _, initial := range []string{"", "agent: claude\nteam: foundation\nactive_bundle: foundation\n"} {
		t.Run(initial, func(t *testing.T) {
			home, repo := setupTestEnv(t)
			selectedSetupBundle(t, home, repo)
			if initial != "" {
				writeRepoConfigFile(t, repo, initial)
			}
			entry := resolve.Roots()
			require.NoError(t, entry.Err())
			cmdRoots.Store(&repoRoots{Located: entry, Global: true})
			t.Cleanup(func() { cmdRoots.Store(nil) })
			setupFile = filepath.Join(repo, "answers.yaml")
			writeSetupFile(t, setupFile, "name: Snapshot Human\nhandle: snapshot-human\nemail: snapshot@example.test\n")
			setupBundle = "acme"
			cmd := &cobra.Command{}
			cmd.Flags().String("bundle", "acme", "")
			require.NoError(t, cmd.Flags().Set("bundle", "acme"))
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			require.NoError(t, runSetup(cmd), out.String())
			assigned, err := team.NewStore(filepath.Join(repo, ".punt-labs", "ethos")).Load("acme")
			require.NoError(t, err)
			assert.Contains(t, assigned.Members, team.Member{Identity: "snapshot-human", Role: "ceo"})
			assert.Contains(t, assigned.Members, team.Member{Identity: "claude", Role: "coo"})
			assert.Contains(t, assigned.Members, team.Member{Identity: "acme-specialist", Role: "acme-specialist"})
			// The command's entry value is not mutated by setup's progression.
			assert.Equal(t, entry, currentRoots().Located)
			if initial == "" {
				assert.Nil(t, entry.Config)
			} else {
				assert.Equal(t, "foundation", entry.Config.ActiveBundle)
			}
		})
	}
}

func TestTeamActivateSnapshot_SelectedBundleLeadership(t *testing.T) {
	home, repo := setupTestEnv(t)
	selectedSetupBundle(t, home, repo)
	answers := filepath.Join(repo, "answers.yaml")
	writeSetupFile(t, answers, "name: Snapshot Human\nhandle: snapshot-human\nemail: snapshot@example.test\n")
	_, stderr, err := execHandler(t, "setup", "--file", answers, "--bundle", "foundation")
	require.NoError(t, err, stderr)
	entry := resolve.Roots()
	cmdRoots.Store(&repoRoots{Located: entry, Global: true})
	t.Cleanup(func() { cmdRoots.Store(nil) })
	t.Setenv("USER", "snapshot-human")
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	require.NoError(t, runTeamActivate(cmd, "acme"), out.String())
	assigned, err := team.NewStore(filepath.Join(repo, ".punt-labs", "ethos")).Load("acme")
	require.NoError(t, err)
	assert.Contains(t, assigned.Members, team.Member{Identity: "snapshot-human", Role: "ceo"})
	assert.Contains(t, assigned.Members, team.Member{Identity: "acme-specialist", Role: "acme-specialist"})
	assert.Equal(t, "foundation", entry.Config.ActiveBundle)
}

type setupConfigReplacementWriter struct {
	bytes.Buffer
	path string
	done bool
	err  error
}

func (w *setupConfigReplacementWriter) Write(p []byte) (int, error) {
	if !w.done && strings.Contains(string(p), "activated: bundle") {
		w.done = true
		w.err = os.WriteFile(w.path, []byte("agent: [replacement-is-malformed\n"), 0o600)
	}
	return w.Buffer.Write(p)
}

func TestSetupSnapshot_ConfigReplacedAfterActivation(t *testing.T) {
	home, repo := setupTestEnv(t)
	selectedSetupBundle(t, home, repo)
	entry := resolve.Roots()
	require.NoError(t, entry.Err())
	cmdRoots.Store(&repoRoots{Located: entry, Global: true})
	t.Cleanup(func() { cmdRoots.Store(nil) })
	setupFile = filepath.Join(repo, "answers.yaml")
	writeSetupFile(t, setupFile, "name: Snapshot Human\nhandle: snapshot-human\nemail: snapshot@example.test\n")
	setupBundle = "acme"
	cmd := &cobra.Command{}
	cmd.Flags().String("bundle", "acme", "")
	require.NoError(t, cmd.Flags().Set("bundle", "acme"))
	out := &setupConfigReplacementWriter{path: filepath.Join(repo, ".punt-labs", "ethos.yaml")}
	cmd.SetOut(out)
	cmd.SetErr(out)
	err := runSetup(cmd)
	require.NoError(t, out.err)
	require.True(t, out.done, "test must replace config at the successful activation boundary")
	require.NoError(t, err, out.String())
	assigned, err := team.NewStore(filepath.Join(repo, ".punt-labs", "ethos")).Load("acme")
	require.NoError(t, err)
	assert.Contains(t, assigned.Members, team.Member{Identity: "acme-specialist", Role: "acme-specialist"})
	assert.FileExists(t, filepath.Join(repo, ".claude", "agents", "foundation-architect.md"))
	assert.Nil(t, entry.Config, "private post-write selection must not mutate the command entry")
}
