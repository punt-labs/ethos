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

// The scan covers internal/, cmd/ and plugin/ only: tests/ is test
// infrastructure, not shipped code. Test files (_test.go) are not scanned.

// processStarts is the set of ways Go source starts another program.
// Each entry is the (package, function) of a call.
var processStarts = map[[2]string]bool{
	{"os", "StartProcess"}:  true,
	{"syscall", "Exec"}:     true,
	{"syscall", "ForkExec"}: true,
}

// allowedProcessUses names every non-test Go file outside this package that
// imports os/exec or starts a program, with exactly what it does there. A
// file not listed may do neither. A listed file may do exactly what is listed:
// a new call, a new program, or an alias import changes the list and fails the
// test, so a git command cannot slip in behind a variable, an alias or a
// wrapper. git itself is never listed: it goes through gitenv, which bounds it.
//
// Descriptors: "import" is os/exec imported (every file that starts a program
// has it; a file with nothing else imports it for its types and errors);
// `Command("x")` and `CommandContext("x")` start the literal program x;
// `Command(<expr>)` starts a program computed at run time (named below).
var allowedProcessUses = map[string][]string{
	// Types and errors only: these files classify an error from gitenv.
	"internal/audit/git.go":        {"import"},
	"internal/enable/enable.go":    {"import"},
	"internal/enable/gitignore.go": {"import"},
	"internal/githook/resolver.go": {"import"},
	"internal/resolve/resolve.go":  {"import"},

	// Own context deadlines, stated at each command.
	"internal/hook/working_context.go": {"import"}, // exec.LookPath only; its git runs through gitenv.Run with a 2s Limit
	"internal/doctor/sandbox.go": {
		"import",
		`CommandContext("sh")`,   // sandbox: re-run a hook that returned ENOEXEC through sh (TERM, then KILL)
		`CommandContext(<expr>)`, // sandbox: the hook itself, run in the sandbox repository (TERM, then KILL)
	},
	"internal/doctor/procgroup_unix.go":    {"import"},
	"internal/doctor/procgroup_windows.go": {"import"},

	// Not git.
	"cmd/ethos/ui.go":        {"import", `Command("open")`, `Command("xdg-open")`}, // opens the browser
	"cmd/ethos/attribute.go": {"import", `Command(<expr>)`},                        // $EDITOR
	"cmd/ethos/uninstall.go": {"import", `Command(<expr>)`},                        // the claude CLI

	// A test helper compiled into a non-test file: its callers set the bound.
	"internal/testhelpers/gitenv.go": {"import", `Command("git")`, `Command(<expr>)`},
}

// processUses returns, per file, the descriptors of every os/exec import and
// program start in it.
func processUses(t *testing.T, root string) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd", "plugin"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/gitenv/") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			names := map[string]string{} // local name -> import path, for the packages that start programs
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				switch path {
				case "os/exec", "os", "syscall":
				default:
					continue
				}
				local := path[strings.LastIndex(path, "/")+1:]
				if imp.Name != nil {
					local = imp.Name.Name
				}
				if path == "os/exec" {
					found[rel] = append(found[rel], "import")
					if local == "." {
						found[rel] = append(found[rel], "dot-import")
					}
				}
				names[local] = path
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.CallExpr:
					sel, ok := v.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkg, ok := sel.X.(*ast.Ident)
					if !ok {
						return true
					}
					path := names[pkg.Name]
					switch {
					case path == "os/exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext"):
						i := 0
						if sel.Sel.Name == "CommandContext" {
							i = 1
						}
						prog := "<expr>"
						if len(v.Args) > i {
							if lit, ok := v.Args[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								prog = strconv.Quote(mustUnquote(lit.Value))
							}
						}
						found[rel] = append(found[rel], sel.Sel.Name+"("+prog+")")
					case processStarts[[2]string{filepath.Base(path), sel.Sel.Name}]:
						found[rel] = append(found[rel], filepath.Base(path)+"."+sel.Sel.Name)
					}
				case *ast.CompositeLit:
					if sel, ok := v.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Cmd" {
						if pkg, ok := sel.X.(*ast.Ident); ok && names[pkg.Name] == "os/exec" {
							found[rel] = append(found[rel], "exec.Cmd{}")
						}
					}
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	for _, v := range found {
		sort.Strings(v)
	}
	return found
}

func mustUnquote(s string) string {
	u, err := strconv.Unquote(s)
	if err != nil {
		return s
	}
	return u
}

// Every program ethos starts is accounted for. git goes through gitenv
// (bounded by its deadline); the files that start anything else, or hold
// their own deadline, are listed above with exactly what they do.
func TestNoUnaccountedProcessStarts(t *testing.T) {
	found := processUses(t, filepath.Join("..", ".."))
	var files []string
	for f := range found {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		want, ok := allowedProcessUses[f]
		if !ok {
			t.Errorf("%s imports os/exec or starts a program outside gitenv (%v): run git through "+
				"gitenv.Output, OutputEnv or Run, or list the file in allowedProcessUses with what it does",
				f, found[f])
			continue
		}
		w := append([]string(nil), want...)
		sort.Strings(w)
		assert.Equal(t, w, found[f], "%s: the allowlist and the source disagree", f)
	}
	for f := range allowedProcessUses {
		assert.Contains(t, found, f, "%s is allowlisted but starts nothing: remove it from the list", f)
	}
}
