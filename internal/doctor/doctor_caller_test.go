package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoOnlyStores is a repo-only store whose repo vendors only "impl" and whose
// global store holds "claude", the person running ethos.
func repoOnlyStores(t *testing.T) identity.IdentityStore {
	t.Helper()
	repoRoot, globalRoot := t.TempDir(), t.TempDir()
	for _, root := range []string{repoRoot, globalRoot} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "identities"), 0o700))
	}
	writeIdentity(t, repoRoot, "impl", "name: Impl\nhandle: impl\nkind: agent\n")
	writeIdentity(t, globalRoot, "claude", "name: Claude Agento\nhandle: claude\nkind: agent\n")
	return identity.NewLayeredStoreWithBundle(identity.NewStore(repoRoot), nil, identity.NewStore(globalRoot), true)
}

func TestCheckHumanIdentity_RepoOnlyCallerFromGlobal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("caller only in the global store passes with a note", func(t *testing.T) {
		t.Setenv("USER", "claude")
		r := CheckHumanIdentity(repoOnlyStores(t), nil, "")
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "claude")
		assert.Contains(t, r.Detail, "resolved from the global store")
	})

	t.Run("caller in the repo carries no note", func(t *testing.T) {
		t.Setenv("USER", "impl")
		r := CheckHumanIdentity(repoOnlyStores(t), nil, "")
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
		assert.NotContains(t, r.Detail, "global store")
	})

	t.Run("caller in neither store fails", func(t *testing.T) {
		t.Setenv("USER", "ghost")
		r := CheckHumanIdentity(repoOnlyStores(t), nil, "")
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "no identity matches")
	})
}
