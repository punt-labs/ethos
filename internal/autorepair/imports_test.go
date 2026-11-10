package autorepair

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The session-start hook will call this package's engine, so this package
// must never import internal/hook: the line guards it needs live in
// internal/lineguard, a leaf below both. Checked from the import blocks of
// every non-test source file here.
func TestAutorepairNeverImportsHook(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		require.NoError(t, err, name)
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			require.NotEqual(t, "github.com/punt-labs/ethos/v4/internal/hook", path,
				"%s imports internal/hook; use internal/lineguard", name)
		}
	}
}
