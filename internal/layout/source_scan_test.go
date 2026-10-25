package layout_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// allowance is a site the scan reports but the table need not classify: count
// is how many findings the function may have, reason says why they are outside
// the path-class table. A new site in an allowed function raises the count and
// fails the test.
type allowance struct {
	count  int
	reason string
}

// tempAllowed is keyed "<repo-relative file>:<function>". Everything here
// writes outside the repository's working tree: under $HOME, or under
// os.TempDir(), the user's temp directory.
var tempAllowed = map[string]allowance{
	"internal/session/store.go:WriteCurrentSession":     {1, "writes under $HOME/.punt-labs/ethos/sessions, outside any working tree"},
	"internal/session/store.go:writeRoster":             {1, "writes under $HOME/.punt-labs/ethos/sessions, outside any working tree"},
	"internal/mission/active.go:writeSidecarFile":       {1, "writes under the global root, outside any working tree"},
	"internal/mission/id.go:writeCounterAtomic":         {1, "writes the per-user mission counter under $HOME, outside any working tree"},
	"internal/doctor/sandbox.go:hookInvocationObserved": {1, "os.MkdirTemp(\"\", ...) puts the sandbox under os.TempDir(), not in the repo; it is removed before the function returns"},
	"internal/hook/generate_agents.go:buildAgentFile":   {1, "a shell command in generated agent frontmatter that skips .tmp/ scratch paths; no file is created"},
	"internal/mission/delegation.go:writeAtomicFile":    {1, "takes the pattern as a parameter; every caller must pass a layout helper (checked as helperArgs)"},
}

// helperArgs maps a function that forwards a temp pattern to os.CreateTemp to
// the index of that parameter; every call to it must pass a layout helper.
var helperArgs = map[string]int{"writeAtomicFile": 1}

const (
	layoutPath = "github.com/punt-labs/ethos/v4/internal/layout"
	osPath     = "os"
	ioutilPath = "io/ioutil"
)

// tempFinding is one place a staging name is built without a layout helper.
type tempFinding struct {
	Func string
	Line int
	What string
}

// importNames returns the local names f gives the package with import path p.
// An aliased import is found under its alias; a blank or dot import is not a
// name a selector can use.
func importNames(f *ast.File, p string) map[string]bool {
	out := make(map[string]bool)
	for _, s := range f.Imports {
		got, err := strconv.Unquote(s.Path.Value)
		if err != nil || got != p {
			continue
		}
		name := path.Base(got)
		if s.Name != nil {
			name = s.Name.Name
		}
		if name != "_" && name != "." {
			out[name] = true
		}
	}
	return out
}

// isTempLiteral reports whether e is a string literal that looks like a
// staging name: it contains ".tmp" or "tmp-".
func isTempLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && (strings.Contains(s, ".tmp") || strings.Contains(s, "tmp-"))
}

// declName names the declaration a finding is reported under: a function, or
// the first name of a package-level var or const block.
func declName(d ast.Decl) string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		return d.Name.Name
	case *ast.GenDecl:
		for _, s := range d.Specs {
			if v, ok := s.(*ast.ValueSpec); ok && len(v.Names) > 0 {
				return v.Names[0].Name
			}
		}
	}
	return ""
}

// scanTempNames parses src and reports, under the declaration that holds it:
//
//   - a call to os.CreateTemp, os.MkdirTemp, ioutil.TempFile or ioutil.TempDir,
//     through any import alias, whose pattern is not a call to package layout;
//   - a call to a function in helperArgs that does not pass a layout call;
//   - every other string literal containing ".tmp" or "tmp-", wherever it
//     appears: an operand of + or +=, a format string, a filepath.Join
//     argument, a named constant, a package-level var, a function literal body.
//
// It looks only at source it is given. Names built by a layout helper, and
// files created under os.TempDir(), are the caller's concern; see
// TestTempNamesComeFromLayoutHelpers.
func scanTempNames(name, src string) ([]tempFinding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	osNames := importNames(f, osPath)
	ioutilNames := importNames(f, ioutilPath)
	layoutNames := importNames(f, layoutPath)

	isLayoutCall := func(e ast.Expr) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && layoutNames[id.Name]
	}

	var out []tempFinding
	for _, d := range f.Decls {
		fn := declName(d)
		add := func(pos token.Pos, what string) {
			out = append(out, tempFinding{Func: fn, Line: fset.Position(pos).Line, What: what})
		}
		// A literal that is the pattern of a call already reported is not
		// reported again.
		var covered []ast.Expr
		ast.Inspect(d, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				id, ok := fun.X.(*ast.Ident)
				if !ok {
					return true
				}
				pkg := ""
				switch {
				case osNames[id.Name] && (fun.Sel.Name == "CreateTemp" || fun.Sel.Name == "MkdirTemp"):
					pkg = "os."
				case ioutilNames[id.Name] && (fun.Sel.Name == "TempFile" || fun.Sel.Name == "TempDir"):
					pkg = "ioutil."
				default:
					return true
				}
				if len(call.Args) != 2 {
					add(call.Pos(), pkg+fun.Sel.Name+" called with an unexpected argument list")
				} else if !isLayoutCall(call.Args[1]) {
					add(call.Pos(), pkg+fun.Sel.Name+" pattern is not built by a layout helper")
					covered = append(covered, call.Args[1])
				}
			case *ast.Ident:
				if i, ok := helperArgs[fun.Name]; ok {
					if len(call.Args) <= i || !isLayoutCall(call.Args[i]) {
						add(call.Pos(), fun.Name+" pattern is not built by a layout helper")
						if len(call.Args) > i {
							covered = append(covered, call.Args[i])
						}
					}
				}
			}
			return true
		})
		ast.Inspect(d, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok || !isTempLiteral(e) {
				return true
			}
			for _, c := range covered {
				if e.Pos() >= c.Pos() && e.End() <= c.End() {
					return true
				}
			}
			lit := e.(*ast.BasicLit).Value
			if len(lit) > 40 {
				lit = lit[:40] + `..."`
			}
			add(e.Pos(), "string literal looks like a staging name: "+lit)
			return true
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out, nil
}

func TestScanFlagsHandBuiltTempNames(t *testing.T) {
	const src = `package p

import (
	"fmt"
	"io/ioutil"
	"os"
	osx "os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
	lay "github.com/punt-labs/ethos/v4/internal/layout"
)

const tmpSuffix = ".tmp"

var viaVar = func(dest string) string { return dest + ".tmp" }

func a(dir, dest string) { os.CreateTemp(dir, "x-*.tmp") }
func b(dir string)       { os.MkdirTemp(dir, "stage-") }
func c(dest string) string { return dest + ".tmp" }
func d(dir, p string)    { os.CreateTemp(dir, p) }
func e(dir string)       { writeAtomicFile(dir, "r-*.tmp") }
func f(dest string) string {
	dest += ".tmp"
	return dest
}
func g(dest string) string { return fmt.Sprintf("%s.tmp", dest) }
func h(dir string) string  { return filepath.Join(dir, "x.tmp") }
func i(dir string) string  { return filepath.Join(dir, "tmp-stage") }
func j(dir string)         { osx.CreateTemp(dir, "x-*") }
func k(dir string)         { ioutil.TempFile(dir, "x-*") }
func l(dir string)         { ioutil.TempDir(dir, "x-") }
func m(dir string)         { os.CreateTemp(dir, lay.TmpPattern("r", ".yaml")+".x") }

func ok(dir, dest string) {
	os.CreateTemp(dir, layout.TmpPattern("r", ".yaml"))
	os.MkdirTemp(dir, layout.MigrateStagingPrefix("m"))
	osx.CreateTemp(dir, lay.TmpPattern("r", ".yaml"))
	writeAtomicFile(dir, layout.TmpPattern("r", ".yaml"))
	_ = layout.TmpPath(dest)
	_ = filepath.Join(os.TempDir(), "ethos")
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
	want := "tmpSuffix viaVar a b c d e f g h i j k l m"
	if strings.Join(funcs, " ") != want {
		t.Errorf("flagged = %v, want %q: %+v", funcs, want, got)
	}
}

// TestTempNamesComeFromLayoutHelpers checks one rule over every non-test .go
// file under internal/ and cmd/ except internal/layout, which defines the
// helpers: each os.CreateTemp, os.MkdirTemp, ioutil.TempFile and ioutil.TempDir
// takes its pattern from a function of package layout, and no other string
// literal in the tree looks like a staging name (contains ".tmp" or "tmp-").
// Writes under os.TempDir(), the user's temp directory, are outside the
// repository and out of scope; the test does not look for them.
//
// A function that must break the rule for a path outside the working tree is
// listed in tempAllowed with its reason and the number of sites; a new site in
// it fails, and so does an entry that no longer matches.
func TestTempNamesComeFromLayoutHelpers(t *testing.T) {
	root := filepath.Join("..", "..")
	found := make(map[string][]tempFinding)
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
			switch {
			case d.IsDir() && rel == "internal/layout":
				return filepath.SkipDir
			case d.IsDir() || !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go"):
				return nil
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fs, err := scanTempNames(rel, string(src))
			if err != nil {
				return err
			}
			for _, f := range fs {
				key := rel + ":" + f.Func
				found[key] = append(found[key], f)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for key, fs := range found {
		a, ok := tempAllowed[key]
		if ok && len(fs) == a.count {
			continue
		}
		for _, f := range fs {
			rel, _, _ := strings.Cut(key, ":")
			t.Errorf("%s:%d: %s; use a helper from internal/layout/temp.go, or list %q in tempAllowed with a reason and count", rel, f.Line, f.What, key)
		}
		if ok {
			t.Errorf("%s has %d sites, tempAllowed allows %d", key, len(fs), a.count)
		}
	}
	var stale []string
	for key := range tempAllowed {
		if _, ok := found[key]; !ok {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("tempAllowed entry %q matches no staging name; remove it", key)
	}
}
