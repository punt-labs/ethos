package mission

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keyRenamers are the calls whose result is a rewritten map key. A call whose
// function is itself a call (w.pass(mode)(k)) is one too.
var keyRenamers = map[string]bool{
	"Text": true, "Path": true, "HomePaths": true, "Content": true, "Prose": true,
	"scrubKey": true, "freeKey": true, "freeHeaderKey": true, "free": true,
}

// renameAssemblySites are the functions allowed to build a renamed-key map or
// rewrite a key node: the shared helper and the two walkers that apply its plan.
var renameAssemblySites = map[string]bool{
	"redact.RenameKeys": true, "redact.PlanKeys": true, "redact.planKeys": true,
	"mission.walkMapping": true, "mission.redactOrdered": true, "redact.scrub": true,
	// walk rewrites a scalar VALUE node (n.Value = pass(n.Value)); a key node
	// reaches it only through walkMapping, which applies the plan.
	"mission.walk": true,
}

// renamedKeyWrites returns "<file>:<func>" for every function outside the
// allowlist that writes a rewritten key: into a map (m[nk] = v where nk is
// tainted by a renaming call, or a map write whose key is neither the range
// variable nor a constant), through SetMapIndex, through SetString or Set on a
// key value, into a mapping-key node's Value, or into an ojson's keys.
//
// Taint starts at a call to a renamer (or to a func-typed parameter, or to a
// function call whose function is itself a call) and at redactedKey, and flows
// through assignments, multi-value assignments, var declarations, copies, and
// the receivers of SetString and Set.
func renamedKeyWrites(t *testing.T, sources map[string]string) []string {
	t.Helper()
	var bad []string
	fset := token.NewFileSet()
	for path, src := range sources {
		f, err := parser.ParseFile(fset, path, src, 0)
		require.NoError(t, err, path)
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if renameAssemblySites[f.Name.Name+"."+fd.Name.Name] {
				continue
			}
			funcParams := map[string]bool{} // func-typed parameters: a call is a renamer
			mapIdents := map[string]bool{}  // identifiers known to hold a map
			if fd.Type.Params != nil {
				for _, p := range fd.Type.Params.List {
					for _, n := range p.Names {
						switch ft := p.Type.(type) {
						case *ast.FuncType:
							// Only func(string) string can rename a key.
							if isStringToString(ft) {
								funcParams[n.Name] = true
							}
						case *ast.MapType:
							mapIdents[n.Name] = true
						}
					}
				}
			}
			renamer := func(e ast.Expr) bool { return callsRenamerWith(e, funcParams) }
			tainted := map[string]bool{}
			rangeKeys := map[string]bool{}
			markAssign := func(lhs []ast.Expr, rhs []ast.Expr) bool {
				changed := false
				for i, l := range lhs {
					id, isID := l.(*ast.Ident)
					if !isID || i >= len(rhs) && len(rhs) != 1 {
						continue
					}
					r := rhs[0]
					if i < len(rhs) {
						r = rhs[i]
					}
					if (renamer(r) || mentionsIdent(r, "redactedKey") || usesTainted(r, tainted)) && !tainted[id.Name] {
						tainted[id.Name] = true
						changed = true
					}
					if isMapExpr(r) {
						mapIdents[id.Name] = true
					}
				}
				return changed
			}
			// Fixpoint: copies of tainted values are tainted.
			for changed := true; changed; {
				changed = false
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.AssignStmt:
						if markAssign(n.Lhs, n.Rhs) {
							changed = true
						}
					case *ast.ValueSpec:
						lhs := make([]ast.Expr, len(n.Names))
						for i, id := range n.Names {
							lhs[i] = id
						}
						if markAssign(lhs, n.Values) {
							changed = true
						}
						if _, isMap := n.Type.(*ast.MapType); isMap {
							for _, id := range n.Names {
								mapIdents[id.Name] = true
							}
						}
					case *ast.RangeStmt:
						for _, e := range []ast.Expr{n.Key, n.Value} {
							if id, ok := e.(*ast.Ident); ok {
								rangeKeys[id.Name] = true
							}
						}
					case *ast.CallExpr:
						// nk.SetString(f(k)) taints nk.
						if sel, ok := n.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "SetString" || sel.Sel.Name == "Set") && len(n.Args) > 0 {
							if id, ok := sel.X.(*ast.Ident); ok && (renamer(n.Args[0]) || usesTainted(n.Args[0], tainted)) && !tainted[id.Name] {
								tainted[id.Name] = true
								changed = true
							}
						}
					}
					return true
				})
			}
			flag := func() { bad = append(bad, filepath.ToSlash(path)+":"+fd.Name.Name) }
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.AssignStmt:
					for i, l := range n.Lhs {
						switch l := l.(type) {
						case *ast.IndexExpr:
							if renamer(l.Index) || usesTainted(l.Index, tainted) {
								flag()
							}
							if base, ok := l.X.(*ast.Ident); ok && mapIdents[base.Name] && passFile(path) && !rangeKeyOrConst(l.Index, rangeKeys) {
								flag()
							}
							if sel, ok := l.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "keys" {
								flag()
							}
						case *ast.SelectorExpr:
							if l.Sel.Name == "Value" && i < len(n.Rhs) && (renamer(n.Rhs[i]) || usesTainted(n.Rhs[i], tainted)) {
								flag()
							}
						}
					}
				case *ast.CallExpr:
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
						switch sel.Sel.Name {
						case "SetMapIndex":
							if len(n.Args) > 0 && (renamer(n.Args[0]) || usesTainted(n.Args[0], tainted)) {
								flag()
							}
						case "SetString":
							if len(n.Args) > 0 && renamer(n.Args[0]) {
								flag()
							}
						}
					}
				}
				return true
			})
		}
	}
	sort.Strings(bad)
	return bad
}

// passFiles are the files that hold the redaction passes. In them every write
// to a map must use the range variable or a constant as its key: a key built
// any other way is a rename. (Elsewhere the repo writes thousands of unrelated
// maps, and without type information the scan cannot tell a record map from
// those; the taint rules above apply everywhere.)
var passFiles = map[string]bool{
	"internal/redact/redact.go": true, "internal/mission/redact.go": true,
	"internal/hook/audit_entry.go": true,
	"internal/hook/audit_guard.go": true, "internal/hook/audit_migrate.go": true,
}

func passFile(path string) bool { return passFiles[filepath.ToSlash(path)] }

func isStringToString(ft *ast.FuncType) bool {
	isString := func(fl *ast.FieldList) bool {
		if fl == nil || len(fl.List) != 1 || len(fl.List[0].Names) > 1 {
			return false
		}
		id, ok := fl.List[0].Type.(*ast.Ident)
		return ok && id.Name == "string"
	}
	return isString(ft.Params) && isString(ft.Results)
}

// isMapExpr reports whether e is make(map...) or a map composite literal.
func isMapExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.CompositeLit:
		_, ok := x.Type.(*ast.MapType)
		return ok
	case *ast.CallExpr:
		if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "make" && len(x.Args) > 0 {
			_, isMap := x.Args[0].(*ast.MapType)
			return isMap
		}
	}
	return false
}

// rangeKeyOrConst reports whether a map write's key is a range key variable
// or a constant (a literal, or a name in capitals).
func rangeKeyOrConst(e ast.Expr, rangeKeys map[string]bool) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		return rangeKeys[x.Name] || (x.Name != "" && x.Name[0] >= 'A' && x.Name[0] <= 'Z')
	}
	return false
}

func mentionsIdent(e ast.Expr, name string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

func usesTainted(e ast.Expr, tainted map[string]bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && tainted[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

func callsRenamerWith(e ast.Expr, funcParams map[string]bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			switch fn := c.Fun.(type) {
			case *ast.Ident:
				found = found || keyRenamers[fn.Name] || funcParams[fn.Name]
			case *ast.SelectorExpr:
				found = found || keyRenamers[fn.Sel.Name]
			case *ast.CallExpr:
				found = true // w.pass(mode)(k)
			}
		}
		return !found
	})
	return found
}

// TestRenamedKeysGoThroughTheHelper: only the shared helper and the two
// walkers that apply its plan write a renamed key into a map, a key node or
// an ojson's keys.
func TestRenamedKeysGoThroughTheHelper(t *testing.T) {
	sources := map[string]string{}
	for p, s := range loadSources(t, filepath.Join("..", "..")) {
		if strings.HasPrefix(p, "internal/") || strings.HasPrefix(p, "cmd/") {
			sources[p] = s
		}
	}
	assert.Empty(t, renamedKeyWrites(t, sources))
}

// TestRenamedKeysGoThroughTheHelper_Mutants: the five forms the review listed,
// and a few more, each in a function outside the allowlist, are flagged.
func TestRenamedKeysGoThroughTheHelper_Mutants(t *testing.T) {
	for name, src := range map[string]string{
		"nk variable":     "package p\nfunc f(m, out map[string]any, r R) {\n\tfor k, v := range m {\n\t\tnk := r.Text(k)\n\t\tout[nk] = v\n\t}\n}\n",
		"freeKey":         "package p\nfunc f(m, out map[string]any) {\n\tfor k, v := range m {\n\t\tout[freeKey(out, k)] = v\n\t}\n}\n",
		"free redacted":   "package p\nfunc f(m, out map[string]any) {\n\tfor _, v := range m {\n\t\tout[free(out, redactedKey)] = v\n\t}\n}\n",
		"node value":      "package p\nfunc f(w W, k N, mode int) {\n\tk.Value = w.pass(mode)(k.Value)\n}\n",
		"ojson keys":      "package p\nfunc f(out O, nk string) {\n\tout.keys[0] = nk\n}\n",
		"direct":          "package p\nfunc f(m, out map[string]any, r R) {\n\tfor k, v := range m {\n\t\tout[r.Text(k)] = v\n\t}\n}\n",
		"home paths":      "package p\nfunc g(m, out map[string]any) {\n\tfor k, v := range m {\n\t\tout[HomePaths(k)] = v\n\t}\n}\n",
		"reflect":         "package p\nfunc h(out, k, v V) {\n\tout.SetMapIndex(scrubKey(k, f), v)\n}\n",
		"setstring":       "package p\nfunc f(key, k V, f func(string) string, out V) {\n\tnk := reflect.New(key.Type()).Elem()\n\tnk.SetString(f(k.String()))\n\tout.SetMapIndex(nk, k)\n}\n",
		"copy of taint":   "package p\nfunc f(m, out map[string]any, r R) {\n\tfor k, v := range m {\n\t\tnk := r.Text(k)\n\t\tcp := nk\n\t\tout[cp] = v\n\t}\n}\n",
		"multi assign":    "package p\nfunc f(m, out map[string]any, r R) {\n\tfor k, v := range m {\n\t\ta, nk := 1, r.Text(k)\n\t\t_ = a\n\t\tout[nk] = v\n\t}\n}\n",
		"var decl":        "package p\nfunc f(m, out map[string]any, r R) {\n\tfor k, v := range m {\n\t\tvar nk = r.Text(k)\n\t\tout[nk] = v\n\t}\n}\n",
		"func param":      "package p\nfunc f(m, out map[string]any, rename func(string) string) {\n\tfor k, v := range m {\n\t\tout[rename(k)] = v\n\t}\n}\n",
		"non range key":   "package p\nfunc f(m map[string]any, name string, v any) {\n\tout := map[string]any{}\n\tout[name] = v\n}\n",
		"redactedKey var": "package p\nfunc f(out map[string]any, v any) {\n\tnk := redactedKey\n\tout[nk] = v\n}\n",
	} {
		path := "internal/x/x.go"
		if name == "non range key" {
			path = "internal/mission/redact.go"
		}
		bad := renamedKeyWrites(t, map[string]string{path: src})
		assert.NotEmpty(t, bad, name)
	}
	assert.Empty(t, renamedKeyWrites(t, map[string]string{"internal/x/x.go": "package p\nfunc ok(m, out map[string]any) {\n\tfor k, v := range m {\n\t\tout[k] = v\n\t}\n}\n"}))
	assert.Empty(t, renamedKeyWrites(t, map[string]string{"internal/redact/x.go": "package redact\nfunc RenameKeys(m, out map[string]any, r R) {\n\tfor k, v := range m {\n\t\tout[r.Text(k)] = v\n\t}\n}\n"}), "the helper itself is allowed")
}
