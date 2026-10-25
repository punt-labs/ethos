package gitenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// boundedElsewhere names the files allowed to start git themselves, with how
// many sites each holds and why none needs gitenv: each carries its own
// context deadline, or is a test helper.
var boundedElsewhere = map[string]int{
	"internal/hook/working_context.go": 2, // gitTimeout (2s) context on both
	"internal/doctor/sandbox.go":       2, // sandboxTimeout (10s) context on both
	"internal/testhelpers/gitenv.go":   1, // GitCommand: a test helper; its callers set the bound
}

// gitStarts returns "file:line" for every exec.Command or exec.CommandContext
// in src that starts git by the literal name "git" or an identifier named git.
func gitStarts(t *testing.T, root string) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd", "plugin"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "exec" {
					return true
				}
				idx := map[string]int{"Command": 0, "CommandContext": 1}
				i, ok := idx[sel.Sel.Name]
				if !ok || len(call.Args) <= i {
					return true
				}
				if isGitName(call.Args[i]) {
					pos := fset.Position(call.Pos())
					found[rel] = append(found[rel], rel+":"+strconv.Itoa(pos.Line))
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	return found
}

func isGitName(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		s, err := strconv.Unquote(v.Value)
		return err == nil && s == "git"
	case *ast.Ident:
		return v.Name == "git"
	}
	return false
}

// Every git command ethos starts goes through gitenv (bounded by its
// deadline), except the allowlisted sites that carry their own context.
// The scan covers internal/, cmd/ and plugin/ only: tests/ is test
// infrastructure, not shipped code.
func TestNoUnboundedGitCommands(t *testing.T) {
	found := gitStarts(t, filepath.Join("..", ".."))
	var files []string
	for f := range found {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		want, ok := boundedElsewhere[f]
		if !ok {
			t.Errorf("%s starts git outside gitenv: use gitenv.Output, OutputEnv or Run (or bound it and allowlist it here): %v",
				f, found[f])
			continue
		}
		assert.Len(t, found[f], want, "%s: the allowlist counts %d sites", f, want)
	}
	for f := range boundedElsewhere {
		assert.Contains(t, found, f, "%s is allowlisted but starts no git: remove it from the list", f)
	}
}
