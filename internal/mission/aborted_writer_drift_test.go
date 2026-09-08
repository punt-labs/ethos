package mission

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"
)

// TestAbortedVerdictWriteSites_MatchKnownThree pins review finding K4
// (full-branch review, m-2026-09-08-004 round 3): countBlockingDelegations's
// unconditional aborted-verdict exclusion is safe only because
// DelegationVerdictAborted is written by exactly three call sites, all
// of which fire BEFORE a worker process starts (see that function's own
// doc comment). Nothing in the type system enforces that count — a
// fourth writer (a "cancel a running worker" command is the obvious
// future candidate) would silently let a delegation whose worker did
// real work stop blocking Abandon, with no event, no attestation, no
// error. This test enumerates every WRITE-position occurrence of
// DelegationVerdictAborted across the two packages that can produce
// one and fails if the count or the set of enclosing functions ever
// changes from the known three.
//
// "Write position" means: the identifier appears as the right-hand
// side of an assignment, or as an argument to a function call. This
// deliberately excludes three other shapes the identifier legitimately
// appears in, none of which write a verdict:
//   - its own const declaration (internal/mission/delegation.go)
//   - a switch-case label validating an incoming verdict string
//     (CloseDelegation, CloseDelegationSkeleton)
//   - the read-side comparison in countBlockingDelegations itself
//     (d.Verdict == DelegationVerdictAborted)
//
// Follows internal/mcpclass's own drift-guard discipline (parsing
// source directly rather than trusting a comment) — see
// mcpclass_test.go's TestIdentityMethodsMatchGate for the precedent
// this pattern is modeled on.
func TestAbortedVerdictWriteSites_MatchKnownThree(t *testing.T) {
	files := []string{
		filepath.Join(".", "store.go"),
		filepath.Join("..", "hook", "pretooluse_dispatch.go"),
		filepath.Join("..", "hook", "subagent_start.go"),
	}

	var sites []string
	for _, path := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, fn := range enclosingFuncsWritingAbortedVerdict(f) {
			sites = append(sites, filepath.Base(path)+":"+fn)
		}
	}
	sort.Strings(sites)

	wantSites := []string{
		"pretooluse_dispatch.go:closeDelegationAborted",
		"store.go:Close",
		"subagent_start.go:closeSkeletonOnHashRefusal",
	}
	if len(sites) != len(wantSites) {
		t.Fatalf("DelegationVerdictAborted is written from %v (%d site(s)), want exactly %v (%d) — "+
			"a new writer means countBlockingDelegations's unconditional aborted exclusion "+
			"(store.go's own doc comment) needs re-justifying, not silently inheriting an "+
			"exclusion built for a different set of callers",
			sites, len(sites), wantSites, len(wantSites))
	}
	for i, got := range sites {
		if got != wantSites[i] {
			t.Fatalf("DelegationVerdictAborted write sites are %v, want %v — "+
				"a renamed or moved writer needs the same re-justification as a new one",
				sites, wantSites)
		}
	}
}

// enclosingFuncsWritingAbortedVerdict returns the name of every
// top-level function in f containing at least one WRITE-position
// occurrence of the DelegationVerdictAborted identifier (bare, or
// package-qualified as mission.DelegationVerdictAborted). "Write
// position" is an assignment right-hand side or a call argument, never
// a switch-case label or the operand of an == / != comparison.
func enclosingFuncsWritingAbortedVerdict(f *ast.File) []string {
	var funcs []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if abortedVerdictWrittenIn(fn.Body) {
			funcs = append(funcs, fn.Name.Name)
		}
	}
	return funcs
}

// abortedVerdictWrittenIn walks body with parent tracking (via
// go/ast.Walk's documented nil-call-on-children-done contract) and
// reports whether DelegationVerdictAborted appears as an assignment
// RHS or a call argument anywhere inside it.
func abortedVerdictWrittenIn(body ast.Node) bool {
	v := &abortedWriteVisitor{}
	ast.Walk(v, body)
	return v.found
}

type abortedWriteVisitor struct {
	stack []ast.Node
	found bool
}

func (v *abortedWriteVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		v.stack = v.stack[:len(v.stack)-1]
		return nil
	}
	v.stack = append(v.stack, n)
	if isAbortedVerdictIdent(n) && len(v.stack) >= 2 {
		switch v.stack[len(v.stack)-2].(type) {
		case *ast.AssignStmt, *ast.CallExpr:
			v.found = true
		}
	}
	return v
}

// isAbortedVerdictIdent reports whether n is the bare identifier
// DelegationVerdictAborted, or a package-qualified
// mission.DelegationVerdictAborted selector.
func isAbortedVerdictIdent(n ast.Node) bool {
	switch e := n.(type) {
	case *ast.Ident:
		return e.Name == "DelegationVerdictAborted"
	case *ast.SelectorExpr:
		return e.Sel.Name == "DelegationVerdictAborted"
	default:
		return false
	}
}
