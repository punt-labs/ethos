package mission

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestAbortedVerdictWriteSites_MatchKnownThree pins review finding K4
// (full-branch review, m-2026-09-08-004 round 3): countBlockingDelegations's
// unconditional aborted-verdict exclusion (see that function's doc
// comment, and this test's own cross-reference from it) is safe only
// because DelegationVerdictAborted is written by exactly three call
// sites, all of which fire BEFORE a worker process starts. Nothing in
// the type system enforces that count — a fourth writer (a "cancel a
// running worker" command is the obvious future candidate) would
// silently let a delegation whose worker did real work stop blocking
// Abandon, with no event, no attestation, no error. This test
// enumerates every WRITE-position occurrence of DelegationVerdictAborted
// across every non-test .go file under internal/ and cmd/, and fails if
// the set of enclosing sites ever changes from the known three.
//
// "Write position" means: the identifier appears as the right-hand
// side of an assignment, an argument to a function call, the value
// half of a composite-literal key:value pair (e.g.
// Delegation{Verdict: DelegationVerdictAborted}), or the initializer of
// a var/const spec (e.g. a package-level alias). This deliberately
// excludes shapes the identifier legitimately appears in without
// writing a verdict: its own const declaration, a switch-case label
// validating an incoming verdict string (CloseDelegation,
// CloseDelegationSkeleton), and the read-side comparison in
// countBlockingDelegations itself (d.Verdict == DelegationVerdictAborted).
//
// Review finding J2 (full-branch review, m-2026-09-08-004 round 3):
// this test's first version parsed a HARDCODED three-file list and only
// recognized *ast.AssignStmt/*ast.CallExpr as write positions —
// falsified empirically by (a) a fourth writer in a NEW, unwatched file
// calling CloseDelegation(..., DelegationVerdictAborted, ...), which
// the hardcoded list could never see, and (b) a
// Delegation{Verdict: DelegationVerdictAborted} composite literal in a
// WATCHED file, whose parent node is *ast.KeyValueExpr, which the
// narrower write-position set did not recognize. Both are fixed here:
// filepath.WalkDir over the whole tree (skipping _test.go) replaces the
// file list, and *ast.KeyValueExpr/*ast.ValueSpec join the write-position
// set alongside *ast.AssignStmt/*ast.CallExpr.
//
// Follows internal/mcpclass's own drift-guard discipline (parsing
// source directly rather than trusting a comment) — see
// mcpclass_test.go's TestIdentityMethodsMatchGate for the precedent
// this pattern is modeled on.
func TestAbortedVerdictWriteSites_MatchKnownThree(t *testing.T) {
	sites, err := scanTreeForAbortedWriteSites(t, filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("scanning for DelegationVerdictAborted write sites: %v", err)
	}
	sort.Strings(sites)

	wantSites := []string{
		"internal/hook/pretooluse_dispatch.go:closeDelegationAborted",
		"internal/hook/subagent_start.go:closeSkeletonOnHashRefusal",
		"internal/mission/store.go:Close",
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

// scanTreeForAbortedWriteSites walks every non-test .go file under
// root/internal and root/cmd and returns the sorted, deduplicated set
// of "relative/path.go:site" strings naming every WRITE-position
// occurrence of DelegationVerdictAborted, where site is the enclosing
// function name, or "var <name>" for a package-level var/const
// initializer outside any function.
func scanTreeForAbortedWriteSites(t *testing.T, root string) ([]string, error) {
	t.Helper()
	var sites []string
	for _, sub := range []string{"internal", "cmd"} {
		dir := filepath.Join(root, sub)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				rel = path
			}
			for _, site := range abortedWriteSitesInFile(f) {
				sites = append(sites, filepath.ToSlash(rel)+":"+site)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return sites, nil
}

// abortedWriteSitesInFile returns the deduplicated names of every site
// in f (a top-level function, or "var <name>" for a package-level
// var/const declaration) containing at least one WRITE-position
// occurrence of DelegationVerdictAborted.
func abortedWriteSitesInFile(f *ast.File) []string {
	v := &abortedWriteVisitor{seen: map[string]bool{}}
	ast.Walk(v, f)
	return v.sites
}

// abortedWriteVisitor walks a file with parent tracking (via
// go/ast.Walk's documented nil-call-on-children-done contract),
// recording the enclosing site name each time DelegationVerdictAborted
// is found in a write position.
type abortedWriteVisitor struct {
	stack []ast.Node
	seen  map[string]bool
	sites []string
}

func (v *abortedWriteVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		v.stack = v.stack[:len(v.stack)-1]
		return nil
	}
	v.stack = append(v.stack, n)
	if isAbortedVerdictIdent(n) && v.isWritePosition(n) {
		site := v.enclosingSiteName()
		if !v.seen[site] {
			v.seen[site] = true
			v.sites = append(v.sites, site)
		}
	}
	return v
}

// isWritePosition reports whether n's immediate parent (the second-to-last
// stack entry, since n itself is the last) uses n as a write position:
// an assignment RHS, a call argument, the value half of a composite-literal
// key:value pair, or a var/const spec's initializer. A switch-case label
// (*ast.CaseClause) and a binary comparison (*ast.BinaryExpr) are NOT
// write positions and fall through to the false default.
func (v *abortedWriteVisitor) isWritePosition(n ast.Node) bool {
	if len(v.stack) < 2 {
		return false
	}
	switch p := v.stack[len(v.stack)-2].(type) {
	case *ast.AssignStmt:
		for _, rhs := range p.Rhs {
			if rhs == n {
				return true
			}
		}
	case *ast.CallExpr:
		for _, arg := range p.Args {
			if arg == n {
				return true
			}
		}
	case *ast.KeyValueExpr:
		return p.Value == n
	case *ast.ValueSpec:
		for _, val := range p.Values {
			if val == n {
				return true
			}
		}
	}
	return false
}

// enclosingSiteName walks the stack outward from the current node to
// find the nearest *ast.FuncDecl (named by its function name) or, for a
// package-level var/const declaration outside any function, the
// declaration's first name prefixed with "var ".
func (v *abortedWriteVisitor) enclosingSiteName() string {
	for i := len(v.stack) - 2; i >= 0; i-- {
		switch d := v.stack[i].(type) {
		case *ast.FuncDecl:
			return d.Name.Name
		case *ast.ValueSpec:
			if len(d.Names) > 0 {
				return "var " + d.Names[0].Name
			}
			return "var (unnamed)"
		}
	}
	return "package scope"
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
