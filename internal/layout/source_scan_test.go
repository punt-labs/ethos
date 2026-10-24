package layout_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// tempAllowed lists the functions that make a temporary name without a layout
// helper, each with the reason it is outside the path-class table. The key is
// "<repo-relative file>:<function>".
var tempAllowed = map[string]string{
	"internal/session/store.go:WriteCurrentSession":     "writes under $HOME/.punt-labs/ethos/sessions, outside any working tree",
	"internal/session/store.go:writeRoster":             "writes under $HOME/.punt-labs/ethos/sessions, outside any working tree",
	"internal/mission/active.go:writeSidecarFile":       "writes under the global root, outside any working tree",
	"internal/mission/id.go:writeCounterAtomic":         "writes the per-user mission counter under $HOME, outside any working tree",
	"internal/doctor/sandbox.go:hookInvocationObserved": "a throwaway sandbox directory in $TMPDIR, removed before the function returns",
	"internal/mission/delegation.go:writeAtomicFile":    "takes the pattern as a parameter; every caller must pass a layout helper (checked as helperArgs)",
}

// helperArgs maps a function that forwards a temp pattern to os.CreateTemp to
// the index of that parameter; every call to it must pass a layout helper.
var helperArgs = map[string]int{"writeAtomicFile": 1}

// tempFinding is one place a staging name is built without a layout helper.
type tempFinding struct {
	Func string
	Line int
	What string
}

// isLayoutCall reports whether e is a call to a function of package layout.
func isLayoutCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "layout"
}

// isTmpLiteral reports whether e is a string literal ending in ".tmp".
func isTmpLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && strings.HasSuffix(s, ".tmp")
}

// scanTempNames parses src and returns every os.CreateTemp or os.MkdirTemp
// whose name argument is not a layout call, every `x + ".tmp"`, and every call
// to a function in helperArgs that does not pass a layout call.
func scanTempNames(name, src string) ([]tempFinding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	var out []tempFinding
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			add := func(pos token.Pos, what string) {
				out = append(out, tempFinding{Func: fn.Name.Name, Line: fset.Position(pos).Line, What: what})
			}
			switch n := n.(type) {
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "os" &&
						(sel.Sel.Name == "CreateTemp" || sel.Sel.Name == "MkdirTemp") &&
						len(n.Args) == 2 && !isLayoutCall(n.Args[1]) {
						add(n.Pos(), "os."+sel.Sel.Name+" name is not built by a layout helper")
					}
				}
				if id, ok := n.Fun.(*ast.Ident); ok {
					if i, ok := helperArgs[id.Name]; ok && (len(n.Args) <= i || !isLayoutCall(n.Args[i])) {
						add(n.Pos(), id.Name+" pattern is not built by a layout helper")
					}
				}
			case *ast.BinaryExpr:
				if n.Op == token.ADD && (isTmpLiteral(n.X) || isTmpLiteral(n.Y)) {
					add(n.Pos(), `name built with + ".tmp"`)
				}
			}
			return true
		})
	}
	return out, nil
}

func TestScanFlagsHandBuiltTempNames(t *testing.T) {
	const src = `package p
import "os"
func a(dir, dest string) { os.CreateTemp(dir, "x-*.tmp") }
func b(dir string)       { os.MkdirTemp(dir, "stage-") }
func c(dest string) string { return dest + ".tmp" }
func d(dir, p string)    { os.CreateTemp(dir, p) }
func e(dir string)       { writeAtomicFile(dir, "r-*.tmp") }
func ok(dir, dest string) {
	os.CreateTemp(dir, layout.TmpPattern("r", ".yaml"))
	os.MkdirTemp(dir, layout.MigrateStagingPrefix("m"))
	writeAtomicFile(dir, layout.TmpPattern("r", ".yaml"))
	_ = layout.TmpPath(dest)
}
`
	got, err := scanTempNames("p.go", src)
	if err != nil {
		t.Fatal(err)
	}
	var funcs []string
	for _, f := range got {
		funcs = append(funcs, f.Func)
	}
	if want := "a b c d e"; strings.Join(funcs, " ") != want {
		t.Errorf("flagged functions = %v, want %q: %+v", funcs, want, got)
	}
}

// TestTempNamesComeFromLayoutHelpers fails when a writer under internal/ or
// cmd/ builds a staging name by hand, because no class-4 entry then covers it.
// A function that must do so for a path outside the working tree is listed in
// tempAllowed with its reason; a stale entry also fails.
func TestTempNamesComeFromLayoutHelpers(t *testing.T) {
	root := filepath.Join("..", "..")
	used := make(map[string]bool)
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
			switch {
			case d.IsDir() && rel == "internal/layout":
				return filepath.SkipDir // defines the helpers
			case d.IsDir() || !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go"):
				return nil
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			found, err := scanTempNames(rel, string(src))
			if err != nil {
				return err
			}
			for _, f := range found {
				key := rel + ":" + f.Func
				if _, ok := tempAllowed[key]; ok {
					used[key] = true
					continue
				}
				t.Errorf("%s:%d: %s; use a helper from internal/layout/temp.go, or list %q in tempAllowed with a reason", rel, f.Line, f.What, key)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var stale []string
	for key := range tempAllowed {
		if !used[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("tempAllowed entry %q matches no hand-built temp name; remove it", key)
	}
}
