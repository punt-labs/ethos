package resolve

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// locators are the resolve functions whose error says why a repository, a
// store or a checkout could not be located. "" with a nil error is their
// answer for "not in a repository" (or, for ethosRootOf, "no store yet");
// every other failure is the error.
var locators = map[string]bool{
	"locateRepoRoot": true, "locateStoreRoot": true,
	"storeRootIfAny": true, "checkoutRootIfAny": true,
	"ethosRootOf": true, "Roots": true,
}

// The lossy resolvers returned "" for "not in a repository" and for every
// failure alike. No function may turn a failure from a locator into "" again:
// in an `if err != nil` block over the error a locator returned, the block
// may not return, or assign, the empty string without returning an error —
// unless the condition separates the two cases by testing the error against
// ErrNotInRepo or fs.ErrNotExist.
func TestNoFunctionTurnsALocateFailureIntoEmpty(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir:  root, Tests: false,
	}, "./internal/...", "./cmd/...", "./plugin/...")
	require.NoError(t, err)
	var violations []string
	for _, p := range pkgs {
		for _, e := range p.Errors {
			t.Fatalf("loading %s: %v", p.PkgPath, e)
		}
		for _, f := range p.Syntax {
			violations = append(violations, scanLocateFailures(p, f)...)
		}
	}
	sort.Strings(violations)
	require.Empty(t, violations, "a located failure was turned into the empty string")
}

// scanLocateFailures returns "file:line: function" for each violation in f.
func scanLocateFailures(p *packages.Package, f *ast.File) []string {
	var out []string
	info := p.TypesInfo
	errorType := types.Universe.Lookup("error").Type()
	isLocatorCall := func(e ast.Expr) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return false
		}
		var id *ast.Ident
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			id = fn
		case *ast.SelectorExpr:
			id = fn.Sel
		default:
			return false
		}
		obj, ok := info.Uses[id].(*types.Func)
		return ok && obj.Pkg() != nil && strings.HasSuffix(obj.Pkg().Path(), "/internal/resolve") && locators[obj.Name()]
	}
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			return true
		}
		tracked := map[types.Object]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 || !isLocatorCall(as.Rhs[0]) {
				return true
			}
			for _, l := range as.Lhs {
				id, ok := l.(*ast.Ident)
				if !ok {
					continue
				}
				obj := info.ObjectOf(id)
				if obj != nil && types.Identical(obj.Type(), errorType) {
					tracked[obj] = true
				}
			}
			return true
		})
		if len(tracked) == 0 {
			return true
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			ifs, ok := n.(*ast.IfStmt)
			if !ok || !testsNonNil(info, ifs.Cond, tracked) || separatesCases(ifs.Cond) {
				return true
			}
			if emptiesWithoutError(info, ifs.Body, errorType) {
				out = append(out, p.Fset.Position(ifs.Pos()).String()+": "+fd.Name.Name)
			}
			return true
		})
		return true
	})
	return out
}

// testsNonNil reports whether cond contains `err != nil` for a tracked error.
func testsNonNil(info *types.Info, cond ast.Expr, tracked map[types.Object]bool) bool {
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		b, ok := n.(*ast.BinaryExpr)
		if !ok || b.Op != token.NEQ {
			return true
		}
		id, ok := b.X.(*ast.Ident)
		nilID, ok2 := b.Y.(*ast.Ident)
		if ok && ok2 && nilID.Name == "nil" && tracked[info.ObjectOf(id)] {
			found = true
		}
		return true
	})
	return found
}

// separatesCases reports whether cond tests an error against ErrNotInRepo or
// fs.ErrNotExist: the one distinction a caller may make before answering "".
func separatesCases(cond ast.Expr) bool {
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && (id.Name == "ErrNotInRepo" || id.Name == "ErrNotExist") {
			found = true
		}
		return true
	})
	return found
}

// emptiesWithoutError reports whether body returns, or assigns, the empty
// string without handing an error back: `return ""`, `return "", nil`,
// `x = ""`.
func emptiesWithoutError(info *types.Info, body *ast.BlockStmt, errorType types.Type) bool {
	bad := false
	isEmptyString := func(e ast.Expr) bool {
		lit, ok := e.(*ast.BasicLit)
		return ok && lit.Kind == token.STRING && lit.Value == `""`
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			empty, hasErr := false, false
			for _, r := range v.Results {
				if isEmptyString(r) {
					empty = true
				}
				if tv, ok := info.Types[r]; ok && types.Identical(tv.Type, errorType) {
					if id, isNil := r.(*ast.Ident); !isNil || id.Name != "nil" {
						hasErr = true
					}
				} else if call, ok := r.(*ast.CallExpr); ok {
					// fmt.Errorf(...) and friends have type error.
					if tv, ok := info.Types[call]; ok && types.Identical(tv.Type, errorType) {
						hasErr = true
					}
				}
			}
			if empty && !hasErr {
				bad = true
			}
		case *ast.AssignStmt:
			for _, r := range v.Rhs {
				if isEmptyString(r) {
					bad = true
				}
			}
		}
		return true
	})
	return bad
}
