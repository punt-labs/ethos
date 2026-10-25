package layout_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, path string
		want          bool
	}{
		{"a/b", "a/b", true},
		{"a/b", "a/c", false},
		{"a/*", "a/b", true},
		{"a/*", "a/b/c", false},
		{"a/**", "a", true},
		{"a/**", "a/b/c", true},
		{"a/**/z", "a/z", true},
		{"a/**/z", "a/b/c/z", true},
		{"a/**/z", "a/b/c", false},
		{"**/hook", "hook", true},
		{"**/hook", "x/y/hook", true},
		{"a/*.lock", "a/.lock", true},
		{"a/*.local.*", "a/q.local.yaml", true},
		{"a/*.local.*", "a/q.yaml", false},
	}
	for _, tt := range tests {
		if got := layout.Match(tt.pattern, tt.path); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

func TestOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		// The pair the round-1 witnesses missed.
		{"**/pre-commit", ".punt-labs/ethos/missions/*/artifacts/**", true},
		{".punt-labs/ethos/missions/*/artifacts/**", "**/pre-commit", true},
		{"a/b", "a/b", true},
		{"a/b", "a/c", false},
		{"a/*", "a/*/b", false},
		{"a/**", "a", true},
		{"a/**", "b/**", false},
		{"**/x", "a/**/y", false},
		{"**/x", "a/**/x", true},
		{"*.yaml", "contract.*", true},
		{"*.yaml", "*.md", false},
		{"log-*.jsonl", "log.jsonl", false},
		{"log-*.jsonl", "log-*.jsonl.corrupt*", true}, // log-x.jsonl.corrupt.jsonl matches both
		{"audit-*.jsonl", "audit-*", true},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"*", "**", true},
		{"a/*/c", "**/c", true},
		{`\*`, "*", true},
		{`\*`, "x", false},
		// ? is one rune, as in path.Match, not one byte.
		{"a?c", "aéc", true},
		{"a?c", "aééc", false},
		{"a??c", "aéc", false},
		{"é*", "éb", true},
	}
	for _, tt := range tests {
		if got := layout.Overlap(tt.a, tt.b); got != tt.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// witness returns a concrete path matching pattern.
func witness(pattern string) string {
	segs := strings.Split(pattern, "/")
	for i, s := range segs {
		if s == "**" {
			segs[i] = "x/y"
			continue
		}
		segs[i] = strings.ReplaceAll(s, "*", "x")
	}
	return strings.Join(segs, "/")
}

// TestOverlapAgreesWithMatch checks Overlap is sound against Match: when a
// witness of one pattern matches the other, they overlap.
func TestOverlapAgreesWithMatch(t *testing.T) {
	for _, a := range layout.Table {
		for _, b := range layout.Table {
			if layout.Match(a.Pattern, witness(b.Pattern)) && !layout.Overlap(a.Pattern, b.Pattern) {
				t.Errorf("%q matches a witness of %q but Overlap says disjoint", a.Pattern, b.Pattern)
			}
		}
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		path string
		want layout.Class
		ok   bool
	}{
		{".punt-labs/ethos/identities/bwk.yaml", layout.Source, true},
		{".punt-labs/ethos/identities/bwk.ext/quarry.yaml", layout.Source, true},
		{".punt-labs/ethos.yaml", layout.Source, true},
		{".gitmodules", layout.Source, true},
		{".claude/agents/bwk.md", layout.Source, true},
		{".punt-labs/ethos/missions/m-1.contract.yaml", layout.Blame, true},
		{".punt-labs/ethos/missions-closed/m-2026-10-24-001.result.yaml", layout.Blame, true},
		{".punt-labs/ethos/missions-closed/m-2026-10-24-001.contract.yaml", layout.Blame, true},
		// A fallback never takes a path another entry claims.
		{".punt-labs/ethos/missions/m-1/artifacts/m-1.contract.yaml", layout.Trace, true},
		{".punt-labs/ethos/identities/m-1.contract.yaml", layout.Source, true},
		{".punt-labs/ethos/missions/m-1/delegations/d-1/record.yaml", layout.Blame, true},
		{".punt-labs/ethos/missions/m-1/corrections.yaml", layout.Blame, true},
		{".punt-labs/ethos/missions.jsonl", layout.Blame, true},
		{".punt-labs/ethos/adrs/DES-079.yaml", layout.Blame, true},
		{".punt-labs/ethos/missions/m-1/delegations/d-1/prompt.md", layout.Trace, true},
		{".punt-labs/ethos/missions/m-1/reflections.yaml", layout.Trace, true},
		{".punt-labs/ethos/missions/m-1/artifacts/notes.md", layout.Trace, true},
		{".punt-labs/local/ethos/trace/missions/m-1/reflections.yaml", layout.Trace, true},
		// Class 4 wins over the class that would otherwise match.
		{".punt-labs/ethos/missions/m-1/.lock", layout.Never, true},
		{".punt-labs/ethos/missions/.create.lock", layout.Never, true},
		{".punt-labs/ethos/missions/m-1/delegations/d-1/record.yaml" + layout.TmpMarker, layout.Never, true},
		{".punt-labs/ethos/identities/bwk.ext/quarry.local.yaml", layout.Never, true},
		{".punt-labs/ethos/identities/bwk.yaml.lock", layout.Never, true},
		{".punt-labs/local/ethos/missions/m-1/s-1.lock", layout.Never, true},
		// No entry.
		{".punt-labs/ethos/ethos.yaml", 0, false},
		{".punt-labs/ethos/unknown/file", 0, false},
		{".punt-labs/ethos/README.md", 0, false},
		{".punt-labs/ethos/missions-closed/notes.yaml", 0, false},
		{".githooks/pre-commit", 0, false}, // known only through HookEntries
		{"README.md", 0, false},
	}
	for _, tt := range tests {
		got, e, ok := layout.Classify(tt.path)
		if ok != tt.ok || got != tt.want {
			t.Errorf("Classify(%q) = %v, %q, %v; want %v, ok=%v", tt.path, got, e.Pattern, ok, tt.want, tt.ok)
		}
	}
}

func TestClassifyNormalizesPath(t *testing.T) {
	for _, p := range []string{
		"./.punt-labs/ethos/missions.jsonl",
		".punt-labs//ethos/missions.jsonl",
		".punt-labs/ethos/./missions.jsonl",
	} {
		if c, _, ok := layout.Classify(p); !ok || c != layout.Blame {
			t.Errorf("Classify(%q) = %v, %v; want blame", p, c, ok)
		}
	}
}

func TestHookEntries(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name     string
		hooksDir string
		want     []string // paths that must classify as source; nil means no entries
		not      []string
	}{
		{"in tree", root + "/.githooks", []string{".githooks/pre-commit", ".githooks/commit-msg", ".githooks/pre-merge-commit"}, []string{"pre-commit", "x/pre-commit"}},
		{"nested", root + "/tools/git hooks", []string{"tools/git hooks/pre-commit"}, nil},
		{"metacharacters", root + "/h[1]*", []string{"h[1]*/pre-commit"}, []string{"h1/pre-commit"}},
		{"outside", t.TempDir(), nil, []string{"pre-commit"}},
		{"git dir", root + "/.git/hooks", nil, []string{".git/hooks/pre-commit"}},
		{"root itself", root, []string{"pre-commit"}, nil},
		{"ethos zone", root + "/.punt-labs/ethos/hooks", nil, []string{".punt-labs/ethos/hooks/pre-commit"}},
		{"punt-labs itself", root + "/.punt-labs", nil, []string{".punt-labs/pre-commit"}},
	}
	for _, tt := range tests {
		table := mustTableFor(t, root, tt.hooksDir)
		for _, p := range tt.want {
			if c, _, ok := layout.ClassifyIn(table, p); !ok || c != layout.Source {
				t.Errorf("%s: ClassifyIn(%q) = %v, %v; want source", tt.name, p, c, ok)
			}
		}
		for _, p := range tt.not {
			if _, _, ok := layout.ClassifyIn(table, p); ok {
				t.Errorf("%s: ClassifyIn(%q) matched; want no entry", tt.name, p)
			}
		}
	}
}

// TestHookEntriesSymlinkedRoot: a hooks directory that does not exist yet must
// resolve against a symlinked repo root as an existing one does (macOS /var).
func TestHookEntriesSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, root := range []string{link, real} {
		for _, dir := range []string{link, real} {
			table := mustTableFor(t, root, filepath.Join(dir, ".githooks"))
			if c, _, ok := layout.ClassifyIn(table, ".githooks/pre-commit"); !ok || c != layout.Source {
				t.Errorf("root %s, hooks dir under %s: ClassifyIn = %v, %v; want source", root, dir, c, ok)
			}
		}
	}
}

func mustTableFor(t *testing.T, root, hooksDir string) []layout.Entry {
	t.Helper()
	table, err := layout.TableFor(root, hooksDir)
	if err != nil {
		t.Fatalf("TableFor(%q, %q): %v", root, hooksDir, err)
	}
	return table
}

// TestHookEntriesUnresolvablePath: a symlink loop is neither "does not exist"
// nor a path to guess about; the error reaches the caller.
func TestHookEntriesUnresolvablePath(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := layout.HookEntries(root, filepath.Join(root, "loop", "hooks")); err == nil {
		t.Error("HookEntries through a symlink loop returned no error")
	}
	if _, err := layout.TableFor(root, filepath.Join(root, "loop", "hooks")); err == nil {
		t.Error("TableFor through a symlink loop returned no error")
	}
}

func TestPuntLabsHookPaths(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name, hooksDir string
		want           []string
	}{
		{"githooks", root + "/.punt-labs/githooks", []string{".punt-labs/githooks/pre-commit", ".punt-labs/githooks/commit-msg", ".punt-labs/githooks/pre-merge-commit"}},
		{"in zone", root + "/.punt-labs/ethos/hooks", []string{".punt-labs/ethos/hooks/pre-commit", ".punt-labs/ethos/hooks/commit-msg", ".punt-labs/ethos/hooks/pre-merge-commit"}},
		{"in tree", root + "/.githooks", nil},
		{"git dir", root + "/.git/hooks", nil},
		{"outside", t.TempDir(), nil},
	}
	for _, tt := range tests {
		got, err := layout.PuntLabsHookPaths(root, tt.hooksDir)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
		}
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestTablePatternsWellFormed(t *testing.T) {
	for _, e := range layout.Table {
		for _, seg := range strings.Split(e.Pattern, "/") {
			if _, err := path.Match(seg, ""); err != nil {
				t.Errorf("pattern %q: bad segment %q: %v", e.Pattern, seg, err)
			}
			if strings.ContainsAny(seg, "[\\") {
				t.Errorf("pattern %q: segment %q uses a character class or escape, which Overlap does not model", e.Pattern, seg)
			}
		}
		if e.Class < layout.Source || e.Class > layout.Never {
			t.Errorf("pattern %q: invalid class %d", e.Pattern, e.Class)
		}
	}
}

// TestClassFourComesFirst pins the one overlap the table resolves by order.
func TestClassFourComesFirst(t *testing.T) {
	seenOther := false
	for _, e := range layout.Table {
		if e.Class != layout.Never {
			seenOther = true
		} else if seenOther {
			t.Errorf("class-4 entry %q follows a non-class-4 entry", e.Pattern)
		}
	}
}

// conflicts returns every pair of entries of table that can match one path and
// have different classes. Two kinds of pair are exempt: any pair with a class 4
// entry, which the table resolves by order, and a Fallback entry paired with a
// non-fallback one, because a fallback claims only paths no other entry
// matches. Two fallbacks are not exempt: nothing orders them.
func conflicts(table []layout.Entry) []string {
	var out []string
	for i, a := range table {
		for _, b := range table[i+1:] {
			if a.Class == b.Class || a.Class == layout.Never || b.Class == layout.Never || a.Fallback != b.Fallback {
				continue
			}
			if layout.Overlap(a.Pattern, b.Pattern) {
				out = append(out, a.Pattern+" ("+a.Class.String()+") overlaps "+b.Pattern+" ("+b.Class.String()+")")
			}
		}
	}
	return out
}

// unclassified returns the paths no entry of table matches.
func unclassified(table []layout.Entry, paths []string) []string {
	var out []string
	for _, p := range paths {
		if _, _, ok := layout.ClassifyIn(table, p); !ok {
			out = append(out, p)
		}
	}
	return out
}

func TestEntriesAreExclusive(t *testing.T) {
	c := writerCorpus(t)
	for _, s := range conflicts(c.table) {
		t.Errorf("overlapping classes: %s", s)
	}
}

// TestTableForIsExclusive: with core.hooksPath at a directory where a class-2 or
// class-3 entry's files live (or at that file's own path), TableFor must add no
// source entry that overlaps it. HookEntries meets this by refusing directories
// inside .punt-labs/; the test fails if that refusal is narrowed, or if an
// entry is added whose witness directory lies elsewhere.
func TestTableForIsExclusive(t *testing.T) {
	root := t.TempDir()
	dirs := []string{".githooks"}
	for _, e := range layout.Table {
		if e.Class == layout.Source || e.Class == layout.Never {
			continue
		}
		w := witness(e.Pattern)
		dirs = append(dirs, w, path.Dir(w))
	}
	for _, dir := range dirs {
		for _, s := range conflicts(mustTableFor(t, root, filepath.Join(root, dir))) {
			t.Errorf("hooks dir %s: overlapping classes: %s", dir, s)
		}
	}
}

// TestExclusivityDetectsOverlap is the mutant: entries the round-1 witnesses
// missed, a class-2/class-3 overlap, and the same shapes as class 4.
func TestExclusivityDetectsOverlap(t *testing.T) {
	with := func(extra ...layout.Entry) []layout.Entry {
		return append(append([]layout.Entry{}, layout.Table...), extra...)
	}
	for _, e := range []layout.Entry{
		{Pattern: ".punt-labs/ethos/missions/*/*.yaml", Class: layout.Trace},
		{Pattern: "**/pre-commit", Class: layout.Source},
	} {
		if got := conflicts(with(e)); len(got) == 0 {
			t.Errorf("overlap of %q with the table not detected", e.Pattern)
		}
	}
	for _, e := range []layout.Entry{
		{Pattern: ".punt-labs/ethos/missions/*/contract.yaml", Class: layout.Never},
	} {
		if got := conflicts(with(e)); len(got) != 0 {
			t.Errorf("class-4 overlap reported: %v", got)
		}
	}
}

func TestEveryWriterPathIsClassified(t *testing.T) {
	c := writerCorpus(t)
	for _, p := range unclassified(c.table, c.paths) {
		t.Errorf("writer created %s, which no entry of layout.Table matches", p)
	}
}

// TestCoverageDetectsUnmatchedWriter is the mutant: a writer path with no
// entry, and a table that lost an entry a writer needs, must both be reported.
func TestCoverageDetectsUnmatchedWriter(t *testing.T) {
	c := writerCorpus(t)
	paths := append(append([]string{}, c.paths...), ".punt-labs/ethos/newthing/file.bin")
	got := unclassified(c.table, paths)
	if len(got) != 1 || got[0] != ".punt-labs/ethos/newthing/file.bin" {
		t.Errorf("unclassified = %v, want the new writer path only", got)
	}

	var trimmed []layout.Entry
	for _, e := range c.table {
		if e.Pattern != ".punt-labs/ethos/missions/*/contract.yaml" {
			trimmed = append(trimmed, e)
		}
	}
	if got := unclassified(trimmed, c.paths); len(got) == 0 {
		t.Error("removing the contract.yaml entry left every writer path classified")
	}
}

// TestFallbackComesLast: a fallback entry only claims paths nothing else does,
// so no non-fallback entry may follow one, with or without hook entries.
func TestFallbackComesLast(t *testing.T) {
	for name, table := range map[string][]layout.Entry{
		"Table":    layout.Table,
		"TableFor": mustTableFor(t, "/repo", "/repo/.githooks"),
	} {
		seen := false
		for _, e := range table {
			if e.Fallback {
				seen = true
			} else if seen {
				t.Errorf("%s: non-fallback entry %q follows a fallback entry", name, e.Pattern)
			}
		}
		if !seen {
			t.Errorf("%s has no fallback entry", name)
		}
	}
}

// fill substitutes what os.CreateTemp and os.MkdirTemp would for the last "*".
func fill(pattern string) string {
	i := strings.LastIndex(pattern, "*")
	if i < 0 {
		return pattern + "123456"
	}
	return pattern[:i] + "123456" + pattern[i+1:]
}

// TestTempHelpersMakeClassFourNames: whatever base name a helper is given, in
// whatever directory the writer uses it, the name contains TmpMarker and
// classifies as class 4. A base the table has never heard of (a new hook, a
// symlink target in an unlisted directory) is covered because no entry names it.
func TestTempHelpersMakeClassFourNames(t *testing.T) {
	bases := []string{"post-merge", "AGENTS.md", "shared.ignore", "pre-commit", ".vendored-manifest", "x"}
	dirs := []string{"", "docs", ".githooks", ".punt-labs/ethos", ".punt-labs/ethos/missions/m-1/delegations/d-1", "a/b/c"}
	for _, h := range tempHelperCases {
		for _, b := range bases {
			n := h.name(b)
			if !strings.Contains(n, layout.TmpMarker) {
				t.Errorf("%s(%q) = %q does not contain %q", h.fn, b, n, layout.TmpMarker)
			}
			for _, d := range dirs {
				p := path.Join(d, n)
				if c, e, ok := layout.Classify(p); !ok || c != layout.Never {
					t.Errorf("%s(%q) in %q: %q classified %v, %q, %v; want class 4", h.fn, b, d, p, c, e.Pattern, ok)
				}
			}
		}
	}
}

// tempHelperCases exercises each name-building function of temp.go. fn is the
// exported function the case covers; a function may have several cases, one per
// shape of name it yields.
var tempHelperCases = []struct {
	fn   string
	name func(base string) string
}{
	{"TmpPath", layout.TmpPath},
	{"DotTmp", layout.DotTmp},
	{"SeedTmpPattern", func(b string) string { return fill(layout.SeedTmpPattern(b)) }},
	{"DotTempPattern", func(b string) string { return fill(layout.DotTempPattern(b)) }},
	{"SiblingTempPattern", func(b string) string { return fill(layout.SiblingTempPattern(b)) }},
	{"TmpPattern", func(b string) string { return fill(layout.TmpPattern(b, ".yaml")) }},
	{"ScratchDirPattern", func(b string) string { return fill(layout.ScratchDirPattern(b)) }},
	{"ScratchDirPattern", func(b string) string { return fill(layout.ScratchDirPattern(b)) + "/hooks/pre-commit" }},
	{"MigrateStagingPrefix", func(b string) string { return fill(layout.MigrateStagingPrefix(b)+"*") + "/contract.yaml" }},
	{"MigrateStagingPrefix", func(b string) string { return fill(layout.MigrateStagingPrefix(b) + "*") }},
}

// TestEveryTempHelperIsExercised: an exported function of temp.go that returns
// a string is a name builder, and must have a case in tempHelperCases, or a new
// helper could omit TmpMarker without failing TestTempHelpersMakeClassFourNames.
func TestEveryTempHelperIsExercised(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "temp.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, h := range tempHelperCases {
		covered[h.fn] = true
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || !fd.Name.IsExported() || fd.Type.Results == nil || len(fd.Type.Results.List) != 1 {
			continue
		}
		if id, ok := fd.Type.Results.List[0].Type.(*ast.Ident); ok && id.Name == "string" && !covered[fd.Name.Name] {
			t.Errorf("temp.go: %s returns a string but has no case in tempHelperCases", fd.Name.Name)
		}
	}
	for fn := range covered {
		found := false
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == fn {
				found = true
			}
		}
		if !found {
			t.Errorf("tempHelperCases names %s, which temp.go does not declare", fn)
		}
	}
}

// TestTempEntriesAreTheMarker: the table names the marker and nothing else for
// temporary names. Per-base entries are what let a name slip through; a second
// temp shape needs a deliberate edit here.
func TestTempEntriesAreTheMarker(t *testing.T) {
	var got []string
	for _, e := range layout.Table {
		if e.Temp {
			got = append(got, e.Pattern)
			if e.Class != layout.Never {
				t.Errorf("temp entry %q is class %v, want class 4", e.Pattern, e.Class)
			}
		}
	}
	want := []string{"**/*" + layout.TmpMarker + "*", "**/*" + layout.TmpMarker + "*/**"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("temp entries = %q, want %q", got, want)
	}
}

// TestMigrateStagingIsClassFour: files in the staging directory of an
// interrupted `ethos mission migrate` look like mission files
// (missions/*/contract.yaml), so only the explicit class-4 entry, which comes
// first, keeps them out of git.
func TestMigrateStagingIsClassFour(t *testing.T) {
	c := writerCorpus(t)
	n := 0
	for _, p := range c.paths {
		if !strings.Contains(p, ".m-2026-10-24-904.") {
			continue
		}
		n++
		if class, e, ok := layout.ClassifyIn(c.table, p); !ok || class != layout.Never || !e.Temp {
			t.Errorf("%s classified %v by %q, want a class-4 temp entry", p, class, e.Pattern)
		}
	}
	if n == 0 {
		t.Fatal("corpus holds no migrate staging file")
	}
}

// TestLegacyMigrateStagingIsClassFour: before the temp marker, `ethos mission
// migrate` staged in ".<id>.migrate-<random>"; an interrupted run of that
// version leaves files that look like mission files.
func TestLegacyMigrateStagingIsClassFour(t *testing.T) {
	for _, p := range []string{
		".punt-labs/ethos/missions/.m-1.migrate-123/contract.yaml",
		".punt-labs/ethos/missions/.m-1.migrate-123/results.yaml",
	} {
		class, e, ok := layout.Classify(p)
		if !ok || class != layout.Never {
			t.Errorf("%s classified %v by %q (ok=%v), want class 4", p, class, e.Pattern, ok)
		}
		if e.Temp {
			t.Errorf("%s matched a Temp entry %q; the marker set must stay marker-only", p, e.Pattern)
		}
	}
}

func TestConflicts(t *testing.T) {
	tests := []struct {
		name  string
		table []layout.Entry
		want  int
	}{
		{"same class", []layout.Entry{{Pattern: "a/**", Class: 1}, {Pattern: "a/b", Class: 1}}, 0},
		{"different classes overlap", []layout.Entry{{Pattern: "a/**", Class: 1}, {Pattern: "a/b", Class: 2}}, 1},
		{"class 4 is exempt", []layout.Entry{{Pattern: "a/**", Class: 1}, {Pattern: "a/b", Class: layout.Never}}, 0},
		{"fallback against non-fallback is exempt", []layout.Entry{{Pattern: "a/**", Class: 1, Fallback: true}, {Pattern: "a/b", Class: 2}}, 0},
		{"fallback against fallback is not", []layout.Entry{{Pattern: "a/**", Class: 1, Fallback: true}, {Pattern: "a/b", Class: 2, Fallback: true}}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := conflicts(tt.table); len(got) != tt.want {
				t.Errorf("conflicts = %v, want %d", got, tt.want)
			}
		})
	}
}
