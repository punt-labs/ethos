package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An incomplete repo-authoritative set carries a fully-formed
// diagnostic: which refs are missing, where ethos looked, and the vendor
// command that fixes it. MCP must surface it verbatim rather than
// flattening it into a generic "not found" that discards all of it
// (DES-057 Part A, DES-020).
func TestHandleGetIdentity_RepoOnlyIncompleteSet(t *testing.T) {
	repoRoot := t.TempDir()
	repo := identity.NewStore(repoRoot)
	global := identity.NewStore(t.TempDir())

	require.NoError(t, os.MkdirAll(repo.IdentitiesDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo.IdentitiesDir(), "mal.yaml"),
		[]byte("name: Mal\nhandle: mal\nkind: human\npersonality: analytical\n"), 0o600))

	store := identity.NewLayeredStoreWithBundle(repo, nil, global, true)
	h := NewHandler(store,
		attribute.NewStore(repoRoot, attribute.Talents),
		attribute.NewStore(repoRoot, attribute.Personalities),
		attribute.NewStore(repoRoot, attribute.WritingStyles),
	)

	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method": "get",
		"handle": "mal",
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)

	text := resultText(t, result)
	assert.Contains(t, text, "repo-only")
	assert.Contains(t, text, "personalities/analytical")
	assert.Contains(t, text, "ethos vendor mal")
}
