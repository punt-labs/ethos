package layout_test

import (
	"path"
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
		{".punt-labs/ethos/missions/m-1.contract.yaml", layout.Blame, true},
		{".punt-labs/ethos/missions-closed/m-2026-10-24-001.result.yaml", layout.Blame, true},
		{".punt-labs/ethos/missions-closed/m-2026-10-24-001.contract.yaml", layout.Blame, true},
		// A fallback never takes a path another entry claims.
		{".punt-labs/ethos/missions/m-1/artifacts/m-1.contract.yaml", layout.Trace, true},
		{".punt-labs/ethos/identities/m-1.contract.yaml", layout.Source, true},
		{".punt-labs/ethos/missions/m-1/delegations/d-1/prompt.md", layout.Trace, true},
		{".punt-labs/ethos/missions/m-1/reflections.yaml", layout.Trace, true},
		{".punt-labs/ethos/missions/m-1/artifacts/notes.md", layout.Trace, true},
		{".punt-labs/local/ethos/trace/missions/m-1/reflections.yaml", layout.Trace, true},
		// Class 4 wins over the class that would otherwise match.
		{".punt-labs/ethos/missions/m-1/.lock", layout.Never, true},
		{".punt-labs/ethos/missions/.create.lock", layout.Never, true},
		{".punt-labs/ethos/missions/m-1/delegations/d-1/record.yaml.tmp", layout.Never, true},
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
		{"in tree", root + "/.githooks", []string{".githooks/pre-commit", ".githooks/commit-msg"}, []string{"pre-commit", "x/pre-commit"}},
		{"nested", root + "/tools/git hooks", []string{"tools/git hooks/pre-commit"}, nil},
		{"metacharacters", root + "/h[1]*", []string{"h[1]*/pre-commit"}, []string{"h1/pre-commit"}},
		{"outside", t.TempDir(), nil, []string{"pre-commit"}},
		{"git dir", root + "/.git/hooks", nil, []string{".git/hooks/pre-commit"}},
		{"root itself", root, []string{"pre-commit"}, nil},
	}
	for _, tt := range tests {
		table := layout.TableFor(root, tt.hooksDir)
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

// conflicts returns every pair of entries of table that can match one path,
// have different classes, and are not class 4. Class 4 is exempt because the
// table resolves it by order.
func conflicts(table []layout.Entry) []string {
	var out []string
	for i, a := range table {
		for _, b := range table[i+1:] {
			if a.Class == b.Class || a.Class == layout.Never || b.Class == layout.Never || a.Fallback || b.Fallback {
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
		"TableFor": layout.TableFor("/repo", "/repo/.githooks"),
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
