package doctor

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// promiseTerms are phrases a doctor message must not contain. A message states
// what doctor found and where; it never says another component will repair it,
// because that component may not run (a dormant repo, a shared hooks path).
var promiseTerms = []string{
	"next ethos run", "reinstalls", "refreshes it", "relocates it",
	"retires it", "removes them", "repairs it", "repairs a file",
	"storage upgrade", "re-run", "rerun", "inspect before", "add it before",
	"file a bug", "check file", "install ", "upgrade ethos", "move each",
	"ethos mission correct",
}

// userChoices are the literals (file:line) that may name a command because
// the command is a genuine user choice (plan section 7). Recording a
// correction is a human decision ethos never makes on its own.
var userChoices = map[string]bool{
	"hand_edit.go:108": true,
}

// TestDoctorMessages walks the string literals of this package and of the
// hook vacuum probe and fails on any that promises a repair or tells the
// reader to act.
func TestDoctorMessages(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	vacuum, err := filepath.Glob(filepath.Join("..", "hook", "audit_repository_vacuum.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range append(files, vacuum...) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			// "would install" describes a comparison; it does not tell the reader to act.
			low := strings.ReplaceAll(strings.ToLower(v), "would install", "")
			at := fmt.Sprintf("%s:%d", filepath.Base(path), fset.Position(lit.Pos()).Line)
			for _, term := range promiseTerms {
				if strings.Contains(low, term) && !userChoices[at] {
					t.Errorf("%s: message %q contains %q", path, v, term)
				}
			}
			return true
		})
	}
}
