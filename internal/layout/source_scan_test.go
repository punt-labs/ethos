package layout_test

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
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

// tempAllowed is keyed "<repo-relative file>:<function>". Sites either write
// outside the working tree, do not create files, or forward a checked pattern
// through the explicitly enumerated atomic-writer chain below.
var tempAllowed = map[string]allowance{
	"internal/session/store.go:WriteCurrentSession":             {1, "writes under $HOME/.punt-labs/ethos/sessions, outside any working tree"},
	"internal/session/store.go:writeRoster":                     {1, "writes under $HOME/.punt-labs/ethos/sessions, outside any working tree"},
	"internal/mission/active.go:writeSidecarFile":               {1, "writes under the global root, outside any working tree"},
	"internal/mission/id.go:writeCounterAtomic":                 {1, "writes the per-user mission counter under $HOME, outside any working tree"},
	"internal/hook/generate_agents.go:buildAgentFile":           {1, "a shell command in generated agent frontmatter that skips .tmp/ scratch paths; no file is created"},
	"internal/mission/delegation.go:writeAtomicFile":            {1, "forwards its pattern parameter to writeAtomicFileWithOutcome; callers are checked by helperArgs"},
	"internal/mission/delegation.go:writeAtomicFileWithOutcome": {1, "forwards its pattern parameter to writeAtomicFileWithWarning; callers are checked by helperArgs"},
	"internal/mission/delegation.go:writeAtomicFileWithWarning": {1, "forwards its guarded pattern parameter to installAtomicFile; callers are checked by helperArgs"},
	"internal/mission/delegation.go:restoreAtomicFile":          {1, "forwards its rollback pattern parameter to installAtomicFile; callers are checked by helperArgs"},
	"internal/mission/delegation.go:installAtomicFile":          {1, "creates one temp from its pattern parameter for standalone paths; repository records use localReplace; callers are checked by helperArgs"},
	"internal/mission/scrub.go:scrubWrite":                      {1, "forwards its pattern parameter to writeAtomicFileWithWarning; callers are checked by helperArgs"},
}

// helperArgs maps each function in the parameterized atomic-writer chain to
// its pattern parameter. The declaration guard below verifies that position.
// Calls must pass a layout helper, except for the six counted forwarding/creation
// sites above. No suffix-based exemption applies.
var helperArgs = map[string]int{
	"writeAtomicFile":            1,
	"writeAtomicFileWithOutcome": 1,
	"writeAtomicFileWithWarning": 1,
	"restoreAtomicFile":          1,
	"installAtomicFile":          1,
	"scrubWrite":                 1,
}

func tempAllowanceError(key string, findings []tempFinding) error {
	a, ok := tempAllowed[key]
	if !ok {
		return fmt.Errorf("%s has no tempAllowed entry", key)
	}
	if len(findings) != a.count {
		return fmt.Errorf("%s has %d sites, tempAllowed allows %d", key, len(findings), a.count)
	}
	return nil
}

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

// layoutTempFuncs returns the names of the functions declared in
// internal/layout/temp.go, the file of staging-name helpers.
func layoutTempFuncs() (map[string]bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "temp.go", nil, 0)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
			out[fd.Name.Name] = true
		}
	}
	return out, nil
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

	// Only a function declared in temp.go builds a staging name; any other
	// layout.X(...) call may not carry TmpMarker.
	tempFuncs, err := layoutTempFuncs()
	if err != nil {
		return nil, err
	}
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
		return ok && layoutNames[id.Name] && tempFuncs[sel.Sel.Name]
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
func n(dir string)         { os.CreateTemp(dir, layout.NotAHelper("r")) }
func o(dir string)         { restoreAtomicFile(dir, "r-*.tmp") }
func p(dir string)         { writeAtomicFileWithOutcome(dir, "r-*.tmp") }
func q(dir string)         { writeAtomicFileWithWarning(dir, "r-*.tmp") }
func r(dir string)         { installAtomicFile(dir, "r-*.tmp") }
func s(dir string)         { scrubWrite(dir, "r-*.tmp") }

func ok(dir, dest string) {
	os.CreateTemp(dir, layout.TmpPattern("r", ".yaml"))
	os.MkdirTemp(dir, layout.MigrateStagingPrefix("m"))
	osx.CreateTemp(dir, lay.TmpPattern("r", ".yaml"))
	writeAtomicFile(dir, layout.TmpPattern("r", ".yaml"))
	restoreAtomicFile(dir, layout.TmpPattern("r", ".yaml"))
	writeAtomicFileWithOutcome(dir, lay.TmpPattern("r", ".yaml"))
	writeAtomicFileWithWarning(dir, lay.TmpPattern("r", ".yaml"))
	installAtomicFile(dir, lay.TmpPattern("r", ".yaml"))
	scrubWrite(dir, lay.TmpPattern("r", ".yaml"))
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
	want := "tmpSuffix viaVar a b c d e f g h i j k l m n o p q r s"
	if strings.Join(funcs, " ") != want {
		t.Errorf("flagged = %v, want %q: %+v", funcs, want, got)
	}
}

func TestScanAtomicTempCallers(t *testing.T) {
	for helper := range helperArgs {
		for _, tc := range []struct {
			name, pattern string
			want          int
		}{
			{"literal", `"unchecked-*"`, 1},
			{"dynamic", "uncheckedPattern", 1},
			{"layout", `layout.TmpPattern("record", ".yaml")`, 0},
		} {
			t.Run(helper+"/"+tc.name, func(t *testing.T) {
				args := "dir, " + tc.pattern + ", dest, data"
				switch helper {
				case "writeAtomicFileWithOutcome":
					args += ", nil"
				case "writeAtomicFileWithWarning":
					args += `, nil, "installed"`
				}
				src := fmt.Sprintf(`package p
import "github.com/punt-labs/ethos/v4/internal/layout"
func caller() { %s(%s) }`, helper, args)
				got, err := scanTempNames("p.go", src)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != tc.want {
					t.Fatalf("findings = %+v, want %d", got, tc.want)
				}
				if len(got) > 0 {
					if got[0].Func != "caller" || got[0].What != helper+" pattern is not built by a layout helper" {
						t.Fatalf("wrong caller finding: %+v", got)
					}
					if err := tempAllowanceError("internal/mission/delegation.go:caller", got); err == nil {
						t.Fatal("unchecked caller accepted by allowances")
					}
				}
			})
		}
	}
}

// Pin the six exact forwarding/creation edges and their outer pattern formals.
// For these helpers only, also forbid direct writes, shadow bindings and
// address-taking of pattern anywhere in the body, including nested closures and
// loop posts. This deliberately
// conservative structural restriction avoids control-flow/dataflow analysis:
// even an unreachable or later write requires review. It is not a general proof
// against dynamic aliasing, unsafe code or arbitrary changes to callees.
func atomicTempForwardingError(name, src string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return err
	}
	type edge struct {
		callee string
		args   []string
	}
	expected := map[string]edge{
		"writeAtomicFile":            {"writeAtomicFileWithOutcome", []string{"dir", "pattern", "destPath", "data", "nil"}},
		"writeAtomicFileWithOutcome": {"writeAtomicFileWithWarning", []string{"dir", "pattern", "destPath", "data", "out", `"delegation: update installed"`}},
		"writeAtomicFileWithWarning": {"installAtomicFile", []string{"dir", "pattern", "destPath", "data"}},
		"restoreAtomicFile":          {"installAtomicFile", []string{"dir", "pattern", "destPath", "data"}},
		"installAtomicFile":          {"os.CreateTemp", []string{"dir", "pattern"}},
	}
	if name == "scrub.go" {
		clear(expected)
		expected["scrubWrite"] = edge{"writeAtomicFileWithWarning", []string{"dir", "pattern", "dest", "data", "nil", `"mission records: " + dest + ": rewrite installed"`}}
	}
	for _, decl := range f.Decls {
		fn := declName(decl)
		want, ok := expected[fn]
		if !ok {
			continue
		}
		ft, body := atomicTempFunction(decl)
		if body == nil {
			return fmt.Errorf("%s has no atomic helper body", fn)
		}
		index, ok := helperArgs[fn]
		if !ok {
			return fmt.Errorf("%s has no checked pattern argument index", fn)
		}
		if err := atomicPatternParameterError(ft, index); err != nil {
			return fmt.Errorf("%s: pattern parameter contract: %w", fn, err)
		}
		if err := atomicPatternBindingError(body); err != nil {
			return fmt.Errorf("%s: %w", fn, err)
		}
		count := 0
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			callee := ""
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				callee = fun.Name
			case *ast.SelectorExpr:
				if id, ok := fun.X.(*ast.Ident); ok {
					callee = id.Name + "." + fun.Sel.Name
				}
			}
			if callee != want.callee || len(call.Args) != len(want.args) {
				return true
			}
			for i, arg := range call.Args {
				var text strings.Builder
				if err := format.Node(&text, fset, arg); err != nil || text.String() != want.args[i] {
					return true
				}
			}
			count++
			return true
		})
		if count != 1 {
			return fmt.Errorf("%s has %d exact calls forwarding to %s, want 1", fn, count, want.callee)
		}
		delete(expected, fn)
	}
	if len(expected) != 0 {
		return fmt.Errorf("missing atomic pattern forwarders: %v", expected)
	}
	return nil
}

// The scrub forwarder is a replaceable function literal; the other helpers
// are function declarations. Do not accept arbitrary initializers or aliases.
func atomicTempFunction(decl ast.Decl) (*ast.FuncType, *ast.BlockStmt) {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return d.Type, d.Body
	case *ast.GenDecl:
		if len(d.Specs) == 1 {
			if v, ok := d.Specs[0].(*ast.ValueSpec); ok && len(v.Names) == 1 && len(v.Values) == 1 {
				if fn, ok := v.Values[0].(*ast.FuncLit); ok {
					return fn.Type, fn.Body
				}
			}
		}
	}
	return nil, nil
}

func atomicTempBody(decl ast.Decl) *ast.BlockStmt {
	_, body := atomicTempFunction(decl)
	return body
}

// Grouped names each consume one argument. Require a unique named pattern
// formal at the very position checked by callers, rather than assuming its
// position from how the body uses it. Duplicate nonblank names are invalid Go
// parameter schemas even if the parser accepts them.
func atomicPatternParameterError(ft *ast.FuncType, index int) error {
	if ft == nil || ft.Params == nil {
		return fmt.Errorf("missing parameter list")
	}
	if index < 0 {
		return fmt.Errorf("invalid pattern argument index %d", index)
	}
	var names []string
	seen := make(map[string]bool)
	patternIndex := -1
	for _, field := range ft.Params.List {
		if field == nil || field.Type == nil {
			return fmt.Errorf("invalid parameter field at index %d", len(names))
		}
		if len(field.Names) == 0 {
			return fmt.Errorf("unnamed parameter at index %d", len(names))
		}
		for _, name := range field.Names {
			if name == nil || name.Name == "" {
				return fmt.Errorf("invalid formal name at index %d", len(names))
			}
			if name.Name != "_" && seen[name.Name] {
				return fmt.Errorf("duplicate formal name %q", name.Name)
			}
			seen[name.Name] = true
			if name.Name == "pattern" {
				patternIndex = len(names)
			}
			names = append(names, name.Name)
		}
	}
	if len(names) <= index {
		return fmt.Errorf("missing parameter at index %d", index)
	}
	if patternIndex < 0 {
		return fmt.Errorf("missing named pattern formal")
	}
	if patternIndex != index {
		return fmt.Errorf("pattern formal is at index %d, want %d", patternIndex, index)
	}
	return nil
}

func isPatternBinding(e ast.Expr) bool {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "pattern"
}

func atomicPatternBindingError(body *ast.BlockStmt) error {
	var found error
	check := func(e ast.Expr, kind string) {
		if isPatternBinding(e) {
			found = fmt.Errorf("pattern %s is forbidden in a parameter-forwarding helper", kind)
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		switch n := n.(type) {
		case *ast.AssignStmt: // =, :=, compound assignments, including tuples
			for _, lhs := range n.Lhs {
				check(lhs, "assignment or shadow declaration")
			}
		case *ast.ValueSpec: // var and const declarations
			for _, name := range n.Names {
				check(name, "shadow declaration")
			}
		case *ast.RangeStmt: // both = and :=, key or value
			check(n.Key, "range binding")
			check(n.Value, "range binding")
		case *ast.IncDecStmt:
			check(n.X, "increment/decrement")
		case *ast.FuncLit:
			for _, fields := range []*ast.FieldList{n.Type.Params, n.Type.Results} {
				if fields != nil {
					for _, field := range fields.List {
						for _, name := range field.Names {
							check(name, "closure binding")
						}
					}
				}
			}
		case *ast.UnaryExpr:
			if n.Op == token.AND {
				check(n.X, "address escape") // excludes direct pointer aliases
			}
		}
		return true
	})
	return found
}

func TestAtomicTempPatternForwarding(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "mission", "delegation.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if err := atomicTempForwardingError("delegation.go", src); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"unchecked pattern", "os.CreateTemp(dir, pattern)", `os.CreateTemp(dir, "unchecked-*")`},
		{"dynamic pattern", "os.CreateTemp(dir, pattern)", `os.CreateTemp(dir, uncheckedPattern)`},
		{"removed creator", "os.CreateTemp(dir, pattern)", "os.Open(dir)"},
		{"extra creator", "os.CreateTemp(dir, pattern)", "os.CreateTemp(dir, pattern); os.CreateTemp(dir, pattern)"},
		{"renamed creator", "func installAtomicFile(", "func movedAtomicFile("},
		{"unchecked forwarding", `return writeAtomicFileWithWarning(dir, pattern, destPath, data, out, "delegation: update installed")`, `return writeAtomicFileWithWarning(dir, "unchecked-*", destPath, data, out, "delegation: update installed")`},
		{"dynamic forwarding", `return writeAtomicFileWithWarning(dir, pattern, destPath, data, out, "delegation: update installed")`, `return writeAtomicFileWithWarning(dir, uncheckedPattern, destPath, data, out, "delegation: update installed")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(src, tc.old) != 1 {
				t.Fatalf("mutation anchor %q must occur exactly once", tc.old)
			}
			mutated := strings.Replace(src, tc.old, tc.replacement, 1)
			if _, err := parser.ParseFile(token.NewFileSet(), "mutated.go", mutated, 0); err != nil {
				t.Fatalf("mutation must remain parseable: %v", err)
			}
			if err := atomicTempForwardingError("delegation.go", mutated); err == nil {
				t.Fatal("atomic pattern guard accepted source mutation")
			}
		})
	}
}

type patternBindingCase struct {
	name, before, after string
	reject              bool
}

func atomicPatternBindingCases() []patternBindingCase {
	return []patternBindingCase{
		{"reassignment", `pattern = "unchecked-*";`, "", true},
		{"compound assignment", `pattern += "unchecked-*";`, "", true},
		{"tuple first", `pattern, dir = "unchecked-*", dir;`, "", true},
		{"tuple last", `dir, pattern = dir, "unchecked-*";`, "", true},
		{"mixed short declaration", `pattern, other := "unchecked-*", true; _ = other;`, "", true},
		{"short declaration shadow", `{ pattern := "unchecked-*";`, "}", true},
		{"var shadow", `{ var pattern = "unchecked-*";`, "}", true},
		{"const shadow", `{ const pattern = "unchecked-*";`, "}", true},
		{"range assignment", `for _, pattern = range []string{"unchecked-*"} {};`, "", true},
		{"range shadow", `for _, pattern := range []string{"unchecked-*"} {`, "}; return nil;", true},
		{"conditional assignment", `if pattern != "" { pattern = "unchecked-*" };`, "", true},
		{"loop post assignment", `for i := 0; i < 1; pattern = "unchecked-*" { i++ };`, "", true},
		{"closure assignment", `func() { pattern = "unchecked-*" }();`, "", true},
		{"closure parameter shadow", `return func(pattern string) error {`, `}("unchecked-*");`, true},
		{"parenthesized assignment", `(pattern) = "unchecked-*";`, "", true},
		{"pointer alias", `ptr := &pattern; *ptr = "unchecked-*";`, "", true},
		{"read only", `seen := pattern; _ = seen;`, "", false},
		{"value copy and other writes", `copyPattern := pattern; copyPattern += "read"; _ = copyPattern; scratch := "unchecked-*"; scratch, dir = pattern, dir; _ = scratch;`, "", false},
		{"unrelated field writes", `state := struct { pattern string }{pattern: pattern}; state.pattern = "unchecked-*"; ptr := &state.pattern; *ptr = "other"; _ = state;`, "", false},
	}
}

// Change only a named helper's body in an in-memory copy of the actual source.
func atomicPatternSource(src, name, before, after string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "delegation.go", src, 0)
	if err != nil {
		return "", err
	}
	for _, decl := range f.Decls {
		if declName(decl) != name {
			continue
		}
		body := atomicTempBody(decl)
		if body == nil {
			return "", fmt.Errorf("helper %s has no body", name)
		}
		start := fset.Position(body.Lbrace).Offset + 1
		end := fset.Position(body.Rbrace).Offset
		return src[:start] + "\n" + before + "\n" + src[start:end] + "\n" + after + "\n" + src[end:], nil
	}
	return "", fmt.Errorf("missing helper %s", name)
}

func TestAtomicTempPatternBindings(t *testing.T) {
	for _, file := range []struct {
		name    string
		helpers []string
	}{
		{"delegation.go", []string{"writeAtomicFile", "writeAtomicFileWithOutcome", "writeAtomicFileWithWarning", "restoreAtomicFile", "installAtomicFile"}},
		{"scrub.go", []string{"scrubWrite"}},
	} {
		data, err := os.ReadFile(filepath.Join("..", "mission", file.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := atomicTempForwardingError(file.name, string(data)); err != nil {
			t.Fatal(err)
		}
		for _, name := range file.helpers {
			t.Run(name, func(t *testing.T) {
				for _, tc := range atomicPatternBindingCases() {
					t.Run(tc.name, func(t *testing.T) {
						src, err := atomicPatternSource(string(data), name, tc.before, tc.after)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := parser.ParseFile(token.NewFileSet(), "mutated.go", src, 0); err != nil {
							t.Fatalf("mutation must remain parseable: %v", err)
						}
						err = atomicTempForwardingError(file.name, src)
						if tc.reject && err == nil {
							t.Fatal("atomic pattern guard accepted rebinding/shadowing mutation")
						}
						if !tc.reject && err != nil {
							t.Fatalf("legitimate pattern reads/other writes refused: %v", err)
						}
					})
				}
			})
		}
	}
}

// Locate the actual outer signature independently of the forwarding guard's
// declaration extractor. In particular, scrubWrite is a real function literal,
// not a function declaration synthesized around its body.
func atomicTempTestSignature(src, name string) (*token.FileSet, *ast.FuncType, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "signature.go", src, 0)
	if err != nil {
		return nil, nil, err
	}
	for _, decl := range f.Decls {
		if declName(decl) != name {
			continue
		}
		switch d := decl.(type) {
		case *ast.FuncDecl:
			return fset, d.Type, nil
		case *ast.GenDecl:
			if len(d.Specs) == 1 {
				if v, ok := d.Specs[0].(*ast.ValueSpec); ok && len(v.Values) == 1 {
					if fn, ok := v.Values[0].(*ast.FuncLit); ok {
						return fset, fn.Type, nil
					}
				}
			}
		}
		return nil, nil, fmt.Errorf("unsupported signature fixture %s", name)
	}
	return nil, nil, fmt.Errorf("missing signature fixture %s", name)
}

func atomicTempTestParameterTypes(t *testing.T, fset *token.FileSet, ft *ast.FuncType) []string {
	t.Helper()
	var types []string
	for _, field := range ft.Params.List {
		var text strings.Builder
		if err := format.Node(&text, fset, field.Type); err != nil {
			t.Fatal(err)
		}
		count := len(field.Names)
		if count == 0 {
			count = 1 // an unnamed parameter still consumes one argument
		}
		for range count {
			types = append(types, text.String())
		}
	}
	return types
}

func TestAtomicTempPatternParameters(t *testing.T) {
	for _, file := range []struct {
		name    string
		helpers []string
	}{
		{"delegation.go", []string{"writeAtomicFile", "writeAtomicFileWithOutcome", "writeAtomicFileWithWarning", "restoreAtomicFile", "installAtomicFile"}},
		{"scrub.go", []string{"scrubWrite"}},
	} {
		data, err := os.ReadFile(filepath.Join("..", "mission", file.name))
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		if err := atomicTempForwardingError(file.name, src); err != nil {
			t.Fatal(err)
		}
		originalFindings, err := scanTempNames(file.name, src)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range file.helpers {
			t.Run(name, func(t *testing.T) {
				fset, ft, err := atomicTempTestSignature(src, name)
				if err != nil {
					t.Fatal(err)
				}
				start := fset.Position(ft.Params.Opening).Offset + 1
				end := fset.Position(ft.Params.Closing).Offset
				originalTypes := atomicTempTestParameterTypes(t, fset, ft)
				dest := "destPath"
				if name == "scrubWrite" {
					dest = "dest"
				}
				prefix := "dir, pattern, " + dest + " string"
				if !strings.HasPrefix(src[start:end], prefix) {
					t.Fatalf("original parameter prefix changed: %q", src[start:end])
				}
				tail := src[start+len(prefix) : end]
				for _, tc := range []struct {
					name, params, wantError string
					sameTypes               bool
				}{
					{"grouped", prefix + tail, "", true},
					{"split", "dir string, pattern string, " + dest + " string" + tail, "", true},
					{"mixed grouping", "dir string, pattern, " + dest + " string" + tail, "", true},
					{"swap destination grouped", "dir, " + dest + ", pattern string" + tail, "pattern formal is at index 2, want 1", true},
					{"swap destination split", "dir string, " + dest + " string, pattern string" + tail, "pattern formal is at index 2, want 1", true},
					{"swap directory grouped", "pattern, dir, " + dest + " string" + tail, "pattern formal is at index 0, want 1", true},
					{"swap directory split", "pattern string, dir string, " + dest + " string" + tail, "pattern formal is at index 0, want 1", true},
					{"missing name", "dir, unchecked, " + dest + " string" + tail, "missing named pattern formal", true},
					{"blank name", "dir, _, " + dest + " string" + tail, "missing named pattern formal", true},
					{"duplicate pattern", "dir, pattern, pattern string" + tail, `duplicate formal name "pattern"`, true},
					{"duplicate other name", "dir, pattern, dir string" + tail, `duplicate formal name "dir"`, true},
					{"unnamed parameters", strings.Join(originalTypes, ", "), "unnamed parameter", true},
					{"missing slot", "dir string", "missing parameter at index 1", false},
					{"empty parameters", "", "missing parameter at index 1", false},
				} {
					t.Run(tc.name, func(t *testing.T) {
						// Only the parameter list changes. All bodies, calls, results
						// and surrounding declarations retain their original bytes.
						mutated := src[:start] + tc.params + src[end:]
						mfset, mft, err := atomicTempTestSignature(mutated, name)
						if err != nil {
							t.Fatalf("signature mutation must remain parseable: %v", err)
						}
						if same := reflect.DeepEqual(originalTypes, atomicTempTestParameterTypes(t, mfset, mft)); same != tc.sameTypes {
							t.Fatalf("unchanged argument types = %v, want %v", same, tc.sameTypes)
						}
						findings, err := scanTempNames(file.name, mutated)
						if err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(findings, originalFindings) {
							t.Fatalf("signature-only mutation changed scanner findings: %+v, originally %+v", findings, originalFindings)
						}
						err = atomicTempForwardingError(file.name, mutated)
						if tc.wantError == "" {
							if err != nil {
								t.Fatalf("valid signature rejected: %v", err)
							}
						} else if err == nil {
							t.Fatal("parameter guard accepted signature mutation")
						} else if !strings.HasPrefix(err.Error(), name+": pattern parameter contract: ") || !strings.Contains(err.Error(), tc.wantError) {
							t.Fatalf("want parameter-contract refusal %q, got %v", tc.wantError, err)
						}
					})
				}
			})
		}
	}
}

func TestAtomicTempSinkCallers(t *testing.T) {
	for _, file := range []struct {
		name    string
		helpers []string
	}{
		{"delegation.go", []string{"writeAtomicFileWithWarning", "restoreAtomicFile"}},
		{"scrub.go", []string{"scrubWrite"}},
	} {
		data, err := os.ReadFile(filepath.Join("..", "mission", file.name))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range file.helpers {
			for _, pattern := range []string{`"unchecked-*"`, "uncheckedPattern"} {
				t.Run(name+"/"+pattern, func(t *testing.T) {
					// Both install callers have the same call; isolate one body
					// so a mutation cannot silently cover the wrong forwarding edge.
					fset := token.NewFileSet()
					f, err := parser.ParseFile(fset, file.name, data, 0)
					if err != nil {
						t.Fatal(err)
					}
					start, end := 0, 0
					for _, decl := range f.Decls {
						if declName(decl) == name {
							body := atomicTempBody(decl)
							if body == nil {
								t.Fatalf("helper %s has no body", name)
							}
							start = fset.Position(body.Lbrace).Offset + 1
							end = fset.Position(body.Rbrace).Offset
						}
					}
					if start == 0 {
						t.Fatalf("missing helper %s", name)
					}
					src := string(data)
					body := src[start:end]
					callee := "installAtomicFile"
					if name == "scrubWrite" {
						callee = "writeAtomicFileWithWarning"
					}
					anchor := callee + "(dir, pattern,"
					if strings.Count(body, anchor) != 1 {
						t.Fatalf("mutation anchor %q must occur exactly once in %s", anchor, name)
					}
					mutated := src[:start] + strings.Replace(body, anchor, callee+"(dir, "+pattern+",", 1) + src[end:]
					// Replace the checked call, rather than add an unchecked one
					// beside it: the allowance count must stay one.
					findings, err := scanTempNames(file.name, mutated)
					if err != nil {
						t.Fatalf("mutation must remain parseable: %v", err)
					}
					var sites []tempFinding
					for _, finding := range findings {
						if finding.Func == name {
							sites = append(sites, finding)
						}
					}
					if len(sites) != 1 || tempAllowanceError("internal/mission/"+file.name+":"+name, sites) != nil {
						t.Fatalf("mutation must preserve the counted site: %+v", sites)
					}
					if err := atomicTempForwardingError(file.name, mutated); err == nil {
						t.Fatal("atomic pattern guard accepted unchecked sink caller")
					}
				})
			}
		}
	}
}

func TestAtomicTempAllowanceCounts(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "mission", "delegation.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"writeAtomicFileWithWarning", "installAtomicFile"} {
		t.Run(name, func(t *testing.T) {
			key := "internal/mission/delegation.go:" + name
			for _, extra := range []bool{false, true} {
				src := string(data)
				if extra {
					src, err = atomicPatternSource(src, name, "os.CreateTemp(dir, pattern);", "")
					if err != nil {
						t.Fatal(err)
					}
				}
				got, err := scanTempNames("delegation.go", src)
				if err != nil {
					t.Fatal(err)
				}
				var findings []tempFinding
				for _, finding := range got {
					if finding.Func == name {
						findings = append(findings, finding)
					}
				}
				want := 1
				if extra {
					want++
				}
				if len(findings) != want {
					t.Fatalf("findings = %+v, want %d", findings, want)
				}
				if err := tempAllowanceError(key, findings); (err != nil) != extra {
					t.Fatalf("extra creator = %v, allowance error = %v", extra, err)
				}
			}
		})
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
// A function that must break the rule for an out-of-tree path or a reviewed
// parameter-forwarding edge is listed in tempAllowed with its reason and number
// of sites; a new site in it fails, and so does an entry that no longer matches.
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
		if tempAllowanceError(key, fs) == nil {
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
