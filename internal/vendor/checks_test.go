package vendor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const portableChecks = "name: portable\nchecks:\n  - name: format\n    stage: edit\n    files: ['**']\n    run: ['true']\n"

func writeCheckFixture(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func TestVendorCheckSets(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)
	f.writeGlobal(t, "identities/bwk.yaml", "name: Brian\nhandle: bwk\nkind: agent\nchecks: portable\n")
	f.writeGlobal(t, "identities/claudia.yaml", "name: Claudia\nhandle: claudia\nkind: agent\nchecks: none\n")
	f.writeGlobal(t, "roles/go-specialist.yaml", "name: go-specialist\nchecks: role-policy\n")
	f.writeGlobal(t, "checks/portable.yaml", portableChecks)
	f.writeGlobal(t, "checks/role-policy.yaml", "name: role-policy\nchecks: []\n")
	f.writeGlobal(t, "checks/unused.yaml", "name: unused\nchecks: []\n")
	writeCheckFixture(t, f.repo, "checks/portable.yaml", portableChecks+"description: repository override\n")
	_, err := f.run(t, Options{Handles: []string{"bwk"}, Apply: true})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(f.repo, "checks/role-policy.yaml"))
	data, err := os.ReadFile(filepath.Join(f.repo, "checks/portable.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "repository override")
	require.NoFileExists(t, filepath.Join(f.repo, "checks/unused.yaml"))
	require.NoFileExists(t, filepath.Join(f.repo, "checks/none.yaml"))
	require.NoError(t, Verify(f.repo))
}

func TestVerifyCheckSets(t *testing.T) {
	for _, kind := range []string{"identity", "role", "manifest"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			writeCheckFixture(t, root, "identities/agent.yaml", "name: Agent\nhandle: agent\nkind: agent\n")
			switch kind {
			case "identity":
				writeCheckFixture(t, root, "identities/agent.yaml", "name: Agent\nhandle: agent\nkind: agent\nchecks: portable\n")
			case "role":
				writeCheckFixture(t, root, "roles/writer.yaml", "name: writer\nchecks: portable\n")
			case "manifest":
				writeCheckFixture(t, root, ManifestName, "version: 1\nchecks: [portable]\n")
			}
			err := Verify(root)
			require.Error(t, err)
			require.Contains(t, err.Error(), "portable")
			writeCheckFixture(t, root, "checks/portable.yaml", portableChecks)
			require.NoError(t, Verify(root))
		})
	}
}

func TestVendorRejectsBrokenCheckSets(t *testing.T) {
	for _, body := range []string{"", "name: portable\nunknown: true\nchecks: []\n", "name: wrong\nchecks: []\n"} {
		t.Run(body, func(t *testing.T) {
			f := newFixture(t)
			f.writeGlobal(t, "identities/agent.yaml", "name: Agent\nhandle: agent\nkind: agent\nchecks: portable\n")
			if body != "" {
				f.writeGlobal(t, "checks/portable.yaml", body)
			}
			_, err := f.run(t, Options{Handles: []string{"agent"}, Apply: true})
			require.Error(t, err)
			require.NoDirExists(t, filepath.Join(f.repo, "identities"))
		})
	}
}

func TestVendorRejectsInvalidCheckReferences(t *testing.T) {
	for _, kind := range []string{"identity", "role", "config"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.seedOrg(t)
			switch kind {
			case "identity":
				f.writeGlobal(t, "identities/bwk.yaml", "name: Brian\nhandle: bwk\nkind: agent\nchecks: '../outside'\n")
			case "role":
				f.writeGlobal(t, "roles/go-specialist.yaml", "name: go-specialist\nchecks: '../outside'\n")
			case "config":
				f.src.Checks = "../outside"
			}
			_, err := f.run(t, Options{Handles: []string{"bwk"}, Apply: true})
			require.Error(t, err)
			require.NoDirExists(t, filepath.Join(f.repo, "identities"))
		})
	}
}

func TestVerifyRejectsInvalidCheckSet(t *testing.T) {
	root := t.TempDir()
	writeCheckFixture(t, root, "identities/agent.yaml", "name: Agent\nhandle: agent\nkind: agent\nchecks: portable\n")
	writeCheckFixture(t, root, "checks/portable.yaml", "name: portable\nunknown: field\n")
	require.Error(t, Verify(root))
}

func TestVendorRepoCheckSets(t *testing.T) {
	f := newFixture(t)
	f.src.Checks = "portable"
	f.writeGlobal(t, "identities/agent.yaml", "name: Agent\nhandle: agent\nkind: agent\n")
	f.writeGlobal(t, "checks/portable.yaml", portableChecks)
	writeCheckFixture(t, f.repo, "checks/unused.yaml", "name: unused\nchecks: []\n")
	p, err := f.run(t, Options{Handles: []string{"agent"}, Apply: true, Prune: true})
	require.NoError(t, err)
	require.Equal(t, []string{"portable"}, p.Checks)
	require.FileExists(t, filepath.Join(f.repo, "checks/portable.yaml"))
	require.NoFileExists(t, filepath.Join(f.repo, "checks/unused.yaml"))
	require.NoError(t, os.Remove(filepath.Join(f.repo, "checks/portable.yaml")))
	require.Error(t, Verify(f.repo))
}
