package mission

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestAbortedVerdictWriteSites_MatchKnownThree pins review finding K4
// (full-branch review, m-2026-09-08-004 round 3): countBlockingDelegations's
// unconditional aborted-verdict exclusion (see that function's doc
// comment, and this test's own cross-reference from it) is safe only
// because every DelegationVerdictAborted writer is UNREACHABLE FROM
// THE GATE while its mission is still open — never because a writer
// happens to fire "before a worker starts" (review finding A,
// full-branch review, m-2026-09-08-004 round 3, correcting this
// comment's own prior wording, which contradicted its own wantSites
// list three lines below: store.go's Close sweep is one of the three
// known sites, and it can stamp aborted on a delegation whose worker
// DID run). The actual invariant, verified in countBlockingDelegations's
// own doc comment (store.go): two of the three sites
// (pretooluse_dispatch.go's closeDelegationAborted,
// subagent_start.go's hash-refusal cleanup) genuinely refuse before the
// worker starts; the third (store.go's close sweep) can fire against a
// delegation whose worker ran, but ONLY after close has committed the terminal
// contract and required event and returned successfully from its locked section.
// Close and CloseWithOutcome delegate to this same guarded implementation;
// TestCloseWrappersDelegateToGuardedWriter pins that chain separately. Meanwhile,
// countBlockingDelegations is only ever reached from Abandon's gate 1,
// which itself refuses unless the mission is StatusOpen. So no writer
// this function ever actually sees can have stamped aborted on a
// delegation whose mission was open at the time. Nothing in the type
// system enforces that reachability argument — a fourth writer (a
// "cancel a running worker" command is the obvious future candidate)
// that stamped aborted on an OPEN mission's delegation would silently
// let a delegation whose worker did real work stop blocking Abandon,
// with no event, no attestation, no error. This test enumerates every
// WRITE-position occurrence of DelegationVerdictAborted across every
// non-test .go file under internal/ and cmd/, and fails if the set of
// enclosing sites ever changes from the known three — a new site means
// the reachability argument above needs re-proving from scratch, not
// silently inheriting an exclusion built for a different set of
// callers.
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
	if err := knownAbortedWriteSitesError(sites); err != nil {
		t.Fatal(err)
	}
}

func knownAbortedWriteSitesError(sites []string) error {
	sort.Strings(sites)
	wantSites := []string{
		"internal/hook/pretooluse_dispatch.go:closeDelegationAborted",
		"internal/hook/subagent_start.go:closeSkeletonOnHashRefusal",
		"internal/mission/store.go:close",
	}
	if len(sites) != len(wantSites) {
		return fmt.Errorf("DelegationVerdictAborted is written from %v (%d site(s)), want exactly %v (%d) — "+
			"a new writer means countBlockingDelegations's unconditional aborted exclusion "+
			"(store.go's own doc comment) needs re-justifying, not silently inheriting an "+
			"exclusion built for a different set of callers",
			sites, len(sites), wantSites, len(wantSites))
	}
	for i, got := range sites {
		if got != wantSites[i] {
			return fmt.Errorf("DelegationVerdictAborted write sites are %v, want %v — "+
				"a renamed or moved writer needs the same re-justification as a new one",
				sites, wantSites)
		}
	}
	return nil
}

func TestAbortedWriterGuardRejectsSourceMutations(t *testing.T) {
	files := map[string]string{}
	for _, path := range []string{
		"internal/hook/pretooluse_dispatch.go",
		"internal/hook/subagent_start.go",
		"internal/mission/store.go",
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		files[path] = string(data)
	}
	const storePath = "internal/mission/store.go"
	for _, tc := range []struct{ name, old, replacement, extraPath string }{
		{"remove writer", "delegationVerdict = DelegationVerdictAborted", "delegationVerdict = DelegationVerdictPass", ""},
		{"rename writer", "func (s *Store) close(", "func (s *Store) movedClose(", ""},
		{"extra site in existing file", "", "", storePath},
		{"extra site in new file", "", "", "cmd/new_writer.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			requireWrite := func(path, src string) {
				t.Helper()
				path = filepath.Join(root, filepath.FromSlash(path))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for path, src := range files {
				requireWrite(path, src)
			}
			if err := os.MkdirAll(filepath.Join(root, "cmd"), 0o700); err != nil {
				t.Fatal(err)
			}
			sites, err := scanTreeForAbortedWriteSites(t, root)
			if err != nil {
				t.Fatal(err)
			}
			if err := knownAbortedWriteSitesError(sites); err != nil {
				t.Fatalf("unmutated actual-source fixture must pass: %v", err)
			}
			if tc.old != "" {
				src := files[storePath]
				if strings.Count(src, tc.old) != 1 {
					t.Fatalf("mutation anchor %q must occur exactly once", tc.old)
				}
				requireWrite(storePath, strings.Replace(src, tc.old, tc.replacement, 1))
			} else {
				prefix := files[tc.extraPath]
				if prefix == "" {
					prefix = "package main\n"
				}
				requireWrite(tc.extraPath, prefix+"\nfunc extraAbortWriter() { closeDelegation(DelegationVerdictAborted) }\n")
			}
			sites, err = scanTreeForAbortedWriteSites(t, root)
			if err != nil {
				t.Fatalf("source mutation must remain parseable: %v", err)
			}
			if err := knownAbortedWriteSitesError(sites); err == nil {
				t.Fatal("aborted writer guard accepted source mutation")
			}
		})
	}
}

// Outcome reporting moved the implementation, not its permission guards. Pin
// both public wrappers' one Store call and unchanged mission/status arguments.
func closeWrapperDelegationError(src string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "mutation_outcome.go", src, 0)
	if err != nil {
		return err
	}
	expected := map[string]string{
		"Close":            "s.CloseWithOutcome(id, status)",
		"CloseWithOutcome": "s.close(id, status, &out)",
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		want, ok := expected[fd.Name.Name]
		if !ok {
			continue
		}
		var calls []string
		var printErr error
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != "s" {
				return true
			}
			var buf bytes.Buffer
			if err := format.Node(&buf, fset, call); err != nil {
				printErr = err
			}
			calls = append(calls, buf.String())
			return true
		})
		if printErr != nil {
			return printErr
		}
		if len(calls) != 1 || calls[0] != want {
			return fmt.Errorf("%s Store calls = %v, want exactly %s", fd.Name.Name, calls, want)
		}
		delete(expected, fd.Name.Name)
	}
	if len(expected) != 0 {
		return fmt.Errorf("missing close wrappers: %v", expected)
	}
	return nil
}

func TestCloseWrappersDelegateToGuardedWriter(t *testing.T) {
	data, err := os.ReadFile("mutation_outcome.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if err := closeWrapperDelegationError(src); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"compatibility bypass", "s.CloseWithOutcome(id, status)", "s.close(id, status, nil)"},
		{"changed status", "s.close(id, status, &out)", "s.close(id, StatusClosed, &out)"},
		{"removed delegation", "s.close(id, status, &out)", "unrelated(id, status)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(src, tc.old) != 1 {
				t.Fatalf("mutation anchor %q must occur exactly once", tc.old)
			}
			if err := closeWrapperDelegationError(strings.Replace(src, tc.old, tc.replacement, 1)); err == nil {
				t.Fatal("wrapper delegation guard accepted source mutation")
			}
		})
	}
}

// scanTreeForAbortedWriteSites walks every non-test .go file under
// root/internal and root/cmd and returns the deduplicated set of
// "relative/path.go:site" strings naming every WRITE-position
// occurrence of DelegationVerdictAborted, where site is the enclosing
// function name, or "var <name>" for a package-level var/const
// initializer outside any function. The result is in filepath.WalkDir
// order (all of internal/ before all of cmd/, lexical within each) —
// NOT sorted; knownAbortedWriteSitesError sorts before comparing against wantSites.
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
// key:value pair, a var/const spec's initializer, or a return value. A
// switch-case label (*ast.CaseClause) and a binary comparison
// (*ast.BinaryExpr) are NOT write positions and fall through to the
// false default.
//
// *ast.ReturnStmt (review finding F, full-branch review,
// m-2026-09-08-004 round 3) closes a gap the walker otherwise missed
// silently: a helper like `func abortVerdict() string { return
// DelegationVerdictAborted }` returns the identifier directly, one
// indirection away from being a write itself, and its own callers would
// register only as an ordinary *ast.CallExpr naming the helper — never
// as a DelegationVerdictAborted occurrence at all, since the identifier
// itself does not appear at the call site.
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
	case *ast.ReturnStmt:
		for _, result := range p.Results {
			if result == n {
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
