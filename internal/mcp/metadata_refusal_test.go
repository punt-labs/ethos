package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A caller named by session metadata is refused an identity when the store
// cannot be located, as every other whoami is: the metadata branch comes after
// the store-location refusal, so it never answers once the store is located as
// unusable (a cached healthy location is reused for up to locateTTL).
func TestMetadataWhoamiRefusedWhenStoreCannotBeLocated(t *testing.T) {
	t.Run("live location fails", func(t *testing.T) {
		h, _ := metadataHandler(t)
		base := t.TempDir()
		dir := filepath.Join(base, "r")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, ".git")))
		t.Setenv("GIT_CEILING_DIRECTORIES", base)
		t.Chdir(dir)
		result, err := h.handleIdentity(context.Background(), metadataCall(t, "ses_A", map[string]any{"method": "whoami"}))
		require.NoError(t, err)
		require.True(t, result.IsError)
		text := resultText(t, result)
		assert.Contains(t, text, "identity is refused")
		assert.NotContains(t, text, "Repo alice")
	})

	t.Run("the guard refuses an identity tool call on a handler built for a failed location", func(t *testing.T) {
		h, _ := metadataHandler(t)
		WithStoreError(errors.New("store not located"))(h)
		srv := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(true))
		h.RegisterTools(srv)
		res, err := srv.GetTool("identity").Handler(context.Background(), metadataCall(t, "ses_A", map[string]any{"method": "whoami"}))
		require.NoError(t, err)
		require.True(t, res.IsError)
		assert.Contains(t, resultText(t, res), "refusing")
		assert.NotContains(t, resultText(t, res), "Repo alice")
	})

	t.Run("iam, live location fails", func(t *testing.T) {
		h, _ := metadataHandler(t)
		base := t.TempDir()
		dir := filepath.Join(base, "r")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, ".git")))
		t.Setenv("GIT_CEILING_DIRECTORIES", base)
		t.Chdir(dir)
		result, err := h.handleSession(context.Background(), metadataCall(t, "ses_A", map[string]any{
			"method": "iam", "persona": "carol", "session_id": "ses_A", "agent_id": "ses_A",
		}))
		require.NoError(t, err)
		require.True(t, result.IsError)
		assert.Contains(t, resultText(t, result), "refused")
	})

	t.Run("iam checks no persona against it", func(t *testing.T) {
		h, _ := metadataHandler(t)
		WithStoreError(errors.New("store not located"))(h)
		result, err := h.handleSession(context.Background(), metadataCall(t, "ses_A", map[string]any{
			"method": "iam", "persona": "carol", "session_id": "ses_A", "agent_id": "ses_A",
		}))
		require.NoError(t, err)
		require.True(t, result.IsError)
		assert.Contains(t, resultText(t, result), "refused")
	})
}

// A bad active_bundle, read live, refuses metadata whoami and metadata iam too.
// The handlers are called directly, so the guard does not refuse first.
func TestMetadataCallerRefusedUnderABundleIssue(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos", "identities"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".punt-labs", "ethos.yaml"), []byte("active_bundle: nosuchbundle\n"), 0o644))
	t.Setenv("ETHOS_REPO_ROOT", repo)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(repo)

	h, _ := metadataHandler(t)
	result, err := h.handleIdentity(context.Background(), metadataCall(t, "ses_A", map[string]any{"method": "whoami"}))
	require.NoError(t, err)
	require.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "nosuchbundle")
	assert.NotContains(t, resultText(t, result), "Repo alice")

	result, err = h.handleSession(context.Background(), metadataCall(t, "ses_A", map[string]any{
		"method": "iam", "persona": "carol", "session_id": "ses_A", "agent_id": "ses_A",
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "nosuchbundle")
}
