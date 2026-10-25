package resolve

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The lossy resolvers returned "" for "not in a repository" and for every
// failure alike, with a stderr message for the second. They are retired:
// StoreRootIfAny, CheckoutRootIfAny and EthosRootIfAny (or LocateStoreRoot and
// LocateRepoRoot) return "" only outside a repository and an error otherwise.
// This test fails if a retired name appears anywhere in the Go source again, in
// code or in a comment that would send a reader to a function that is gone.
func TestRetiredResolversStayRetired(t *testing.T) {
	retired := regexp.MustCompile(`\b(FindRepoRoot|EnvRepoRoot|StoreRepoRoot|FindRepoEthosRoot)\b`)
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".tmp", ".claude", ".punt-labs", "node_modules", ".venv", "worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || filepath.Base(p) == "retired_test.go" {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if loc := retired.FindIndex(data); loc != nil {
			t.Errorf("%s mentions the retired resolver %q", p, data[loc[0]:loc[1]])
		}
		return nil
	})
	require.NoError(t, err)
}
