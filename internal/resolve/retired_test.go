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
		for _, goarch := range []string{"amd64", "arm64"} {
			pkgs, err := packages.Load(&packages.Config{
				Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
				Dir:  root, Tests: false,
				Env: append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0"),
			}, "./internal/...", "./cmd/...", "./plugin/...")
			require.NoError(t, err)
			for _, p := range pkgs {
				for _, e := range p.Errors {
					t.Fatalf("loading %s for %s/%s: %v", p.PkgPath, goos, goarch, e)
				}
				for _, f := range p.Syntax {
					for _, v := range scanLocateFailures(p, f) {
						found[v] = true
					}
					for _, v := range scanLocatedUse(p, f) {
						found[v] = true
					}
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
				if testsNonNil(info, v.Cond, tracked) && !separatesCases(info, v.Cond, tracked) && emptiesWithoutError(info, v.Body, errorType) {
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
						case v.Tag == nil && testsNonNil(info, e, tracked) && !separatesCases(info, e, tracked):
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
		if (isTracked(info, b.X, tracked) && isNilIdent(b.Y)) || (isNilIdent(b.X) && isTracked(info, b.Y, tracked)) {
			found = true
		}
		return true
	})
	return found
}

// separatesCases reports whether cond runs its block only for the one error a
// caller may answer "" for: a conjunction (&&) with a positive
// errors.Is(err, resolve.ErrNotInRepo) or errors.Is(err, fs.ErrNotExist) (or
// os.ErrNotExist, the same value), or `err == <sentinel>`. Any || lets other
// errors in, and a negated test (`!errors.Is(...)`) runs the block for every
// other error: neither separates. The sentinels are matched by package-level
// object, so a local named ErrNotInRepo does not count.
func separatesCases(info *types.Info, cond ast.Expr, tracked map[types.Object]bool) bool {
	hasOr := false
	positive := false
	var walk func(e ast.Expr, negated bool)
	walk = func(e ast.Expr, negated bool) {
		switch v := e.(type) {
		case *ast.ParenExpr:
			walk(v.X, negated)
		case *ast.UnaryExpr:
			if v.Op == token.NOT {
				walk(v.X, !negated)
			}
		case *ast.BinaryExpr:
			switch v.Op {
			case token.LOR:
				hasOr = true
			case token.LAND:
				walk(v.X, negated)
				walk(v.Y, negated)
			case token.EQL:
				if !negated && ((isTracked(info, v.X, tracked) && isSentinel(info, v.Y)) || (isTracked(info, v.Y, tracked) && isSentinel(info, v.X))) {
					positive = true
				}
			}
		case *ast.CallExpr:
			if !negated && isErrorsIs(info, v) && len(v.Args) == 2 && isTracked(info, v.Args[0], tracked) && isSentinel(info, v.Args[1]) {
				positive = true
			}
		}
	}
	walk(cond, false)
	return positive && !hasOr
}

// isErrorsIs reports whether call is errors.Is.
func isErrorsIs(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == "errors" && fn.Name() == "Is"
}

// isSentinel reports whether e names resolve.ErrNotInRepo or fs.ErrNotExist
// (os.ErrNotExist), by package-level variable.
func isSentinel(info *types.Info, e ast.Expr) bool {
	var id *ast.Ident
	switch v := e.(type) {
	case *ast.Ident:
		id = v
	case *ast.SelectorExpr:
		id = v.Sel
	default:
		return false
	}
	v, ok := info.Uses[id].(*types.Var)
	if !ok || v.Pkg() == nil || v.Parent() != v.Pkg().Scope() {
		return false
	}
	path := v.Pkg().Path()
	return (v.Name() == "ErrNotInRepo" && strings.HasSuffix(path, "/internal/resolve")) ||
		(v.Name() == "ErrNotExist" && (path == "io/fs" || path == "os"))
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
			if len(v.Results) == 0 {
				bad = true // a bare return with named results hands back whatever they hold, a nil error included
				return true
			}
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
			// "" without an error, or any value with a literal nil error: the
			// failure was turned into success.
			last := v.Results[len(v.Results)-1]
			if (empty && !hasErr) || (isNilIdent(last) && len(v.Results) > 1) {
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

// matchingErrors are the Located fields and methods that carry the error for
// each part: using the part without looking at one of them ignores the error.
var matchingErrors = map[string][]string{
	"Store":    {"StoreErr", "StoreFailure", "Err"},
	"Ethos":    {"EthosErr", "StoreFailure", "Err"},
	"Checkout": {"CheckoutErr", "Err"},
}

// scanLocatedUse flags a function that uses a located part (.Store, .Ethos,
// .Checkout of a resolve.Located, or of a struct that embeds one) without
// looking at the error that goes with it on the same value: not
// .StoreErr/.StoreFailure()/.Err() for Store, and so on. A part read off a
// call result (`currentRoots().Checkout`) has no variable to check the error
// on, so it is flagged. It is a tripwire for the shapes ethos code uses, in
// every package: it does not follow the value through a field, an argument or a
// closure called elsewhere.
func scanLocatedUse(p *packages.Package, f *ast.File) []string {
	located := locatedType(p)
	if located == nil {
		return nil
	}
	info := p.TypesInfo
	var out []string
	analyze := func(name string, body *ast.BlockStmt) {
		type use struct {
			pos  token.Pos
			part string
			base types.Object // nil for a call result or other expression
		}
		var uses []use
		mentioned := map[types.Object]map[string]bool{}
		note := func(base types.Object, field string) {
			if mentioned[base] == nil {
				mentioned[base] = map[string]bool{}
			}
			mentioned[base][field] = true
		}
		ast.Inspect(body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			s := info.Selections[sel]
			if s == nil || !isLocatedMember(located, s.Obj()) {
				return true
			}
			base := baseObject(info, sel.X)
			if _, isPart := matchingErrors[sel.Sel.Name]; isPart && s.Kind() == types.FieldVal {
				uses = append(uses, use{sel.Pos(), sel.Sel.Name, base})
				return true
			}
			note(base, sel.Sel.Name) // an error field or a method such as Err
			return true
		})
		for _, u := range uses {
			ok := false
			if u.base != nil {
				for _, e := range matchingErrors[u.part] {
					if mentioned[u.base][e] {
						ok = true
					}
				}
			}
			if !ok {
				out = append(out, p.Fset.Position(u.pos).String()+": "+name+": ."+u.part+" is used without looking at its error")
			}
		}
	}
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.FuncDecl:
			if v.Body != nil {
				analyze(v.Name.Name, v.Body)
			}
		case *ast.GenDecl:
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

// locatedType finds resolve.Located among p's imports (or p itself).
func locatedType(p *packages.Package) *types.Struct {
	var pkg *types.Package
	if strings.HasSuffix(p.PkgPath, "/internal/resolve") {
		pkg = p.Types
	} else {
		for _, imp := range p.Types.Imports() {
			if strings.HasSuffix(imp.Path(), "/internal/resolve") {
				pkg = imp
			}
		}
	}
	if pkg == nil {
		return nil
	}
	obj := pkg.Scope().Lookup("Located")
	if obj == nil {
		return nil
	}
	st, _ := obj.Type().Underlying().(*types.Struct)
	return st
}

// isLocatedMember reports whether obj is a field of Located (reached directly or
// through an embedding) or one of its methods.
func isLocatedMember(located *types.Struct, obj types.Object) bool {
	switch o := obj.(type) {
	case *types.Var:
		for i := 0; i < located.NumFields(); i++ {
			if located.Field(i) == o {
				return true
			}
		}
	case *types.Func:
		sig, _ := o.Type().(*types.Signature)
		if sig == nil || sig.Recv() == nil {
			return false
		}
		t := sig.Recv().Type()
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		n, ok := types.Unalias(t).(*types.Named)
		return ok && n.Obj().Name() == "Located" && n.Obj().Pkg() != nil && strings.HasSuffix(n.Obj().Pkg().Path(), "/internal/resolve")
	}
	return false
}

// baseObject is the variable at the left of a selector chain (r in r.Checkout
// and r.Located.Checkout), or nil when the chain starts at a call or another
// expression.
func baseObject(info *types.Info, e ast.Expr) types.Object {
	for {
		switch v := e.(type) {
		case *ast.Ident:
			return info.ObjectOf(v)
		case *ast.SelectorExpr:
			e = v.X
		case *ast.ParenExpr:
			e = v.X
		case *ast.StarExpr:
			e = v.X
		default:
			return nil
		}
	}
}
