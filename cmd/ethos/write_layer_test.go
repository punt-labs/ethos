//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Where the creates write, in a healthy repository with a store: the attribute
// kinds, roles and teams write the global layer (internal/attribute/layered.go,
// internal/role/layered.go, internal/team/layered.go, docs/build-plan-bundles.md:
// "Write: always target the global layer"; the repo layer only under
// `resolution: repo-only`), and an identity is written to the repo store when
// there is one (internal/identity/layered.go Save). They still read every layer
// to validate and to refuse a duplicate, which is why a repository store ethos
// cannot read refuses them (storeRefuses) rather than letting them run on the
// global layer alone.
func TestCreatesWriteWhereTheLayersSay(t *testing.T) {
	se := setupPhase2Env(t)
	resetPhase2Flags(t)
	globalEthos := filepath.Join(se.home, ".punt-labs", "ethos")
	repoEthos := filepath.Join(se.repo, ".punt-labs", "ethos")
	require.DirExists(t, repoEthos)

	content := filepath.Join(t.TempDir(), "content.md")
	require.NoError(t, os.WriteFile(content, []byte("# Content\n\nbody\n"), 0o644))
	for _, kind := range []struct{ cmd, dir string }{
		{"talent", "talents"}, {"personality", "personalities"}, {"writing-style", "writing-styles"},
	} {
		_, _, err := execHandler(t, kind.cmd, "create", "wl-"+kind.cmd, "-f", content)
		require.NoError(t, err, kind.cmd)
		assert.FileExists(t, filepath.Join(globalEthos, kind.dir, "wl-"+kind.cmd+".md"), "%s create writes the global layer", kind.cmd)
		assert.NoFileExists(t, filepath.Join(repoEthos, kind.dir, "wl-"+kind.cmd+".md"), "%s create does not write the repo store", kind.cmd)
	}

	roleFile := filepath.Join(t.TempDir(), "r.yaml")
	data, err := yaml.Marshal(map[string]any{"name": "wl-role", "responsibilities": []string{"x"}, "permissions": []string{"read"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(roleFile, data, 0o644))
	_, _, err = execHandler(t, "role", "create", "wl-role", "-f", roleFile)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(globalEthos, "roles", "wl-role.yaml"))
	assert.NoFileExists(t, filepath.Join(repoEthos, "roles", "wl-role.yaml"))

	idFile := filepath.Join(t.TempDir(), "id.yaml")
	require.NoError(t, os.WriteFile(idFile, []byte("name: Wl Person\nhandle: wl-person\nkind: human\nemail: wl@example.com\n"), 0o644))
	_, _, err = execHandler(t, "identity", "create", "-f", idFile)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(repoEthos, "identities", "wl-person.yaml"), "an identity goes to the repo store when there is one")
}
