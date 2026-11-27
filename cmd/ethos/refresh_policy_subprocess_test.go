//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLI_EnableRefreshSkipsGlobalBundleInRepoOnlyMode(t *testing.T) {
	se := freshCLIEnv(t, "refresh-human")
	_, stderr, code := runCLI(t, se, "enable")
	require.Zero(t, code, stderr)
	config := filepath.Join(se.repo, ".punt-labs", "ethos.yaml")
	bundle := filepath.Join(se.home, ".punt-labs", "ethos", "bundles", "foreign")
	for path, body := range map[string]string{
		config: "resolution: repo-only\nactive_bundle: foreign\nteam: foreign\n",
		filepath.Join(bundle, "teams", "foreign.yaml"):      "name: foreign\nmembers:\n  - identity: worker\n    role: reviewer\n",
		filepath.Join(bundle, "identities", "worker.yaml"):  "name: Foreign Worker\nhandle: worker\nkind: agent\npersonality: worker\n",
		filepath.Join(bundle, "personalities", "worker.md"): "# Worker\n\nReview the implementation.\n",
		filepath.Join(bundle, "roles", "reviewer.yaml"):     "name: reviewer\ntools: [Read]\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	_, stderr, code = runCLI(t, se, "enable", "--refresh")
	assert.Zero(t, code, stderr)
	assert.Contains(t, stderr, "repo-only cannot use the global bundle")
	assert.Contains(t, stderr, "agent definitions left unchanged")
	assert.NoFileExists(t, filepath.Join(se.repo, ".claude", "agents", "worker.md"))
}
