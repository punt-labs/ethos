package gitenv

import (
	"go/ast"
	"go/parser"
	"go/token"
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
//   - golang.org/x/sys/unix Exec*;
//   - syscall.CreateProcess and golang.org/x/sys/windows CreateProcess*;
//   - syscall.Syscall, Syscall6, RawSyscall and RawSyscall6 whose number is
//     SYS_EXECVE, SYS_EXECVEAT or SYS_CLONE (the constant, by name).
//
// A non-test package with a cgo file also fails: C code can start a program
// where no Go reference shows it.
//
// This is a tripwire for the shapes ethos code uses and their common variants,
// not a proof. It does not chase reflect, an interface that hides the call, or
// a recovered panic.
//
// Every exec.Command and exec.CommandContext is recorded with its program: a
// string literal as `exec.Command("open")`, anything else as its identifier,
// `exec.Command(editor)`. The allowlist lists those exactly, so a reviewer sees
// what each file may run, and a literal "git" fails outside the test helper:
// git goes through gitenv.
//
// A non-test package that imports internal/testhelpers also fails: the helper
// package starts git itself and is for tests.

var guardGOOS = []string{"linux", "darwin", "windows"}

// guardGOARCH: files behind an architecture tag are real code too.
var guardGOARCH = []string{"amd64", "arm64"}

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
	"internal/doctor/sandbox.go": {"exec.CommandContext(hookPath)", `exec.CommandContext("sh")`, "(*exec.Cmd).Run", "(*exec.Cmd).Run"},

	// Not git.
	"cmd/ethos/ui.go":        {`exec.Command("open")`, `exec.Command("xdg-open")`, "(*exec.Cmd).Start", "(*exec.Cmd).Start"}, // opens the browser
	"cmd/ethos/attribute.go": {"exec.Command(editor)", "(*exec.Cmd).Run"},                                                    // $EDITOR
	"cmd/ethos/uninstall.go": {"exec.Command(claude)", "(*exec.Cmd).Run"},                                                    // the claude CLI

	// A test helper compiled into a non-test file: its callers set the bound.
	"internal/testhelpers/gitenv.go": {`exec.Command("git")`, "exec.Command(real)", "(*exec.Cmd).CombinedOutput"},
}

// rawSyscalls are the raw system-call entry points; the number is the first
// argument.
var rawSyscalls = map[string]bool{"Syscall": true, "Syscall6": true, "RawSyscall": true, "RawSyscall6": true}

// startingSyscalls are the system-call numbers that start a program or a
// process.
var startingSyscalls = map[string]bool{"SYS_EXECVE": true, "SYS_EXECVEAT": true, "SYS_CLONE": true}

// startsViaSyscall names call when it is a raw syscall whose number constant is
// one of startingSyscalls, in syscall or golang.org/x/sys/unix.
func startsViaSyscall(info *types.Info, call *ast.CallExpr) string {
	if len(call.Args) == 0 {
		return ""
	}
	var fid *ast.Ident
	switch f := call.Fun.(type) {
	case *ast.Ident:
		fid = f
	case *ast.SelectorExpr:
		fid = f.Sel
	default:
		return ""
	}
	fn, ok := info.Uses[fid].(*types.Func)
	if !ok || fn.Pkg() == nil || !rawSyscalls[fn.Name()] || (fn.Pkg().Path() != "syscall" && fn.Pkg().Path() != "golang.org/x/sys/unix") {
		return ""
	}
	var nid *ast.Ident
	switch a := call.Args[0].(type) {
	case *ast.Ident:
		nid = a
	case *ast.SelectorExpr:
		nid = a.Sel
	case *ast.CallExpr: // uintptr(syscall.SYS_EXECVE)
		if len(a.Args) == 1 {
			switch b := a.Args[0].(type) {
			case *ast.Ident:
				nid = b
			case *ast.SelectorExpr:
				nid = b.Sel
			}
		}
	}
	if nid != nil && startingSyscalls[nid.Name] {
		return fn.Pkg().Name() + "." + fn.Name() + "(" + nid.Name + ")"
	}
	return ""
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
	case pkg == "syscall" && (fn.Name() == "StartProcess" || fn.Name() == "Exec" || fn.Name() == "ForkExec" || fn.Name() == "CreateProcess"):
		return "syscall." + fn.Name()
	case pkg == "golang.org/x/sys/windows" && strings.HasPrefix(fn.Name(), "CreateProcess"):
		return "windows." + fn.Name()
	case pkg == "golang.org/x/sys/unix" && strings.HasPrefix(fn.Name(), "Exec"):
		return "unix." + fn.Name()
	}
	return ""
}

func recvIsCmd(t types.Type) bool {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "os/exec" && n.Obj().Name() == "Cmd"
}

// execCommandDescriptor describes a call to exec.Command or exec.CommandContext
// by its program: `exec.Command("open")` for a string literal,
// `exec.CommandContext(hookPath)` for an identifier, `exec.Command(<expr>)`
// for anything else. A function value (`f := exec.Command`) is not a call and
// is reported by the reference scan as "exec.Command".
func execCommandDescriptor(info *types.Info, call *ast.CallExpr) string {
	var id *ast.Ident
	switch f := call.Fun.(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return ""
	}
	d := startsProgram(info.Uses[id])
	if d != "exec.Command" && d != "exec.CommandContext" {
		return ""
	}
	i := 0
	if d == "exec.CommandContext" {
		i = 1
	}
	if len(call.Args) <= i {
		return d + "(<missing>)"
	}
	switch a := call.Args[i].(type) {
	case *ast.BasicLit:
		return d + "(" + a.Value + ")"
	case *ast.Ident:
		return d + "(" + a.Name + ")"
	}
	return d + "(<expr>)"
}

func isExecCmd(t types.Type) bool { return t != nil && recvIsCmd(t) }

// cgoFiles lists the files of p that import "C", whether they are compiled in
// or ignored because cgo is off here.
func cgoFiles(p *packages.Package) []string {
	var out []string
	for _, name := range append(append([]string(nil), p.GoFiles...), p.IgnoredFiles...) {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"C"` {
				out = append(out, name)
			}
		}
	}
	return out
}

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
	for _, target := range guardTargets() {
		goos := target[0]
		cfg := &packages.Config{
			Mode:  packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
			Dir:   root,
			Env:   append(append([]string(nil), os.Environ()...), "GOOS="+target[0], "GOARCH="+target[1], "CGO_ENABLED=0"),
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
			for _, name := range cgoFiles(p) {
				rel, _ := filepath.Rel(root, name)
				uses[filepath.ToSlash(rel)] = append(uses[filepath.ToSlash(rel)], `import "C"`)
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
						d := startsProgram(p.TypesInfo.Uses[v])
						if d == "exec.Command" || d == "exec.CommandContext" {
							return true // described with its program at the call, below
						}
						if d != "" {
							add(v, d)
						}
					case *ast.CompositeLit:
						if isExecCmd(p.TypesInfo.TypeOf(v)) {
							add(v, "exec.Cmd{}")
						}
					case *ast.CallExpr:
						if d := startsViaSyscall(p.TypesInfo, v); d != "" {
							add(v, d)
						}
						if d := execCommandDescriptor(p.TypesInfo, v); d != "" {
							add(v, d)
						}
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

// guardTargets is every GOOS x GOARCH the scans load.
func guardTargets() [][2]string {
	var out [][2]string
	for _, os := range guardGOOS {
		for _, arch := range guardGOARCH {
			out = append(out, [2]string{os, arch})
		}
	}
	return out
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
	for f, descs := range found {
		if f == "internal/testhelpers/gitenv.go" {
			continue
		}
		for _, d := range descs {
			assert.NotContains(t, d, `("git")`, "%s: git goes through gitenv", f)
		}
	}
	assert.Empty(t, importers, "non-test packages must not import internal/testhelpers, which starts git itself")
}
