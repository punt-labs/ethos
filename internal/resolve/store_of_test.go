package resolve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreOf(t *testing.T) {
	for _, withGit := range []bool{true, false} {
		for name, layout := range worktreeLayouts {
			prefix := "git/"
			if !withGit {
				prefix = "hand/"
			}
			t.Run(prefix+name, func(t *testing.T) {
				requireGit(t)
				checkout, want := layout.build(t, t.TempDir())
				t.Setenv("ETHOS_REPO_ROOT", t.TempDir())
				if !withGit {
					hideGit(t)
				}
				got, err := StoreOf(checkout)
				if layout.refusal != "" {
					require.ErrorContains(t, err, layout.refusal)
					require.Empty(t, got)
				} else {
					require.NoError(t, err)
					require.Equal(t, realpath(t, want), got)
				}
			})
		}
	}
	t.Run("outside repository", func(t *testing.T) {
		got, err := StoreOf(t.TempDir())
		require.NoError(t, err)
		require.Empty(t, got)
	})
	t.Run("broken gitfile", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("invalid gitfile\n"), 0o600))
		got, err := StoreOf(dir)
		require.Error(t, err)
		require.Empty(t, got)
	})
}
