package resolve

import (
	"go/ast"
	"go/token"
	"go/types"
	"os"
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
//
//   - in a block that runs when the error a locator returned is non-nil (an
//     `if err != nil`, the else of `if err == nil`, a `case err != nil`, the
//     default of `switch err { case nil: }`), the block may not return, or
//     assign, the empty string without returning an error — unless the
//     condition separates the two cases by testing the error against
//     resolve.ErrNotInRepo or fs.ErrNotExist (resolved by object, not by name);
//   - a locator's error may not be assigned to `_` or dropped by calling the
//     locator as a statement.
//
// This is a tripwire for the shapes ethos code uses and their common variants,
// not a proof: it does not follow the error through a variable, an interface
// or a closure called elsewhere. It loads the code for linux, darwin and
// windows, because files behind build tags are real code.
func TestNoFunctionTurnsALocateFailureIntoEmpty(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	found := map[string]bool{}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		pkgs, err := packages.Load(&packages.Config{
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
			Dir:  root, Tests: false,
			Env: append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0"),
		}, "./internal/...", "./cmd/...", "./plugin/...")
		require.NoError(t, err)
		for _, p := range pkgs {
			for _, e := range p.Errors {
				t.Fatalf("loading %s for %s: %v", p.PkgPath, goos, e)
			}
			for _, f := range p.Syntax {
				for _, v := range scanLocateFailures(p, f) {
					found[v] = true
				}
			}
		}
	}
	var violations []string
	for v := range found {
		violations = append(violations, v)
	}
	sort.Strings(violations)
	require.Empty(t, violations, "a located failure was turned into the empty string or dropped")
}

// scanLocateFailures returns "file:line: function" for each violation in f.
func scanLocateFailures(p *packages.Package, f *ast.File) []string {
	var out []string
	info := p.TypesInfo
	errorType := types.Universe.Lookup("error").Type()
	locatorCall := func(e ast.Expr) *ast.CallExpr {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return nil
		}
		var id *ast.Ident
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			id = fn
		case *ast.SelectorExpr:
			id = fn.Sel
		default:
			return nil
		}
		obj, ok := info.Uses[id].(*types.Func)
		if ok && obj.Pkg() != nil && strings.HasSuffix(obj.Pkg().Path(), "/internal/resolve") && locators[obj.Name()] {
			return call
		}
		return nil
	}
	// errorIndex is the position of the error among call's results, or -1.
	errorIndex := func(call *ast.CallExpr) int {
		tup, ok := info.TypeOf(call).(*types.Tuple)
		if !ok {
			return -1
		}
		for i := 0; i < tup.Len(); i++ {
			if types.Identical(tup.At(i).Type(), errorType) {
				return i
			}
		}
		return -1
	}
	// analyze checks one function body: a FuncDecl, or a function literal
	// outside any declaration (a package-level var).
	analyze := func(name string, body *ast.BlockStmt) {
		tracked := map[types.Object]bool{}
		ast.Inspect(body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.ExprStmt:
				if call := locatorCall(v.X); call != nil && errorIndex(call) >= 0 {
					out = append(out, p.Fset.Position(v.Pos()).String()+": "+name+": a locator's error is dropped")
				}
			case *ast.AssignStmt:
				if len(v.Rhs) != 1 {
					return true
				}
				call := locatorCall(v.Rhs[0])
				if call == nil {
					return true
				}
				i := errorIndex(call)
				if i < 0 || i >= len(v.Lhs) {
					return true
				}
				id, ok := v.Lhs[i].(*ast.Ident)
				if !ok {
					return true
				}
				if id.Name == "_" {
					out = append(out, p.Fset.Position(v.Pos()).String()+": "+name+": a locator's error is assigned to _")
					return true
				}
				if obj := info.ObjectOf(id); obj != nil && types.Identical(obj.Type(), errorType) {
					tracked[obj] = true
				}
			}
			return true
		})
		if len(tracked) == 0 {
			return
		}
		report := func(n ast.Node) {
			out = append(out, p.Fset.Position(n.Pos()).String()+": "+name)
		}
		ast.Inspect(body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.IfStmt:
				if testsNonNil(info, v.Cond, tracked) && !separatesCases(info, v.Cond) && emptiesWithoutError(info, v.Body, errorType) {
					report(v)
				}
				if testsNil(info, v.Cond, tracked) && v.Else != nil && emptiesWithoutError(info, v.Else, errorType) {
					report(v)
				}
			case *ast.SwitchStmt:
				var dflt *ast.CaseClause
				sawNilCase := false
				for _, st := range v.Body.List {
					cc := st.(*ast.CaseClause)
					if cc.List == nil {
						dflt = cc
						continue
					}
					for _, e := range cc.List {
						switch {
						case v.Tag == nil && testsNonNil(info, e, tracked) && !separatesCases(info, e):
							if emptiesWithoutError(info, &ast.BlockStmt{List: cc.Body}, errorType) {
								report(cc)
							}
						case v.Tag == nil && testsNil(info, e, tracked):
							sawNilCase = true
						case v.Tag != nil && isTracked(info, v.Tag, tracked) && isNilIdent(e):
							sawNilCase = true
						}
					}
				}
				if sawNilCase && dflt != nil && emptiesWithoutError(info, &ast.BlockStmt{List: dflt.Body}, errorType) {
					report(dflt)
				}
			}
			return true
		})
	}
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.FuncDecl:
			if v.Body != nil {
				analyze(v.Name.Name, v.Body)
			}
		case *ast.GenDecl:
			// A function literal in a package-level var is not inside a FuncDecl.
			ast.Inspect(v, func(n ast.Node) bool {
				if fl, ok := n.(*ast.FuncLit); ok {
					analyze("func literal", fl.Body)
					return false
				}
				return true
			})
		}
	}
	return out
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

func isTracked(info *types.Info, e ast.Expr, tracked map[types.Object]bool) bool {
	id, ok := e.(*ast.Ident)
	return ok && tracked[info.ObjectOf(id)]
}

// testsNonNil reports whether cond contains `err != nil` for a tracked error.
func testsNonNil(info *types.Info, cond ast.Expr, tracked map[types.Object]bool) bool {
	return comparesToNil(info, cond, tracked, token.NEQ)
}

// testsNil reports whether cond contains `err == nil` for a tracked error.
func testsNil(info *types.Info, cond ast.Expr, tracked map[types.Object]bool) bool {
	return comparesToNil(info, cond, tracked, token.EQL)
}

func comparesToNil(info *types.Info, cond ast.Expr, tracked map[types.Object]bool, op token.Token) bool {
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		b, ok := n.(*ast.BinaryExpr)
		if !ok || b.Op != op {
			return true
		}
		if isTracked(info, b.X, tracked) && isNilIdent(b.Y) {
			found = true
		}
		return true
	})
	return found
}

// separatesCases reports whether cond refers to resolve.ErrNotInRepo or
// fs.ErrNotExist (or os.ErrNotExist, the same value): the one distinction a
// caller may make before answering "". The variables are matched by object, so
// a local named ErrNotInRepo does not count.
func separatesCases(info *types.Info, cond ast.Expr) bool {
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		v, ok := info.Uses[id].(*types.Var)
		if !ok || v.Pkg() == nil || v.Parent() != v.Pkg().Scope() {
			return true // not a package-level variable
		}
		switch path := v.Pkg().Path(); {
		case v.Name() == "ErrNotInRepo" && strings.HasSuffix(path, "/internal/resolve"):
			found = true
		case v.Name() == "ErrNotExist" && (path == "io/fs" || path == "os"):
			found = true
		}
		return true
	})
	return found
}

// emptiesWithoutError reports whether body returns, or assigns, the empty
// string without handing an error back: `return ""`, `return "", nil`,
// `x = ""`.
func emptiesWithoutError(info *types.Info, body ast.Node, errorType types.Type) bool {
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
