package doctor

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/seed"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/punt-labs/ethos/v4/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func physicalAgentRoot(t *testing.T, root string) string {
	t.Helper()
	physical, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	return physical
}

func currencyFixture(t *testing.T) (string, string, *identity.LayeredStore, *team.LayeredStore) {
	t.Helper()
	store, checkout := physicalAgentRoot(t, gitRepo(t)), physicalAgentRoot(t, gitRepo(t))
	root := filepath.Join(store, ".punt-labs", "ethos")
	write := func(rel, body string) {
		path := filepath.Join(store, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	write(".punt-labs/ethos.yaml", "team: engineering\nresolution: repo-only\n")
	write(".punt-labs/ethos/identities/bwk.yaml", "name: Brian\nhandle: bwk\nkind: agent\npersonality: clear\n")
	write(".punt-labs/ethos/personalities/clear.md", "# Clear\n\nClear prose.\n")
	write(".punt-labs/ethos/roles/implementer.yaml", "name: implementer\nresponsibilities: [Implement]\ntools: [Read, Write]\n")
	write(".punt-labs/ethos/teams/engineering.yaml", "name: engineering\nmembers:\n  - identity: bwk\n    role: implementer\n")
	write(".punt-labs/ethos/agents/stub.md", "---\nname: stub\n---\n\nInstalled source.\n")
	p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
	require.NoError(t, err)
	s := identity.NewLayeredStoreWithBundle(identity.NewStore(root, identity.RepositoryPolicy(p)), nil, identity.NewStore(physicalAgentRoot(t, t.TempDir())), true)
	teams := team.NewLayeredStoreWithBundle(root, "", s.GlobalRoot(), true)
	roles := role.NewLayeredStoreWithBundle(root, "", s.GlobalRoot(), true)
	require.NoError(t, hook.GenerateAgentFilesTo(store, checkout, s, teams, roles))
	installed, err := hook.PlanInstalledAgentDefinitions(root, map[string]bool{"bwk": true})
	require.NoError(t, err)
	require.NoError(t, hook.ApplyAgentPlan(p, hook.AgentPlan{Files: installed}))
	return store, checkout, s, teams
}

func TestAgentCurrencyGenerationWithMultipleRoles(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			store, checkout, s, teams := currencyFixture(t)
			root := s.RepoRoot()
			require.NoError(t, os.WriteFile(filepath.Join(root, "roles", "reviewer.yaml"), []byte("name: reviewer\nresponsibilities: [Review only]\ntools: [Read]\n"), 0o600))
			first, last := "implementer", "reviewer"
			if reverse {
				first, last = last, first
			}
			body := fmt.Sprintf("name: engineering\nmembers:\n  - identity: bwk\n    role: %s\n  - identity: bwk\n    role: %s\n", first, last)
			require.NoError(t, os.WriteFile(filepath.Join(root, "teams", "engineering.yaml"), []byte(body), 0o600))
			roles := role.NewLayeredStoreWithBundle(root, "", s.GlobalRoot(), true)
			require.NoError(t, hook.GenerateAgentFilesTo(store, checkout, s, teams, roles))
			data, err := os.ReadFile(filepath.Join(checkout, ".claude", "agents", "bwk.md"))
			require.NoError(t, err)
			if reverse {
				assert.Contains(t, string(data), "- Implement\n")
				assert.NotContains(t, string(data), "Review only")
			} else {
				assert.Contains(t, string(data), "Review only")
			}
			r := CheckAgentCurrency(checkout, store, teams, s)
			assert.Equal(t, "PASS", r.Status, r.Detail)
		})
	}
}

func TestRunAllForAgentCurrencyUsesCapturedConfig(t *testing.T) {
	for _, replacement := range []string{
		"team: nonexistent\nresolution: repo-only\n",
		"team: engineering\nrecord: invalid\n",
		"active_bundle: missing\nresolution: repo-only\n",
		"team: [malformed\n",
	} {
		t.Run(replacement, func(t *testing.T) {
			store, checkout, s, teams := currencyFixture(t)
			t.Setenv("HOME", s.GlobalRoot())
			cfg, err := resolve.ConfigOf(store)
			require.NoError(t, err)
			loc := resolve.Located{Store: store, Checkout: checkout, Ethos: s.RepoRoot(), Config: cfg}
			require.NoError(t, os.WriteFile(filepath.Join(store, ".punt-labs", "ethos.yaml"), []byte(replacement), 0o600))
			found := false
			for _, r := range RunAllFor(loc, s, session.NewStore(physicalAgentRoot(t, t.TempDir())), teams) {
				assert.NotEqual(t, "Repository configuration", r.Name, "bundle policy must use the same captured config")
				if r.Name == "Agent file currency" {
					found = true
					assert.Equal(t, "PASS", r.Status, r.Detail)
				}
			}
			require.True(t, found, "the actual doctor entry point must execute the currency row")
			assert.Equal(t, "FAIL", CheckAgentCurrency(checkout, store, teams, s).Status, "a fresh standalone read must see the replacement")
		})
	}
}

func TestAgentCurrencyPhysicalFixturesUnderAliasedTMPDIR(t *testing.T) {
	base := physicalAgentRoot(t, t.TempDir())
	physical, alias := filepath.Join(base, "tmp-physical"), filepath.Join(base, "tmp-alias")
	require.NoError(t, os.Mkdir(physical, 0o700))
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Setenv("TMPDIR", alias)
	t.Setenv("TMP", alias)
	t.Setenv("TEMP", alias)
	t.Run("generation and currency", func(t *testing.T) {
		raw := t.TempDir()
		require.NotEqual(t, raw, physicalAgentRoot(t, raw), "the fixture must actually use the TMPDIR alias")
		store, checkout, s, teams := currencyFixture(t)
		for _, root := range []string{store, checkout, s.GlobalRoot()} {
			assert.Equal(t, root, physicalAgentRoot(t, root))
		}
		r := CheckAgentCurrency(checkout, store, teams, s)
		assert.Equal(t, "PASS", r.Status, r.Detail)
	})
}

func TestAgentCurrencyComparesActualBytesByOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, file, body, want string
	}{
		{"matching", "", "", "PASS"},
		{"generated edit", "bwk.md", "edited generated bytes\n", "FAIL"},
		{"installed edit", "stub.md", "edited installed bytes\n", "FAIL"},
		{"seeded edit", "code-reviewer.md", "operator checklist edits\n", "WARN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, checkout, s, teams := currencyFixture(t)
			if tc.file != "" {
				require.NoError(t, os.WriteFile(filepath.Join(checkout, ".claude", "agents", tc.file), []byte(tc.body), 0o600))
			}
			before := diagnosticTreeHash(t, store, checkout, s.GlobalRoot())
			r := CheckAgentCurrency(checkout, store, teams, s)
			assert.Equal(t, tc.want, r.Status, r.Detail)
			assert.Equal(t, before, diagnosticTreeHash(t, store, checkout, s.GlobalRoot()))
		})
	}
}

func TestAgentCurrencyMissingAndSymlinkedFilesFail(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprint(symlink), func(t *testing.T) {
			store, checkout, s, teams := currencyFixture(t)
			path := filepath.Join(checkout, ".claude", "agents", "bwk.md")
			require.NoError(t, os.Remove(path))
			if symlink {
				target := filepath.Join(physicalAgentRoot(t, t.TempDir()), "elsewhere.md")
				require.NoError(t, os.WriteFile(target, []byte("private bytes\n"), 0o600))
				require.NoError(t, os.Symlink(target, path))
			}
			r := CheckAgentCurrency(checkout, store, teams, s)
			assert.Equal(t, "FAIL", r.Status, r.Detail)
			if symlink {
				assert.Contains(t, r.Detail, "symlink")
			}
		})
	}
}

func TestAgentCurrencyUsesStoreSourcesAndCheckoutProjectInputs(t *testing.T) {
	store, checkout, s, teams := currencyFixture(t)
	// A checkout-local source change is not shared authority.
	path := filepath.Join(checkout, ".punt-labs", "ethos", "personalities", "clear.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("Not authoritative.\n"), 0o600))
	assert.Equal(t, "PASS", CheckAgentCurrency(checkout, store, teams, s).Status)
	// But project detection belongs to C, not S: changing C's marker changes
	// the expected generated hook bytes.
	require.NoError(t, os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module example.com/test\n"), 0o600))
	assert.Equal(t, "FAIL", CheckAgentCurrency(checkout, store, teams, s).Status)
}

func TestAgentCurrencySeedEmbeddedBytesPass(t *testing.T) {
	store, checkout, s, teams := currencyFixture(t)
	data, err := fs.ReadFile(seed.Agents, "sidecar/agents/code-reviewer.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(checkout, ".claude", "agents", "code-reviewer.md"), data, 0o600))
	assert.Equal(t, "PASS", CheckAgentCurrency(checkout, store, teams, s).Status)
}

func TestAgentCurrencyRejectsMalformedSourcesWithoutMutation(t *testing.T) {
	for _, rel := range []string{"identities/bwk.yaml", "roles/implementer.yaml", "teams/engineering.yaml"} {
		t.Run(rel, func(t *testing.T) {
			store, checkout, s, teams := currencyFixture(t)
			require.NoError(t, os.WriteFile(filepath.Join(s.RepoRoot(), filepath.FromSlash(rel)), []byte("malformed: [\n"), 0o600))
			before := diagnosticTreeHash(t, store, checkout, s.GlobalRoot())
			r := CheckAgentCurrency(checkout, store, teams, s)
			assert.Equal(t, "FAIL", r.Status, r.Detail)
			assert.Equal(t, before, diagnosticTreeHash(t, store, checkout, s.GlobalRoot()))
		})
	}
}

func TestAgentCurrencyRejectsRedirectedSources(t *testing.T) {
	for _, rel := range []string{"identities/bwk.yaml", "personalities/clear.md", "roles/implementer.yaml", "teams/engineering.yaml", "agents/stub.md"} {
		t.Run(rel, func(t *testing.T) {
			store, checkout, s, teams := currencyFixture(t)
			path := filepath.Join(s.RepoRoot(), filepath.FromSlash(rel))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			target := filepath.Join(physicalAgentRoot(t, t.TempDir()), filepath.Base(path))
			require.NoError(t, os.WriteFile(target, data, 0o600))
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Symlink(target, path))
			r := CheckAgentCurrency(checkout, store, teams, s)
			assert.Equal(t, "FAIL", r.Status, r.Detail)
			assert.Contains(t, r.Detail, "symlink")
		})
	}
}

func TestAgentCurrencySnapshotAndMissingDependencies(t *testing.T) {
	store, checkout, s, teams := currencyFixture(t)
	cfg, err := resolve.ConfigOf(store)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(store, ".punt-labs", "ethos.yaml"), []byte("record: invalid\n"), 0o600))
	r := CheckAgentCurrencyFromConfig(checkout, store, cfg, teams, s)
	assert.Equal(t, "PASS", r.Status, r.Detail)
	assert.Equal(t, "FAIL", CheckAgentCurrency(checkout, store, teams, s).Status)
	assert.Equal(t, "FAIL", CheckAgentCurrencyFromConfig(checkout, store, cfg, nil, s).Status)
	cfg.Record = "invalid"
	assert.Equal(t, "FAIL", CheckAgentCurrencyFromConfig(checkout, store, cfg, teams, s).Status)
}

func TestAgentCurrencySourceEditsCauseDrift(t *testing.T) {
	for _, rel := range []string{"personalities/clear.md", "agents/stub.md"} {
		t.Run(rel, func(t *testing.T) {
			store, checkout, s, teams := currencyFixture(t)
			require.NoError(t, os.WriteFile(filepath.Join(s.RepoRoot(), filepath.FromSlash(rel)), []byte("Updated source bytes.\n"), 0o600))
			r := CheckAgentCurrency(checkout, store, teams, s)
			assert.Equal(t, "FAIL", r.Status, r.Detail)
		})
	}
}

func TestDoctorDoesNotMigrateLegacyVoice(t *testing.T) {
	for _, repoOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(repoOnly), func(t *testing.T) {
			store, checkout, s, teams := currencyFixture(t)
			if !repoOnly {
				s = identity.NewLayeredStore(identity.NewStore(s.RepoRoot()), identity.NewStore(s.GlobalRoot()))
			}
			for _, root := range []string{s.RepoRoot(), s.GlobalRoot()} {
				path := filepath.Join(root, "identities", "legacy.yaml")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte("name: Legacy\nhandle: legacy\nkind: human\nvoice:\n  provider: example\n  voice_id: old\n"), 0o600))
			}
			path := filepath.Join(s.RepoRoot(), "identities", "bwk.yaml")
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, append(data, []byte("voice: {provider: example, voice_id: old}\n")...), 0o600))
			before := diagnosticTreeHash(t, store, checkout, s.GlobalRoot())
			assert.Equal(t, "PASS", CheckAgentCurrency(checkout, store, teams, s).Status)
			RunAll(s, session.NewStore(physicalAgentRoot(t, t.TempDir())), checkout, nil, store, nil, teams)
			assert.Equal(t, before, diagnosticTreeHash(t, store, checkout, s.GlobalRoot()), "doctor must not migrate any identity or leave staging/lock debris")
		})
	}
}

func TestDoctorListingRefusesRedirectedExtension(t *testing.T) {
	root := physicalAgentRoot(t, t.TempDir())
	store := identity.NewStore(root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "identities"), 0o700))
	writeIdentity(t, root, "bwk", "name: Brian\nhandle: bwk\nkind: agent\n")
	extDir := store.ExtDir("bwk")
	require.NoError(t, os.MkdirAll(extDir, 0o700))
	target := filepath.Join(physicalAgentRoot(t, t.TempDir()), "quarry.yaml")
	require.NoError(t, os.WriteFile(target, []byte("memory_collection: elsewhere\n"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(extDir, "quarry.yaml")))
	detail, passed := CheckDuplicateFields(store, nil)
	assert.False(t, passed)
	assert.Contains(t, detail, "symlink")
}

func TestRepoOnlySnapshotDoesNotRereadConfig(t *testing.T) {
	store, _ := repoOnlySet(t, "repo-only")
	s := repoOnlyStore(t, store)
	cfg, err := resolve.ConfigOf(store)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(store, ".punt-labs", "ethos.yaml"), []byte("resolution: invalid\n"), 0o600))
	r := CheckRepoOnlyFromConfig(s, store, cfg)
	assert.Equal(t, "PASS", r.Status, r.Detail)
	assert.Equal(t, "FAIL", CheckRepoSetComplete(s, store).Status)
}

func diagnosticTreeHash(t *testing.T, roots ...string) string {
	t.Helper()
	h := sha256.New()
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			require.NoError(t, err)
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			info, err := entry.Info()
			require.NoError(t, err)
			fmt.Fprintf(h, "%s:%s\n", path, info.Mode())
			if !entry.IsDir() {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				h.Write(data)
			}
			return nil
		}))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
