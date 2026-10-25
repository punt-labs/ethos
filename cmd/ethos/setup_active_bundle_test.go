//go:build linux || darwin

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renameBundle copies bundle src to dst under the name to, renaming its
// manifest and team to match.
func renameBundle(t *testing.T, src, dst, from, to string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == filepath.Join("teams", from+".yaml") {
			rel = filepath.Join("teams", to+".yaml")
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if rel == "bundle.yaml" || rel == filepath.Join("teams", to+".yaml") {
			data = []byte(strings.Replace(string(data), "name: "+from, "name: "+to, 1))
		}
		return os.WriteFile(out, data, 0o644)
	})
	require.NoError(t, err)
}

// A re-run without --bundle keeps the active bundle, so that is the bundle
// it validates: a repository whose active bundle is acme does not need the
// foundation default to be installed.
func TestSetup_RerunValidatesActiveBundle(t *testing.T) {
	home, repo := setupTestEnv(t)
	setupCmd.Flags().Lookup("bundle").Changed = false
	cfgPath := filepath.Join(repo, "setup.yaml")
	writeSetupFile(t, cfgPath, "name: Acme User\nhandle: acme-user\n")
	_, _, err := execHandler(t, "setup", "--file", cfgPath)
	require.NoError(t, err)

	global := filepath.Join(home, ".punt-labs", "ethos", "bundles", "foundation")
	renameBundle(t, global, filepath.Join(repo, ".punt-labs", "ethos-bundles", "acme"), "foundation", "acme")
	require.NoError(t, os.RemoveAll(global)) // the default is not installed
	writeRepoConfigFile(t, repo, "agent: claude\nactive_bundle: acme\nteam: acme\n")

	_, stderr, err := execHandler(t, "setup", "--file", cfgPath)
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, stderr, `skipped: bundle "acme" already active`)
	assert.NotContains(t, stderr, "foundation")
}
