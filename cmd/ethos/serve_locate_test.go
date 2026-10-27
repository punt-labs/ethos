package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/mcp"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// locateServeStore distinguishes every answer the server rebuilds on:
// outside a repository, a repository with no store yet, a repository with a
// store, and a store that cannot be located.
func TestLocateServeStore(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	bare := filepath.Join(base, "bare")
	stored := filepath.Join(base, "stored")
	broken := filepath.Join(base, "broken")
	for _, d := range []string{outside, bare, stored, broken} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	testhelpers.MakeGitDir(t, bare)
	testhelpers.MakeGitDir(t, stored)
	require.NoError(t, os.MkdirAll(filepath.Join(stored, ".punt-labs", "ethos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(broken, ".git"), nil, 0o644))

	cases := map[string]struct {
		dir  string
		want func(got mcp.StoreLocation)
	}{
		"outside a repository": {outside, func(got mcp.StoreLocation) {
			assert.Equal(t, mcp.StoreLocation{}, got)
		}},
		"no store yet": {bare, func(got mcp.StoreLocation) {
			assert.NoError(t, got.Err)
			assert.Equal(t, realPath(t, bare), realPath(t, got.Root))
			assert.Empty(t, got.EthosRoot)
		}},
		"with a store": {stored, func(got mcp.StoreLocation) {
			assert.NoError(t, got.Err)
			assert.Equal(t, realPath(t, stored), realPath(t, got.Root))
			assert.Equal(t, realPath(t, filepath.Join(stored, ".punt-labs", "ethos")), realPath(t, got.EthosRoot))
		}},
		"cannot be located": {broken, func(got mcp.StoreLocation) {
			require.Error(t, got.Err)
			assert.Contains(t, got.Err.Error(), filepath.Join(broken, ".git"))
			assert.Empty(t, got.Root)
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Chdir(c.dir)
			c.want(locateServeStore())
		})
	}
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}
