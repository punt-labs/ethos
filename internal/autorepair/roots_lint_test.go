package autorepair

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discardedRootErrors lists, as file:line, every call to StoreRoot or
// CheckoutRoot in the non-test Go files of dir whose error result is assigned
// to the blank identifier. Both helpers return "" on failure, so a step that
// drops the error operates relative to the current directory.
func discardedRootErrors(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	var found []string
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 2 || len(as.Rhs) != 1 {
				return true
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "StoreRoot" && sel.Sel.Name != "CheckoutRoot") {
				return true
			}
			if id, ok := as.Lhs[1].(*ast.Ident); ok && id.Name == "_" {
				found = append(found, fset.Position(as.Pos()).String())
			}
			return true
		})
	}
	return found
}

func TestRootErrorsAreNeverDiscarded(t *testing.T) {
	t.Run("probe", func(t *testing.T) {
		dir := t.TempDir()
		src := `package p

func f(e interface{ StoreRoot() (string, error); CheckoutRoot() (string, error) }) {
	s, _ := e.StoreRoot()
	var c string
	c, _ = e.CheckoutRoot()
	ok, err := e.StoreRoot()
	_, _, _, _ = s, c, ok, err
}
`
		require.NoError(t, os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0o600))
		got := discardedRootErrors(t, dir)
		assert.Len(t, got, 2, "both discards found: %v", got)
	})
	t.Run("package", func(t *testing.T) {
		assert.Empty(t, discardedRootErrors(t, "."))
	})
}
