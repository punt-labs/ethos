package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestMessageGuardFoldsConsts pins const resolution: a message assembled from
// a package-level const and a literal is judged whole, including a const that
// refers to another const and one declared after its use.
func TestMessageGuardFoldsConsts(t *testing.T) {
	src := `package p
var a = "run " + probeCmd
var b = probeRemedy
var c = "fine " + probeOther
const probeRemedy = "hooks stale; " + probeCmd
const probeCmd = "ethos enable"
const probeOther = "text"
`
	f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	consts := map[string]string{}
	collectConsts(f, consts)
	collectConsts(f, consts)
	var found []string
	for _, d := range f.Decls {
		gd := d.(*ast.GenDecl)
		if gd.Tok != token.VAR {
			continue
		}
		v, ok := foldedString(gd.Specs[0].(*ast.ValueSpec).Values[0], consts)
		if ok && bannedIn(v) != "" {
			found = append(found, v)
		}
	}
	if len(found) != 2 {
		t.Errorf("want 2 hits (a and b), got %v", found)
	}
}
