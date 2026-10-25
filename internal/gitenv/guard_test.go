package gitenv

import (
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// The scan loads the non-test packages under internal/, cmd/ and plugin/ with
// go/packages and checks the type-checked syntax, for each of linux, darwin
// and windows (files behind build tags are real code). tests/ is test
// infrastructure, not shipped code. Test files (_test.go) are not scanned.
//
// It flags every REFERENCE, not only calls, to anything that starts a program:
//
//   - os/exec: Command and CommandContext (called, or taken as a function
//     value: `f := exec.Command`), the *exec.Cmd methods Run, Start, Output
//     and CombinedOutput, and exec.Cmd literals and new(exec.Cmd);
//   - os.StartProcess, syscall.StartProcess, syscall.Exec, syscall.ForkExec;
//   - golang.org/x/sys/unix Exec*.
//
// A non-test package that imports internal/testhelpers also fails: the helper
// package starts git itself and is for tests.

var guardGOOS = []string{"linux", "darwin", "windows"}

// allowedProcessUses names every non-test Go file outside this package that
// starts a program, with exactly what it does there, counted. A file not
// listed may start none. A new call, a new method, a function value or an
// alias changes the list and fails the test, so a git command cannot slip in
// behind a variable, an alias or a wrapper. git itself is never listed: it
// goes through gitenv, which bounds it, sends SIGTERM before SIGKILL, and
// classifies its failure.
var allowedProcessUses = map[string][]string{
	// The sandbox runs an untrusted hook, and sh for a hook that returned
	// ENOEXEC; each gets SIGTERM on the process group, then a grace, then KILL.
	"internal/doctor/sandbox.go": {"exec.CommandContext", "exec.CommandContext", "(*exec.Cmd).Run", "(*exec.Cmd).Run"},

	// Not git.
	"cmd/ethos/ui.go":        {"exec.Command", "exec.Command", "(*exec.Cmd).Start", "(*exec.Cmd).Start"}, // opens the browser
	"cmd/ethos/attribute.go": {"exec.Command", "(*exec.Cmd).Run"},                                        // $EDITOR
	"cmd/ethos/uninstall.go": {"exec.Command", "(*exec.Cmd).Run"},                                        // the claude CLI

	// A test helper compiled into a non-test file: its callers set the bound.
	"internal/testhelpers/gitenv.go": {"exec.Command", "exec.Command", "(*exec.Cmd).CombinedOutput"},
}

var execMethods = map[string]bool{"Run": true, "Start": true, "Output": true, "CombinedOutput": true}

// startsProgram names obj when referencing it starts, or can start, a
// program; "" otherwise.
func startsProgram(obj types.Object) string {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return ""
	}
	pkg := fn.Pkg().Path()
	sig, _ := fn.Type().(*types.Signature)
	switch {
	case pkg == "os/exec" && (fn.Name() == "Command" || fn.Name() == "CommandContext") && sig.Recv() == nil:
		return "exec." + fn.Name()
	case pkg == "os/exec" && sig.Recv() != nil && execMethods[fn.Name()] && recvIsCmd(sig.Recv().Type()):
		return "(*exec.Cmd)." + fn.Name()
	case pkg == "os" && fn.Name() == "StartProcess":
		return "os.StartProcess"
	case pkg == "syscall" && (fn.Name() == "StartProcess" || fn.Name() == "Exec" || fn.Name() == "ForkExec"):
		return "syscall." + fn.Name()
	case pkg == "golang.org/x/sys/unix" && strings.HasPrefix(fn.Name(), "Exec"):
		return "unix." + fn.Name()
	}
	return ""
}

func recvIsCmd(t types.Type) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "os/exec" && n.Obj().Name() == "Cmd"
}

func isExecCmd(t types.Type) bool { return t != nil && recvIsCmd(t) }

// processUses returns, per file (repo-relative, slash-separated), the
// descriptors of every program start in the non-test packages, and the
// packages that import internal/testhelpers.
func processUses(t *testing.T, root string) (uses map[string][]string, helperImporters []string) {
	t.Helper()
	root, err := filepath.Abs(root)
	require.NoError(t, err)
	seen := map[string]map[string]bool{} // file -> "descriptor@pos" already counted (a file is loaded once per GOOS)
	uses = map[string][]string{}
	importers := map[string]bool{}
	for _, goos := range guardGOOS {
		cfg := &packages.Config{
			Mode:  packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
			Dir:   root,
			Env:   append(append([]string(nil), os.Environ()...), "GOOS="+goos, "CGO_ENABLED=0"),
			Tests: false,
		}
		pkgs, err := packages.Load(cfg, "./internal/...", "./cmd/...", "./plugin/...")
		require.NoError(t, err)
		for _, p := range pkgs {
			for _, e := range p.Errors {
				t.Fatalf("loading %s for %s: %v", p.PkgPath, goos, e)
			}
			if strings.HasSuffix(p.PkgPath, "/internal/gitenv") || strings.HasSuffix(p.PkgPath, "/internal/testhelpers") {
				if strings.HasSuffix(p.PkgPath, "/internal/gitenv") {
					continue
				}
			} else if _, ok := p.Imports[modulePath(p)+"/internal/testhelpers"]; ok {
				importers[p.PkgPath] = true
			}
			for _, f := range p.Syntax {
				pos := p.Fset.Position(f.Pos())
				rel, rerr := filepath.Rel(root, pos.Filename)
				require.NoError(t, rerr)
				rel = filepath.ToSlash(rel)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if seen[rel] == nil {
					seen[rel] = map[string]bool{}
				}
				add := func(n ast.Node, desc string) {
					key := desc + "@" + p.Fset.Position(n.Pos()).String()
					if seen[rel][key] {
						return
					}
					seen[rel][key] = true
					uses[rel] = append(uses[rel], desc)
				}
				ast.Inspect(f, func(n ast.Node) bool {
					switch v := n.(type) {
					case *ast.Ident:
						if d := startsProgram(p.TypesInfo.Uses[v]); d != "" {
							add(v, d)
						}
					case *ast.CompositeLit:
						if isExecCmd(p.TypesInfo.TypeOf(v)) {
							add(v, "exec.Cmd{}")
						}
					case *ast.CallExpr:
						if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "new" && len(v.Args) == 1 {
							if _, isBuiltin := p.TypesInfo.Uses[id].(*types.Builtin); isBuiltin && isExecCmd(p.TypesInfo.TypeOf(v.Args[0])) {
								add(v, "new(exec.Cmd)")
							}
						}
					}
					return true
				})
			}
		}
	}
	for f := range importers {
		helperImporters = append(helperImporters, f)
	}
	sort.Strings(helperImporters)
	for _, v := range uses {
		sort.Strings(v)
	}
	return uses, helperImporters
}

// modulePath is the module path of p, from its own package path.
func modulePath(p *packages.Package) string {
	i := strings.Index(p.PkgPath, "/internal/")
	if j := strings.Index(p.PkgPath, "/cmd/"); j >= 0 && (i < 0 || j < i) {
		i = j
	}
	if k := strings.Index(p.PkgPath, "/plugin/"); k >= 0 && (i < 0 || k < i) {
		i = k
	}
	if i < 0 {
		return p.PkgPath
	}
	return p.PkgPath[:i]
}

// Every program ethos starts is accounted for. git goes through gitenv; the
// files that start anything else are listed above with exactly what they do.
func TestNoUnaccountedProcessStarts(t *testing.T) {
	found, importers := processUses(t, filepath.Join("..", ".."))
	var files []string
	for f := range found {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		want, ok := allowedProcessUses[f]
		if !ok {
			t.Errorf("%s starts a program outside gitenv (%v): run git through gitenv.Output, OutputEnv or Run, "+
				"or list the file in allowedProcessUses with what it does", f, found[f])
			continue
		}
		w := append([]string(nil), want...)
		sort.Strings(w)
		assert.Equal(t, w, found[f], "%s: the allowlist and the source disagree", f)
	}
	for f := range allowedProcessUses {
		assert.Contains(t, found, f, "%s is allowlisted but starts nothing: remove it from the list", f)
	}
	assert.Empty(t, importers, "non-test packages must not import internal/testhelpers, which starts git itself")
}
