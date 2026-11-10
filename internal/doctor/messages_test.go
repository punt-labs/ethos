package doctor

import (
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
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
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
			for _, term := range promiseTerms {
				if strings.Contains(low, term) {
					t.Errorf("%s: message %q contains %q", path, v, term)
				}
			}
			return true
		})
	}
}
