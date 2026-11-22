//go:build linux || darwin

package hook

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoAdapterExecution(t *testing.T) {
	root, bin := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	postEditTool(t, bin, "go", "printf '%s\\n' \"$@\" > \"$PWD/args\"\n")
	file := filepath.Join(root, "sub", "edited.go")
	writeFile(t, file, "package sub\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: vet\n    stage: edit\n    files: ['**/*.go']\n    adapter: go\n    go: vet\n")
	checks, selected, err := configuredEditChecks(root, root, file, "")
	require.NoError(t, err)
	require.True(t, selected)
	require.Len(t, checks, 1)
	require.Contains(t, runEditCheck(context.Background(), root, checks[0]), "go.mod")
	require.NoFileExists(t, filepath.Join(root, "args"))
	writeFile(t, filepath.Join(root, "go.mod"), "module test\n")
	require.Empty(t, runEditCheck(context.Background(), root, checks[0]))
	args, err := os.ReadFile(filepath.Join(root, "args"))
	require.NoError(t, err)
	require.Equal(t, "vet\n./sub\n", string(args))
}
